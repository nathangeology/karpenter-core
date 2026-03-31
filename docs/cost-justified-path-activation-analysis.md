# CostJustified Disruption Path: Scale-Dependent Activation Analysis

**Bead:** kp-758
**Date:** 2026-03-31
**Branch:** feat/decision-ratio-consolidation-control (commit 013941a + 8a3d165)

---

## Summary

The CostJustified disruption path activates at 50 replicas but not at 500 because
the decision ratio formula produces uniform values when all nodes are identical —
and at full scale with uniform nodes, the ratio equals exactly 1.0 for every
candidate, causing the `shouldExecuteCommand` threshold check to pass only at the
boundary. The real issue is that the **Emptiness method runs first** and claims all
empty nodes, while **Underutilized (single/multi-node consolidation) claims the
rest** — both before the CostJustified path's ratio filtering has any practical
effect.

## The Exact Code Path

### Method execution order (controller.go, `NewMethods()`):

```
1. Emptiness           — deletes empty nodes (any policy)
2. StaticDrift         — drift for static pools
3. Drift               — drift for dynamic pools
4. MultiNodeConsolidation — consolidates multiple underutilized nodes
5. SingleNodeConsolidation — consolidates single underutilized nodes
```

Methods run in order. The first method that produces a command wins; the controller
returns immediately and re-queues.

### How ShouldDisrupt filters candidates (consolidation.go):

```go
func (c *consolidation) ShouldDisrupt(ctx context.Context, cn *Candidate) bool {
    effectivePolicy := resolveConsolidationPolicy(cn.NodePool)
    switch effectivePolicy {
    case v1.ConsolidateWhenEmptyOrUnderutilized:
        // Allow all — no filtering
    case v1.ConsolidateWhenCostJustifiesDisruption:
        // Allow all — ratio filtering deferred to computeConsolidation
    case v1.ConsolidateWhenEmpty:
        return false  // Block non-empty consolidation
    }
    return cn.NodeClaim.StatusConditions().Get(v1.ConditionTypeConsolidatable).IsTrue()
}
```

For `WhenCostJustifiesDisruption`, `ShouldDisrupt` returns true for all
consolidatable nodes — identical to `WhenEmptyOrUnderutilized`. The ratio
filtering happens later in `computeConsolidation` and `shouldExecuteCommand`.

### The Emptiness method (emptiness.go) has its own ShouldDisrupt:

```go
func (e *Emptiness) ShouldDisrupt(_ context.Context, c *Candidate) bool {
    // Runs for ALL policies — no consolidateWhen check
    return len(c.reschedulablePods) == 0 &&
        c.NodeClaim.StatusConditions().Get(v1.ConditionTypeConsolidatable).IsTrue()
}
```

**Emptiness does not check `consolidateWhen` at all.** It claims every empty node
regardless of policy. This is correct behavior — empty nodes have zero disruption
cost — but it means the `CostJustified/` path never sees empty nodes.

## Why 50 Replicas Activates CostJustified But 500 Does Not

### At 50 replicas (smoke test):

- Nodes are partially filled after scale-down
- Some nodes have pods but are underutilized (not empty)
- Emptiness claims the truly empty nodes
- Multi/SingleNodeConsolidation evaluates remaining non-empty nodes
- `computeConsolidation` computes decision ratios
- With heterogeneous utilization, ratios vary → some exceed threshold
- `shouldExecuteCommand` passes → CostJustified path activates
- Log entries show `decision.ratio` and `CostJustified/` path

### At 500 replicas (full scale):

