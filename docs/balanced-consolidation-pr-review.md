# Balanced Consolidation PR #2893 — Re-Review After Fixes

Re-review of PR kubernetes-sigs/karpenter#2893 after blocker and should-fix issues
(kp-iiu) were addressed. Cross-referenced against the original review and RFC
(jamesmt-aws/karpenter#8).

## Assessment: **ready**

All 9 issues from the original review have been fixed. Tests pass. No new issues found.

---

## Fix Verification

### 1. ✅ BLOCKER FIXED: Negative eviction cost floor

**File:** `pkg/controllers/disruption/decisionratio.go`

Previously: pods with negative eviction costs were silently skipped, causing
`totalDisruption = 0` and auto-approving moves via `math.Inf(1)`.

Now: `cost += math.Abs(evictionCost) + 1.0` — every pod contributes a minimum
disruption weight of 1.0. This ensures negative-priority pods are cheap to disrupt
(low cost) but never free (zero cost). RFC-aligned.

**Test:** `TestNegativeEvictionCostFloor` verifies positive cost output.

### 2. ✅ BLOCKER FIXED: CEL validation for disruptionTolerance

**File:** `pkg/apis/v1/nodepool.go`

Added kubebuilder CEL rule:
```
rule: "self.consolidationPolicy == 'Balanced' || !has(self.disruptionTolerance)"
message: "disruptionTolerance is only valid with consolidationPolicy Balanced"
```

CRD files (`pkg/apis/crds/` and `kwok/charts/crds/`) are in sync. DeepCopy
generated correctly for the new `*int32` field.

**Test:** `TestDisruptionToleranceDefault` and `TestGetDisruptionToleranceThreshold`
cover the threshold computation.

### 3. ✅ SHOULD-FIX FIXED: Compatible offering price lookup

**File:** `pkg/controllers/disruption/decisionratio.go`

Previously: `candidate.instanceType.Offerings[0].Price` — wrong capacity type/zone.

Now: `candidate.instanceType.Offerings.Compatible(reqs).Cheapest().Price` — matches
the pattern used by `getCandidatePrices` in `consolidation.go`. Both functions now
use label-filtered compatible offerings.

### 4. ✅ SHOULD-FIX FIXED: Pool metrics scope documented

**File:** `pkg/controllers/disruption/decisionratio.go`

Added comment documenting the design choice:
> "This intentionally uses only eligible candidates (not the full pool) because
> eligible-candidate-scoped scoring is more useful for the threshold decision than
> full-pool scoring. The RFC is ambiguous here; this is a deliberate design choice."

### 5. ✅ SHOULD-FIX FIXED: Mixed-policy batching

**File:** `pkg/controllers/disruption/consolidation.go`

`checkBalancedScore` now filters to only Balanced candidates within the command:
```go
balancedCandidates := lo.Filter(cmd.Candidates, func(cn *Candidate, _ int) bool {
    return cn.NodePool.Spec.Disruption.ConsolidationPolicy == v1.ConsolidationPolicyBalanced
})
if len(balancedCandidates) == 0 {
    return cmd, true  // WhenEmptyOrUnderutilized pass unconditionally
}
```

Only Balanced candidates are scored; WhenEmptyOrUnderutilized candidates in mixed
batches pass unconditionally.

**Test:** `TestMixedPolicyBatching` verifies WhenEmptyOrUnderutilized passes.

### 6. ✅ SHOULD-FIX FIXED: Reason label consistency

**File:** `pkg/controllers/disruption/types.go`

Added `Command.Reason()` (delegates to `Method.Reason()`) and
`Command.ConsolidationPolicy()` (returns "Balanced" for log labels). The budget
system uses `Method.Reason()` = `Underutilized`, while human-readable labels use
`ConsolidationPolicy()`. This avoids the enum mismatch while keeping operator-facing
labels clear.

### 7. ✅ NICE-TO-HAVE FIXED: balanced_test.go included

**File:** `pkg/controllers/disruption/balanced_test.go` (202 lines)

Seven test functions covering:
- `TestBalancedConsolidationRouting` — `isBalancedPolicy` routing logic
- `TestBalancedScoreThreshold` — high vs low savings scoring
- `TestBalancedScoreApproval` — `checkBalancedScore` approval path
- `TestMixedPolicyBatching` — WhenEmptyOrUnderutilized passthrough
- `TestNegativeEvictionCostFloor` — disruption cost floor
- `TestDisruptionToleranceDefault` — nil tolerance defaults to k=2
- `TestGetDisruptionToleranceThreshold` — threshold = 1/k for k={1,2,4,10}

All tests pass: `ok sigs.k8s.io/karpenter/pkg/controllers/disruption 110.438s`

### 8. ✅ NICE-TO-HAVE FIXED: Histogram bucket at 0.4

**File:** `pkg/controllers/disruption/metrics.go`

Buckets: `{0, 0.1, 0.25, 0.4, 0.5, 0.75, 1.0, 1.5, 2.0, 3.0, 5.0, 10.0}`

The 0.4 bucket fills the gap between 0.25 and 0.5, capturing moves "just below"
the default threshold.

### 9. ✅ NICE-TO-HAVE FIXED: Duplicate log field removed

**File:** `pkg/controllers/disruption/consolidation.go:148-155`

Log line now has unique keys: `consolidation_score`, `threshold`, `decision`,
`candidates`, `deletedCost`, `replacementCost`. No duplicates.

---

## Additional Review: No New Issues Found

Checked for issues introduced by the fixes:

- **Replacement cost path** in `checkBalancedScore` uses `InstanceTypeOptions[0].Offerings.Cheapest().Price`
  for new node claims — consistent with existing pattern in `types.go:274`. Correct because
  the scheduler already selected compatible instance types for replacements.
- **`getCandidatePrices`** also uses compatible offerings with null-safety checks. Consistent.
- **CRD sync**: `pkg/apis/crds/` and `kwok/charts/crds/` are identical for the new fields.
- **Single-node consolidation** (`singlenodeconsolidation.go`): balanced score check runs
  before validation, `continue` on rejection. Correct.
- **Multi-node consolidation** (`multinodeconsolidation.go`): balanced score check runs
  after decision validation, passes `allCandidates` for pool metrics. Correct.
- **`ShouldDisrupt`**: allows both `WhenEmptyOrUnderutilized` and `Balanced` policies. Correct.

---

## RFC Alignment (Final)

| RFC Requirement | Status |
|----------------|--------|
| `score = savings_fraction / disruption_fraction` | ✅ |
| `savings_fraction = (deleted - replacement) / total_cost` | ✅ |
| `disruption_fraction = move_disruption / total_disruption` | ✅ (eligible-candidate scope, documented) |
| Per-pod disruption from `EvictionCost` with floor | ✅ |
| `threshold = 1/k`, default k=2 | ✅ |
| CEL validation: disruptionTolerance requires Balanced | ✅ |
| Mixed-policy: only Balanced candidates scored | ✅ |
| Zero disruption → approve if savings positive | ✅ |

---

## Summary

All 2 blockers, 4 should-fix, and 3 nice-to-have items from the original review are
resolved. Tests pass. No new issues introduced. PR is ready for merge.
