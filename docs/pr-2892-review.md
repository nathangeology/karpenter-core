# PR #2892 Review: Pod Disruption Tracking for Perf Test Reporting

## Existing Disruption Metrics Inventory

| Metric Name | Type | Subsystem | Labels | Location |
|---|---|---|---|---|
| `karpenter_voluntary_disruption_decision_evaluation_duration_seconds` | Histogram | `voluntary_disruption` | reason, consolidation_type | `pkg/controllers/disruption/metrics.go` |
| `karpenter_voluntary_disruption_decisions_total` | Counter | `voluntary_disruption` | decision, reason, consolidation_type | `pkg/controllers/disruption/metrics.go` |
| `karpenter_voluntary_disruption_decisions_by_nodepool_total` | Counter | `voluntary_disruption` | nodepool, decision, reason, consolidation_type | `pkg/controllers/disruption/metrics.go` |
| `karpenter_voluntary_disruption_eligible_nodes` | Gauge | `voluntary_disruption` | reason | `pkg/controllers/disruption/metrics.go` |
| `karpenter_voluntary_disruption_consolidation_timeouts_total` | Counter | `voluntary_disruption` | consolidation_type | `pkg/controllers/disruption/metrics.go` |
| `karpenter_voluntary_disruption_failed_validations_total` | Counter | `voluntary_disruption` | consolidation_type | `pkg/controllers/disruption/metrics.go` |
| `karpenter_voluntary_disruption_queue_failures_total` | Counter | `voluntary_disruption` | decision, reason, consolidation_type | `pkg/controllers/disruption/metrics.go` |
| `karpenter_nodepools_allowed_disruptions` | Gauge | `nodepools` | nodepool, reason | `pkg/controllers/disruption/metrics.go` |
| `karpenter_nodepools_nodes_consuming_budgets` | Gauge | `nodepools` | nodepool, reason | `pkg/controllers/disruption/metrics.go` |
| `karpenter_nodeclaims_disrupted_total` | Counter | `nodeclaims` | reason, nodepool, capacity_type | `pkg/metrics/metrics.go` |
| `karpenter_nodeclaims_unhealthy_disrupted_total` | Counter | `nodeclaims` | condition, nodepool, capacity_type, image_id | `pkg/controllers/node/health/metrics.go` |
| `karpenter_pods_eviction_requests_total` | Counter | `pods` | code | `pkg/controllers/node/termination/terminator/metrics.go` |
| `karpenter_pods_drained_total` | Counter | `pods` | reason | `pkg/controllers/node/termination/terminator/metrics.go` |

## Duplication Analysis

**Does the new metric (`karpenter_voluntary_disruption_pods_disrupted_total`) duplicate existing metrics?**

**No — it fills a genuine gap.** The existing metrics track disruption at the *node/nodeclaim* level, not the *pod* level:

- `karpenter_nodeclaims_disrupted_total` counts how many nodeclaims were disrupted, but says nothing about how many pods were on those nodeclaims.
- `karpenter_pods_drained_total` counts pods drained during *termination* (downstream of the disruption decision), labeled only by drain reason — not by the disruption reason (consolidation, drift, etc.) or nodepool.
- `karpenter_pods_eviction_requests_total` counts eviction API calls, labeled only by HTTP response code.
- `karpenter_pods_state` is a point-in-time gauge of pod states, not a disruption event counter.

**The new metric uniquely provides:** a count of reschedulable pods affected by each disruption action, labeled by disruption reason and nodepool. This cannot be derived from existing metrics because:
1. `nodeclaims_disrupted_total` doesn't carry pod counts.
2. `pods_drained_total` doesn't carry disruption reason or nodepool labels.
3. There is no join key between the two that would allow computing "pods disrupted per consolidation action per nodepool" from existing metrics alone.

**Verdict: Not a duplicate. Adds genuinely new information.**

## PR Cleanliness Assessment

**Overall: Clean and focused. Ready for review.**

### Strengths
- **Minimal and additive**: 49 additions, 0 deletions across 4 files. No refactoring mixed in.
- **Single commit**: One well-structured conventional commit (`feat(disruption): Add pod disruption tracking to perf test reporting`).
- **Correct placement**: The new metric is emitted in `queue.go` right next to the existing `NodeClaimsDisruptedTotal.Inc()` call, inside the same `ParallelizeUntil` callback. This ensures the pod count is recorded at the same point in the disruption lifecycle.
- **Consistent style**: Uses the same `opmetrics.NewPrometheusCounter` pattern, same label constants (`metrics.ReasonLabel`, `metrics.NodePoolLabel`), same subsystem (`voluntary_disruption`).
- **Test integration is well-designed**: The baseline-delta pattern (`getPodsDisruptedCount(env)` before and after) correctly isolates per-test-phase disruption counts, avoiding cross-test contamination.

### Minor Observations
1. **`lo` dependency in test code**: The PR adds `github.com/samber/lo` to `test/suites/performance/report.go`. This is already used extensively throughout the codebase, so it's fine — not a new dependency.
2. **No unit tests for the metric itself**: The metric emission is a simple counter increment alongside an existing pattern. The perf test framework exercises it end-to-end. Acceptable for this scope.
3. **Draft status**: PR body says "Experimental/Draft Do-not-merge" — the author considers this not yet ready for merge. The code itself is clean, but the author's intent should be respected.

### Potential Suggestions
1. **Consider adding `capacity_type` label**: The adjacent `NodeClaimsDisruptedTotal` metric includes `capacity_type`. Adding it to `PodsDisruptedTotal` would enable "pods disrupted on spot vs on-demand" analysis. However, this is a scope expansion and could be a follow-up.
2. **Init zero values**: The existing `ConsolidationTimeoutsTotal` initializes zero values in `init()`. The new metric doesn't, which means it won't appear in Prometheus until the first disruption event. This is consistent with how `NodeClaimsDisruptedTotal` works (also no init), so it's fine.

## Summary

| Question | Answer |
|---|---|
| Does it duplicate existing metrics? | **No** — fills a gap between nodeclaim-level and pod-level disruption tracking |
| Is the change minimal and focused? | **Yes** — 49 additions, 0 deletions, 4 files, 1 commit |
| Are there unnecessary changes? | **No** |
| Is the commit message clear? | **Yes** — conventional commit format, descriptive body |
| Merge readiness | **Not yet** — author marked as draft/experimental. Code quality is merge-ready, but author intent should be confirmed. |