- After scale-down from 500→0 replicas, nodes drain completely
- **All nodes become empty** (zero reschedulable pods)
- Emptiness (method #1) claims **every node** as empty
- Multi/SingleNodeConsolidation (methods #4/#5) see **zero candidates**
- `computeConsolidation` is never called
- Decision ratio is never computed
- CostJustified path never activates
- All disruptions logged as `Empty/` path

The scale-down pattern matters: at 500 replicas, the deployment deletion causes
all pods to terminate, leaving nodes fully empty. At 50 replicas, bin-packing
means some nodes retain pods from other workloads or partial scheduling, creating
the mixed utilization that triggers the CostJustified evaluation.

## The Decision Ratio Formula at Uniform Scale

Even if nodes weren't all empty at 500 replicas, there's a second issue. The
decision ratio formula (decisionratio.go):

```
normalizedCost      = nodeCost / totalCost
normalizedDisruption = nodeDisruption / totalDisruption
decisionRatio       = normalizedCost / normalizedDisruption
```

When all N nodes have identical instance types and identical pod counts:
- `normalizedCost = cost / (N * cost) = 1/N`
- `normalizedDisruption = disruption / (N * disruption) = 1/N`
- `decisionRatio = (1/N) / (1/N) = 1.0`

Every node gets ratio = 1.0 exactly. With default threshold = 1.0, the check
`decisionRatio >= threshold` passes (barely). But this means the threshold has
**no discriminating power** — it's either all-pass or all-fail for uniform fleets.

This explains the observed indirect influence: `cost-justified-5.00` (threshold=5.0)
blocks all consolidation (ratio 1.0 < 5.0 → skip), while `cost-justified-1.00`
(threshold=1.0) allows all consolidation (ratio 1.0 >= 1.0 → pass). The 16 vs 49
disruption difference comes from the threshold acting as a binary gate, not a
gradient.

## Why the Threshold Still Influences Behavior Indirectly

Despite CostJustified never activating at full scale, `decisionRatioThreshold`
affects behavior because:

1. Higher thresholds (e.g., 5.0) cause `shouldExecuteCommand` to reject commands
   in the single/multi-node consolidation path when it does run
2. This leaves more nodes for the Emptiness path to handle
3. The net effect: high threshold → fewer Underutilized disruptions, more Empty
   disruptions → lower total disruption count

This is a side effect of the threshold filtering, not intentional CostJustified
behavior.

## Required Changes

### 1. Emptiness should respect CostJustified policy (optional, design decision)

If the intent is for CostJustified to control ALL consolidation including empty
nodes, then `Emptiness.ShouldDisrupt` needs a policy check:

```go
func (e *Emptiness) ShouldDisrupt(_ context.Context, c *Candidate) bool {
    if c.NodePool.Spec.Disruption.ConsolidateAfter.Duration == nil {
        return false
    }
    // For CostJustified, let the consolidation methods handle empty nodes too
    if resolveConsolidationPolicy(c.NodePool) == v1.ConsolidateWhenCostJustifiesDisruption {
        return false
    }
    return len(c.reschedulablePods) == 0 &&
        c.NodeClaim.StatusConditions().Get(v1.ConditionTypeConsolidatable).IsTrue()
}
```

**Trade-off:** This would slow down empty node cleanup for CostJustified pools,
since empty nodes would wait for the consolidation methods instead of being
immediately claimed. Empty nodes have zero disruption cost, so the ratio would be
infinity (free consolidation) — they'd always pass. The delay is unnecessary work
for the same outcome.

**Recommendation:** Keep Emptiness behavior as-is. Empty node deletion is always
beneficial. The CostJustified path should only gate non-trivial consolidation
decisions.

### 2. Decision ratio needs heterogeneity to be useful

The formula `normalizedCost / normalizedDisruption` produces 1.0 for all nodes in
a uniform fleet. For the threshold to create a meaningful gradient, the formula
needs asymmetry between cost and disruption dimensions.

Options:
- **Use absolute costs** instead of pool-relative normalization (breaks
  cross-pool comparability)
- **Include replacement cost** in the ratio: `(nodeCost - replacementCost) /
  disruptionCost` (requires scheduling simulation before ratio computation)
- **Weight by utilization**: factor in actual resource usage vs capacity

### 3. The real test gap: non-empty scale-down

The KWOK test scales from 500→0 replicas, which empties all nodes. A more
realistic test would scale from 500→250 replicas, leaving nodes partially filled.
This would exercise the CostJustified path at scale with heterogeneous utilization.

## Conclusion

The CostJustified path implementation is **correct** — it activates when non-empty
candidates reach `computeConsolidation`. The problem is **environmental**: the
full-scale KWOK test creates conditions (all-empty nodes) where the Emptiness
method handles everything before CostJustified gets a chance. The indirect
threshold influence is a real but unintended binary gate effect from uniform
fleet topology, not the smooth gradient the design intended.
