# RFC Review: jamesmt-aws/karpenter#8 — Balanced Consolidation Policy

Review of PR #8 (supersedes PR #4). Covers RFC design, PR discussion insights,
comparison to fork implementation (commit 013941a), and implications for
simulator + KWOK verification.

## 1. RFC Summary

PR #8 proposes `consolidationPolicy: Balanced` with a `disruptionTolerance` parameter
(default k=2, threshold 0.5). The scoring formula:

```
score = savings_fraction / disruption_fraction
```

Where:
- `savings_fraction = (deleted_cost - replacement_cost) / nodepool_total_cost`
- `disruption_fraction = move_disruption_cost / nodepool_total_disruption_cost`
- Per-pod disruption cost from existing `EvictionCost` (pod-deletion-cost + priority), default 1.0
- Move approved when `score >= 1/k` (0.5 at default k=2)

Three policies on one spectrum: `WhenEmpty` (k=0), `Balanced` (k=2), `WhenEmptyOrUnderutilized` (k=∞).

### Key changes from PR #4 → PR #8

1. Renamed `WhenSavingsJustifyDisruption` → `Balanced`
2. Added `disruptionTolerance` field (integer, default 2) — replaces fixed threshold=1.0
3. Threshold verification section with exhaustive enumeration over c7i/m7i/r7i families
4. k=2 justified as smallest integer fixing the k=1 design hole (uniform pool REPLACEs never pass at k=1)
5. File renames: `consolidation-cost-threshold.md` → `balanced-consolidation.md`

## 2. Key Discussion Insights (from PR #4 + #8)

### Kube-scheduler placement gap (shreyas-badiger)
The most substantive concern. Karpenter assumes pods land on intended destinations, but
kube-scheduler may scatter them across existing nodes. A replacement node K ends up
underutilized, triggering another consolidation cycle. The RFC acknowledges the threshold
reduces but doesn't eliminate this. MostAllocated scheduling strategy is the real fix.
The Workload-Aware Scheduling proposal (Kepka, Feb 2026) addresses this more directly.

### Pod-deletion-cost adoption (bwagner5)
Without `pod-deletion-cost` annotations, every pod has disruption cost 1.0 and the score
reduces to pod-count-vs-savings. The score's main differentiator (distinguishing expensive
pods from cheap ones) depends on customers setting this annotation. PR #2894 would automate it.

### Uniform disruption cost invariance
Setting every pod to the same disruption cost (e.g., all 10) cancels in the ratio:
`(n*k)/(N*k) = n/N`. No effect on any score. Users who set all pods to maximum gain nothing.

### Reserved/ODCR nodes
Zero-cost nodes produce zero savings → never consolidated by scoring. Emptiness controller
still handles empty zero-cost nodes. Opportunity cost modeling deferred.

## 3. Comparison: Fork Implementation (013941a) vs RFC (PR #8)

| Aspect | Fork (013941a) | RFC PR #8 |
|--------|----------------|-----------|
| Policy name | `WhenCostJustifiesDisruption` | `Balanced` |
| Threshold field | `DecisionRatioThreshold` (float64, default 1.0) | `disruptionTolerance` (integer k, default 2, threshold = 1/k = 0.5) |
| Default threshold | 1.0 (break-even) | 0.5 (k=2) |
| Per-node baseline | 1.0 baseline disruption cost per node | No per-node baseline (open question) |
| Zero disruption | Returns +Inf (always approve) | Approve if savings positive (DELETE) or strictly positive (REPLACE) |
| Scoring scope | Per-candidate (single node cost / nodepool cost) | Per-move (sum of deleted nodes - sum of created nodes) |
| API field name | `consolidateWhen` | `consolidationPolicy` (existing field, new enum value) |

### Critical divergences requiring fork updates

1. **Threshold default**: Fork uses 1.0, RFC uses 0.5 (k=2). At threshold 1.0, no REPLACE
   passes in a uniform pool. The fork's default is the k=1 design hole the RFC explicitly fixes.

2. **Per-node baseline**: Fork adds `baselineNodeCost = 1.0` to every node's disruption cost.
   RFC does not include this — it's listed as an open question. The baseline means empty nodes
   have disruption cost 1.0 in the fork but 0.0 in the RFC.

3. **Move-level vs candidate-level scoring**: Fork computes ratio per-candidate (single node).
   RFC defines scoring per-move (which may involve multiple deleted nodes and replacement nodes).
   The fork doesn't account for replacement node cost in the ratio — it uses the full node cost
   as savings, which is only correct for DELETEs, not REPLACEs.

