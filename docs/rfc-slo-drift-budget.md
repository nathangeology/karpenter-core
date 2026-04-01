# RFC: SLO-Based Drift Budget with Consolidation-Drift Cooperation

| Field | Value |
|-------|-------|
| **Authors** | Nathaniel Jones (nathangeology) |
| **Status** | Draft |
| **Created** | 2026-04-01 |
| **Last Updated** | 2026-04-01 |
| **Reviewers** | SIG-Autoscaling, Karpenter Maintainers |
| **Tracking** | [PR #2930](https://github.com/kubernetes-sigs/karpenter/pull/2930), [PR #2927](https://github.com/kubernetes-sigs/karpenter/pull/2927) |

## Summary

This RFC proposes making drift and consolidation cooperative rather than competitive by (1) preferring drifted nodes in consolidation candidate selection and (2) introducing an SLO-based drift annotation that dynamically allocates disruption budget based on drift completion deadlines. Together, these changes reduce total disruptions, eliminate drift-consolidation starvation, and let users express outcomes (complete drift in N days) rather than mechanisms (reserve X% of budget). Operators of large clusters with continuous drift will see fewer total disruptions and predictable drift completion timelines without manual budget tuning.

## Motivation

Karpenter's disruption budgets do not behave the way their configuration suggests. Users configure per-reason budgets (e.g., "10% for drift, 5% for consolidation") expecting independent pools, but the runtime uses a single shared `disrupting` counter across all reasons. When drift consumes 10 of 15 allowed disruptions, consolidation sees only 5 remaining — even if its own configured budget is 5%. The YAML looks like a partition but behaves like a shared pool with soft hints.

This creates a concrete operational problem: drift and consolidation compete for the same budget, and because the disruption controller processes drift before consolidation, drift can exhaust the shared budget before consolidation ever runs. Worse, the controller's restart-on-success loop means that during continuous drift (e.g., a rolling AMI update across hundreds of nodes), consolidation is mathematically starved — it never gets a turn.

The impact is significant for operators running large, long-lived clusters:

- **As a cluster operator**, I want drift to complete within a predictable time window, so that I can meet compliance and security patching SLAs.
- **As a platform engineer**, I want consolidation to continue running during drift events, so that I don't accumulate unnecessary cost from underutilized nodes.
- **As an SRE**, I want fewer total node disruptions, so that my workloads experience less churn and my PDBs are respected more effectively.

If we do nothing, operators must choose between timely drift completion and cost optimization. There is no configuration that achieves both simultaneously under the current model.

## Goals and Non-Goals

### Goals

- **Make drift and consolidation cooperative**: A single disruption should serve both purposes when a drifted node is also a consolidation candidate.
- **Reduce total disruption count**: By preferring drifted nodes during consolidation, fewer separate disruption actions are needed to achieve the same outcomes.
- **Express drift completion as an SLO**: Users specify a time window (e.g., 7 days) rather than a budget percentage, and Karpenter self-tunes the drift rate.
- **Eliminate consolidation starvation during drift**: Consolidation must continue to make progress even during large-scale drift events.
- **Maintain backward compatibility**: Existing configurations without the new annotation behave identically to today.
- **Keep the interface simple**: One annotation on NodePool, no new CRDs, no multi-level budget hierarchies.

### Non-Goals

- **General per-reason budget isolation**: This RFC does not implement independent budget pools for every disruption reason. It solves the drift-consolidation conflict specifically.
- **Cluster-wide disruption budgets**: Cross-NodePool budget coordination (e.g., a standalone DisruptionBudget CRD) is out of scope.
- **Replacing the existing budget model**: The current `disruption.budgets` API is unchanged. This RFC layers cooperative behavior on top.
- **Guaranteeing SLO achievement under all conditions**: If disruption limits or PDBs prevent sufficient throughput, Karpenter will emit warnings and complete drift as fast as possible, but the SLO is best-effort.

### Future Goals

- Per-reason budget isolation (Phase 3) if Phases 1 and 2 do not fully resolve user pain.
- Extension of SLO-based annotations to other disruption reasons beyond drift.

## Proposal / Design

We propose a three-phase approach. Phase 1 is a minimal, low-risk change that makes drift and consolidation cooperative. Phase 2 introduces the SLO annotation for adaptive drift pacing. Phase 3 is a contingency for full per-reason budget isolation if needed.

### Phase 1: Drift-Priority in Consolidation Candidate Selection

When evaluating single-node and multi-node consolidation candidates (Underutilized reason), we sort nodes that are marked for drift higher in the candidate list. This means consolidation naturally removes drifted nodes first — one disruption achieves both cost savings and drift progress.

#### Implementation

The change is in `sortCandidates()` in `consolidation.go`. When comparing two candidate nodes, a node with an active drift condition is ranked higher (disrupted sooner) than a node without one.

```go
// In sortCandidates(), add drift-priority tiebreaker:
// Nodes pending drift sort before non-drifted nodes
func sortCandidates(candidates []*Candidate) {
    sort.SliceStable(candidates, func(i, j int) bool {
        iDrifted := candidates[i].StateNode.DriftedCondition() != nil
        jDrifted := candidates[j].StateNode.DriftedCondition() != nil
        if iDrifted != jDrifted {
            return iDrifted
        }
        // ... existing sort criteria (cost, utilization, etc.)
    })
}
```

#### Properties

- **Safety**: No new disruptions are created. We only reorder which nodes consolidation picks first.
- **Efficiency**: One disruption serves two purposes (cost + drift), reducing total disruption count.
- **Scope**: ~5 lines of code change. No API changes. No new configuration.

#### Example

Consider a NodePool with 100 nodes, 10 of which are drifted and underutilized. Today, consolidation might remove 5 non-drifted underutilized nodes while drift separately removes 5 drifted nodes — 10 total disruptions. With Phase 1, consolidation preferentially removes the 5 drifted underutilized nodes — 5 disruptions achieve the same outcome.

### Phase 2: Drift SLO Annotation

A new annotation on NodePool defines the time window within which all current drifts should complete:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  annotations:
    karpenter.sh/drift-slo: "7d"   # Complete all drifts within 7 days
spec:
  # ... existing spec unchanged
```

#### Algorithm

Karpenter dynamically computes the drift budget share each reconciliation cycle:

1. **Inputs**: `remaining_drifted_nodes` (nodes with drift condition not yet disrupted), `remaining_time` (time until SLO deadline), `total_budget` (NodePool's disruption budget per cycle).
2. **Compute required rate**: `drift_share = ceil(remaining_drifted_nodes / (remaining_time_cycles * total_budget))`
3. **Clamp**: `drift_share = clamp(drift_share, 0, total_budget)`
4. **Allocate**: Drift gets `drift_share` of the budget. Consolidation gets `total_budget - currently_disrupting - drift_share`.

```
drift_share = ⌈ remaining_drifted / remaining_cycles ⌉
consolidation_available = total_budget - disrupting - drift_share
```

#### Temporal Behavior

- **Early in window** (e.g., day 1 of 7): Many cycles remain. `drift_share` is small. Consolidation runs freely.
- **Mid window** (e.g., day 4 of 7): `drift_share` increases as remaining time shrinks. Drift and consolidation share budget roughly equally.
- **Near deadline** (e.g., day 6 of 7): `drift_share` dominates. Drift takes priority to meet the SLO.
- **Past deadline**: Drift gets maximum priority. Warning event emitted on NodePool.

#### Tracking Drift Progress

We track drift state per NodePool:

- **Drift epoch start**: Timestamp when the first drifted node appeared (or annotation was set).
- **Remaining drifted nodes**: Count of nodes with `Drifted` condition that have not yet been disrupted.
- **SLO deadline**: `epoch_start + drift-slo duration`.

This state is derived from existing NodeClaim conditions — no new persistent storage is needed.

#### Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `karpenter.sh/drift-slo` | Duration string | (none) | Time window to complete all drifts. If unset, drift uses current behavior. |

When the annotation is absent, behavior is identical to today — full backward compatibility.

#### Example

A NodePool has 100 nodes. An AMI update drifts all 100. The operator sets `drift-slo: 7d` with a disruption budget of `10%` (10 nodes per cycle).

| Day | Remaining | Cycles Left | drift_share | consolidation_available |
|-----|-----------|-------------|-------------|------------------------|
| 1 | 100 | ~168 | 1 | 9 |
| 3 | 70 | ~120 | 1 | 9 |
| 5 | 30 | ~48 | 1 | 9 |
| 6 | 15 | ~24 | 1 | 9 |
| 6.5 | 8 | ~12 | 1 | 9 |

In this scenario, drift needs only ~1 slot per cycle to meet the SLO, leaving 9 for consolidation. If drift falls behind (e.g., PDB blocks), the share automatically increases.

### Phase 3: Per-Reason Budget Isolation (Contingency)

If Phases 1 and 2 do not fully resolve user pain, we pursue per-reason budget isolation:

1. **Track disruption reason on StateNode**: Add a `DisruptionReason` field to `StateNode` (not just the NodeClaim condition).
2. **Per-reason disrupting counters**: Modify `BuildDisruptionBudgetMapping` to count disruptions by reason.
3. **Independent budget deduction**: Each reason's budget is decremented only by its own in-flight disruptions.

This is a larger change and is deferred unless Phases 1 and 2 prove insufficient. The SLO-based approach does not preclude this work.

## Alternatives Considered

### Alternative: HTB-Based Disruption Budget Model (PR #2930)

Hierarchical Token Bucket (HTB) borrows from Linux traffic control to give each disruption reason a guaranteed minimum rate and a ceiling, with unused capacity flowing to an excess pool.

**Pros**: Fixes the budget semantic gap precisely. Work-conserving. Generalizes to all disruption reasons. Well-understood model.

**Cons**: Significant implementation complexity (per-reason tracking, HTB computation, new CRD). Excess pool fairness is undefined — first-come-first-served still favors drift over consolidation. Gives users more knobs to turn rather than expressing their actual goal. Does not make drift and consolidation cooperative.

**Rejected because**: It solves the mechanism (budget partitioning) rather than the outcome (timely drift without starving consolidation). The SLO approach is simpler, more aligned with user intent, and reduces total disruptions through cooperation.

### Alternative: CFS-Based Scheduling (PR #2927)

Completely Fair Scheduler-inspired approach that gives each disruption reason proportional time slices.

**Pros**: Fair allocation across reasons. Prevents starvation by design.

**Cons**: Adds scheduling complexity to the disruption controller. Does not reduce total disruptions. Does not make drift and consolidation cooperative. Proportional fairness may not match user intent — users want drift to complete on time, not to get exactly 50% of budget.

**Rejected because**: Fairness is not the goal — timely drift completion with minimal disruption is. CFS treats all reasons equally when users have asymmetric requirements.

### Alternative: Do Nothing

Operators continue to experience consolidation starvation during drift events. They must choose between timely drift and cost optimization by manually adjusting budgets during drift windows.

**Rejected because**: The problem is well-documented, affects real operators, and has a straightforward solution. The cost of inaction is ongoing operational toil and unnecessary infrastructure spend.

## Risks and Mitigations

| Risk | Severity | Mitigation |
|------|----------|------------|
| Phase 1 sort change alters consolidation behavior for all users | Low | Sort is stable; only tiebreaking changes. Non-drifted clusters see no difference. |
| Drift SLO annotation misconfigured (e.g., unrealistically short window) | Medium | Emit warning events when SLO is unachievable. Clamp drift_share to total_budget. |
| Drift progress tracking adds memory overhead | Low | State is derived from existing NodeClaim conditions, not new storage. |
| Phase 2 algorithm edge case: zero remaining time | Medium | When past deadline, set drift_share = total_budget (maximum priority). Emit warning. |
| Consolidation starved if SLO deadline is very tight | Medium | Document that tight SLOs reduce consolidation budget. Users control the trade-off explicitly. |

### Backward Compatibility

Both phases are fully backward compatible. Phase 1 changes only the sort order of consolidation candidates — no API changes. Phase 2 activates only when the `karpenter.sh/drift-slo` annotation is present. Existing configurations without the annotation behave identically to today.

### Performance Implications

Phase 1 adds a single boolean comparison to the sort function — negligible overhead. Phase 2 adds a count of drifted nodes and a division per reconciliation cycle — also negligible. No new API calls or external lookups are required.

## Testing and Validation

### Unit Tests

- **Phase 1**: `sortCandidates` returns drifted nodes before non-drifted nodes when both are consolidation candidates.
- **Phase 1**: Sort stability — non-drifted candidate ordering is unchanged.
- **Phase 2**: `drift_share` computation returns correct values for various remaining_nodes / remaining_time combinations.
- **Phase 2**: `drift_share` clamps to total_budget when deadline has passed.
- **Phase 2**: `drift_share` is 0 when no nodes are drifted.
- **Phase 2**: Consolidation available budget is correctly computed as `total - disrupting - drift_share`.

### Integration Tests

- Simulate a NodePool with drifted and non-drifted underutilized nodes; verify consolidation removes drifted nodes first.
- Simulate a drift SLO with a 7-day window; verify drift_share increases as the deadline approaches.
- Simulate SLO expiry; verify drift gets maximum priority and warning event is emitted.

### Edge Cases

- All nodes are drifted (consolidation candidates are all drifted — sort is a no-op, which is correct).
- No nodes are drifted (drift_share = 0, consolidation gets full budget — identical to today).
- Drift SLO annotation removed mid-drift (revert to current behavior immediately).
- Drift SLO annotation changed mid-drift (recompute deadline from new value).
- PDBs block drift disruptions (drift_share increases but actual throughput is limited — warning emitted).

### Metrics and Observability

- `karpenter_drift_slo_remaining_seconds` (Gauge): Time remaining until drift SLO deadline, per NodePool.
- `karpenter_drift_slo_remaining_nodes` (Gauge): Drifted nodes not yet disrupted, per NodePool.
- `karpenter_drift_slo_share` (Gauge): Current drift budget share, per NodePool.
- `karpenter_disruption_cooperative_count` (Counter): Disruptions that served both consolidation and drift purposes.
- Warning event on NodePool when SLO deadline is exceeded.

## Migration and Rollout Plan

### Phase 1 (Drift-Priority Sort)

1. **No feature gate**: This is a sort-order change with no user-visible configuration. It ships as a default behavior improvement.
2. **Rollout**: Included in the next minor release.
3. **Rollback**: Revert the sort change. No data migration needed.

### Phase 2 (Drift SLO Annotation)

1. **Opt-in via annotation**: The feature activates only when `karpenter.sh/drift-slo` is set. No feature gate needed — absence of annotation = current behavior.
2. **Rollout**: Ship in the release following Phase 1. Document the annotation in the Karpenter docs under Disruption > Drift.
3. **Migration**: No action required for existing users. Users who want the feature add the annotation.
4. **Rollback**: Remove the annotation. Drift reverts to current behavior immediately.

### Phase 3 (If Needed)

Deferred. Would require a feature gate and more extensive migration planning due to API changes.

### Documentation

- Update Karpenter docs: Disruption Budgets section to explain cooperative drift-consolidation behavior.
- New docs page: Drift SLO configuration guide with examples.
- Update troubleshooting guide: "Why is my consolidation not running during drift?" → explain Phase 1 behavior and drift SLO option.

## Open Questions

1. **Should the drift SLO be an annotation or an API field?** An annotation is simpler to ship and iterate on. A formal API field provides better validation and discoverability. We lean toward annotation for Phase 2, with promotion to API field if the feature proves valuable. *[For Karpenter Maintainers]*

2. **How should we define the drift epoch start?** Options: (a) timestamp of the first drifted node in the current drift wave, (b) timestamp when the annotation is set/changed, (c) always use the oldest drifted node's timestamp. We lean toward (a). *[For SIG-Autoscaling]*

3. **Should Phase 1 apply to all consolidation types or only Underutilized?** Empty-node consolidation already removes nodes regardless of drift status. The sort change is most impactful for single-node and multi-node (Underutilized) consolidation. We lean toward Underutilized only. *[For Karpenter Maintainers]*

4. **What is the right default behavior when drift_share exceeds available budget?** Options: (a) drift takes priority and consolidation gets zero, (b) maintain a minimum consolidation floor (e.g., 1 slot). We lean toward (a) since the user explicitly set a tight SLO. *[For SIG-Autoscaling]*

5. **Should we emit a Kubernetes Event or a Condition when the SLO is at risk?** Events are transient and good for alerts. Conditions are persistent and good for status inspection. We lean toward both: a warning Event when projected to miss SLO, and a Condition on NodePool when SLO is actively exceeded. *[For Karpenter Maintainers]*
