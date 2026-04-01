# Balanced Consolidation PR Review

Review of the Balanced consolidation implementation on `gastown-dev` branch,
cross-referenced against RFC (jamesmt-aws/karpenter#8) and our prior RFC analysis.

## Assessment: **needs-work**

The core scoring formula is correctly implemented and RFC-aligned. However, there are
several issues ranging from a potential silent-discard bug to missing validation and
zero test coverage for the new code path.

---

## Issues Found

### 1. BLOCKER: `ComputeNodeDisruptionCost` silently discards negative eviction costs

**File:** `pkg/controllers/disruption/decisionratio.go:80-84`

`EvictionCost()` returns values in `[-10.0, 10.0]` (clamped). Pods with negative
priority or negative `pod-deletion-cost` produce negative eviction costs. The current
code silently skips these:

```go
if evictionCost > 0 {
    cost += evictionCost
}
```

This means a node full of low-priority pods (negative cost) reports `totalDisruption = 0`,
which triggers the `math.Inf(1)` return in `ComputeMoveScore` — auto-approving the move
regardless of savings. This is the opposite of the RFC intent: low-priority pods should
be *cheap* to disrupt (low disruption cost, high score), not *free* (infinite score).

**Fix:** Either include negative costs (let them reduce the total, clamping node cost
at 0), or use `math.Abs(evictionCost)` to treat all pods as having *some* disruption
weight. The RFC uses `EvictionCost` directly without filtering.

### 2. BLOCKER: No CEL validation that `disruptionTolerance` requires `Balanced` policy

**File:** `pkg/apis/v1/nodepool.go:103-113`, `pkg/apis/v1/nodepool_validation.go`

The CRD allows setting `disruptionTolerance: 5` with `consolidationPolicy: WhenEmpty`.
There's no CEL cross-field validation rule like:

```
rule: "self.consolidationPolicy == 'Balanced' || !has(self.disruptionTolerance)"
```

This won't cause a runtime error (the field is simply ignored for non-Balanced policies),
but it's a UX trap — operators think they're tuning something when they're not.

**Fix:** Add a CEL validation rule on the `disruption` object, or at minimum add a
warning-level admission webhook.

### 3. SHOULD-FIX: `ComputeNodePoolMetrics` uses `Offerings[0].Price` instead of compatible offering

**File:** `pkg/controllers/disruption/decisionratio.go:30-35`

```go
if candidate.instanceType != nil && len(candidate.instanceType.Offerings) > 0 {
    totalCost += candidate.instanceType.Offerings[0].Price
}
```

This takes the first offering's price, which may not match the candidate's actual
capacity type and zone. The rest of the codebase (e.g., `getCandidatePrices` in
`consolidation.go`) correctly filters offerings by the node's labels to find the
compatible price. A spot node could be priced at the on-demand rate (or vice versa)
if `Offerings[0]` happens to be a different capacity type.

**Fix:** Use the same pattern as `getCandidatePrices`:
```go
reqs := scheduling.NewLabelRequirements(candidate.Labels())
compatible := candidate.instanceType.Offerings.Compatible(reqs)
if len(compatible) > 0 {
    totalCost += compatible.Cheapest().Price
}
```

### 4. SHOULD-FIX: `checkBalancedScore` computes pool metrics from filtered candidates, not full pool

**File:** `pkg/controllers/disruption/consolidation.go:107`

`checkBalancedScore` receives `allCandidates` which is the list of *disruption-eligible*
candidates, not all nodes in the NodePool. Nodes that are not consolidatable (e.g.,
recently created, have do-not-disrupt annotation, owned by static NodePools) are excluded.
This means `totalCost` and `totalDisruption` undercount the true pool totals.

The RFC defines `nodepool_total_cost` and `nodepool_total_disruption_cost` as pool-wide
aggregates. Using only eligible candidates inflates `savings_fraction` and
`disruption_fraction` relative to the true pool, which may cancel out in the ratio —
but not always (e.g., if non-eligible nodes are disproportionately expensive).

**Fix:** Either document this as an intentional deviation, or compute pool-level metrics
from the full cluster state (all nodes in the NodePool, not just candidates).

### 5. SHOULD-FIX: Mixed-policy NodePool candidates in multi-node consolidation

**File:** `pkg/controllers/disruption/consolidation.go:95-98`

`isBalancedPolicy` returns true if *any* candidate uses Balanced. In multi-node
consolidation, candidates from different NodePools can be batched together. If one
NodePool is `Balanced` and another is `WhenEmptyOrUnderutilized`, the entire batch
gets scored against the Balanced threshold using the first Balanced candidate's `k`.

This means a `WhenEmptyOrUnderutilized` node that would normally always be consolidated
could be blocked by a low Balanced score from the mixed batch.

**Fix:** Either (a) filter multi-node batches to same-policy NodePools, or (b) apply
Balanced scoring only to the Balanced candidates within the batch and let
WhenEmptyOrUnderutilized candidates pass unconditionally.

### 6. SHOULD-FIX: `DisruptionReasonBalanced` not in budget enum but used in metrics/conditions

**File:** `pkg/apis/v1/nodepool.go:201-203`, `pkg/controllers/disruption/queue.go:242,268`

`DisruptionReasonBalanced = "Balanced"` is set as the `ReasonOverride` on commands, then
used in:
- `NodeClaimsDisruptedTotal` metric (reason label = `balanced`)
- `ConditionTypeDisruptionReason` on NodeClaim status (reason = `Balanced`)
- Event messages

But the budget enum is `{Underutilized,Empty,Drifted}`. The budget system works correctly
because it uses `Method.Reason()` (= `Underutilized`), not `Command.Reason()`. However:
- The NodeClaim condition shows `Balanced` while the budget tracks `Underutilized` — confusing for operators debugging budget behavior
- Metric dashboards filtering on `reason=underutilized` won't capture Balanced disruptions

**Fix:** Either (a) make Balanced a proper budget reason (add to enum, update validation),
or (b) keep the Method reason as `Underutilized` for metrics/conditions and only use
`Balanced` in human-readable event messages.

### 7. NICE-TO-HAVE: No unit tests for the Balanced code path

**Files:** `pkg/controllers/disruption/*_test.go`

Zero test coverage for:
- `ComputeMoveScore` (the core scoring function)
- `ComputeNodePoolMetrics`
- `ComputeNodeDisruptionCost`
- `checkBalancedScore` (threshold gating)
- `GetDisruptionToleranceThreshold`
- Single-node Balanced consolidation flow
- Multi-node Balanced consolidation flow
- Score below threshold → rejection
- REPLACE vs DELETE scoring differences
- Edge cases: zero cost pool, zero disruption, single-node pool

The `balanced_test.go` file listed in the bead description doesn't exist.

### 8. NICE-TO-HAVE: Histogram buckets may not capture the interesting range

**File:** `pkg/controllers/disruption/metrics.go:42`

Buckets: `{0, 0.1, 0.25, 0.5, 0.75, 1.0, 1.5, 2.0, 3.0, 5.0, 10.0}`

The default threshold is 0.5 (k=2). Most interesting scores cluster around the
threshold. The gap between 0.25 and 0.5 is large — adding 0.4 would help operators
see how many moves are "just below" threshold.

### 9. NICE-TO-HAVE: `checkBalancedScore` log line has redundant fields

**File:** `pkg/controllers/disruption/consolidation.go:145-153`

The log line includes both `"score", score` and `"consolidation_score", score` — same
value, two keys. Remove one.

---

## RFC Alignment Check

| RFC Requirement | Implementation | Status |
|----------------|---------------|--------|
| `score = savings_fraction / disruption_fraction` | `ComputeMoveScore` | ✅ Correct |
| `savings_fraction = (deleted - replacement) / total_pool_cost` | Lines 53-57 | ✅ Correct |
| `disruption_fraction = move_disruption / total_pool_disruption` | Lines 63-72 | ⚠️ Pool totals use eligible candidates only (#4) |
| Per-pod disruption from `EvictionCost` | `ComputeNodeDisruptionCost` | ⚠️ Negative costs silently dropped (#1) |
| No per-node baseline cost | Comment on line 77 | ✅ Correct |
| `threshold = 1/k`, default k=2 | `GetDisruptionToleranceThreshold` | ✅ Correct |
| Policy name `Balanced` | `ConsolidationPolicyBalanced` | ✅ Correct |
| Field name `disruptionTolerance` | `DisruptionTolerance *int32` | ✅ Correct |
| Zero disruption → approve if savings positive | Lines 63-65 return `+Inf` | ✅ Correct |
| Move-level scoring (multi-node) | `ComputeMoveScore` sums candidates | ✅ Correct |
| Three-policy spectrum | `ShouldDisrupt` allows Balanced + WhenEmptyOrUnderutilized | ✅ Correct |

---

## Summary

- **2 blockers**: negative eviction cost bug (#1), missing cross-field validation (#2)
- **4 should-fix**: wrong price lookup (#3), pool metrics scope (#4), mixed-policy batching (#5), reason label confusion (#6)
- **3 nice-to-have**: no tests (#7), histogram gaps (#8), duplicate log field (#9)

The scoring formula core is solid and RFC-aligned. The main risks are edge-case
correctness (negative costs, mixed policies) and operability (confusing reason labels,
no validation guardrails). Recommend fixing blockers and should-fix items before merge.