4. **Field naming**: Fork uses `DecisionRatioThreshold` (direct threshold). RFC uses
   `disruptionTolerance` (inverse: threshold = 1/k). Different mental model for operators.

## 4. Implications for Simulator (kubesim)

### Simulator code changes needed (crates/kubesim-karpenter/src/consolidation.rs)

1. **Update default threshold from 1.0 to 0.5** — Match RFC's k=2 default. The current
   simulator threshold of 1.0 is the design hole where no REPLACE passes in uniform pools.

2. **Implement move-level scoring** — Current simulator likely scores per-candidate like the
   fork. RFC scores per-move: `savings = sum(deleted) - sum(created)`. For single-node
   consolidation this is equivalent, but multi-node moves differ.

3. **Remove per-node baseline disruption cost** (if present) — RFC does not include the
   `baselineNodeCost = 1.0` that the fork adds. Empty nodes should have disruption cost 0.

4. **Add disruptionTolerance parameter** — Expose k as a configurable integer (default 2)
   rather than a direct threshold float.

### KWOK verification changes needed

1. **Heterogeneous node types are required** — Uniform KWOK node types (all same instance)
   produce score=1.0 for all DELETEs and identical sub-threshold scores for all REPLACEs.
   No gradient, no differentiation. KWOK templates must use mixed types (e.g., m7i.xlarge +
   m7i.2xlarge + m7i.4xlarge) for CostJustified/Balanced to produce meaningful results.

2. **500→350 transition (CostJustified activation zone)** — This is where nodes are partially
   filled, not empty. Emptiness won't claim them. With heterogeneous types, Balanced scoring
   should produce a gradient: oversized nodes with few pods score high (approved), well-packed
   nodes score low (rejected). Measure per-node scores during this transition.

3. **350→10 transition (Emptiness dominance zone)** — Most nodes drain to zero pods. Emptiness
   method should dominate. Measure separately to confirm Balanced doesn't interfere — empty
   nodes have zero disruption cost, so DELETEs are always approved regardless of threshold.

4. **Pod disruption cost variation** — Add `pod-deletion-cost` annotations to a subset of pods
   (e.g., model-serving pods with cost 134217728 ≈ eviction cost ~10). Without this, all pods
   have cost 1.0 and the score reduces to pod-count-vs-savings, losing the main differentiator.

5. **Cross-family scenarios** — k=3 opens 8 additional cross-family replacement pairs that k=2
   blocks. Test with c7i + m7i + r7i mixed pools to verify cross-family behavior.

## 5. Action Items

### Simulator (kubesim-karpenter)
- [ ] Change default threshold from 1.0 to 0.5 (or parameterize as k=2)
- [ ] Verify scoring uses move-level savings (deleted - created), not just candidate node cost
- [ ] Remove per-node baseline disruption cost if present
- [ ] Add k parameter to consolidation config

### KWOK verification
- [ ] Update KWOK templates to use mixed instance types (m7i.xlarge + m7i.2xlarge + m7i.4xlarge minimum)
- [ ] Add pod-deletion-cost annotations to subset of pods for disruption cost differentiation
- [ ] Instrument 500→350 transition to measure per-node scores and CostJustified activation
- [ ] Instrument 350→10 transition separately to confirm Emptiness dominance
- [ ] Add k=3 cross-family test scenario

### Karpenter fork
- [ ] Rename `WhenCostJustifiesDisruption` → `Balanced` to match RFC
- [ ] Replace `DecisionRatioThreshold` (float, default 1.0) with `disruptionTolerance` (int, default 2)
- [ ] Evaluate removing `baselineNodeCost = 1.0` per-node baseline (RFC omits it)
- [ ] Update `ComputeDecisionRatio` to handle REPLACE moves (subtract replacement cost from savings)

## References

- RFC PR #8 (open): https://github.com/jamesmt-aws/karpenter/pull/8
- Superseded PR #4 (closed): https://github.com/jamesmt-aws/karpenter/pull/4
- Fork implementation: commit 013941a on feat/decision-ratio-consolidation-control
- Automated pod-deletion-cost: https://github.com/kubernetes-sigs/karpenter/pull/2894
- Decision ratio threshold PR: https://github.com/kubernetes-sigs/karpenter/pull/2893
- Workload-Aware Scheduling proposal: Kepka, Feb 2026
