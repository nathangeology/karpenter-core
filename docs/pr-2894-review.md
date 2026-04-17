# PR #2894 Review — Pod Deletion Cost Management Controller

**Date:** 2026-04-17
**PR:** https://github.com/kubernetes-sigs/karpenter/pull/2894
**RFC:** https://github.com/kubernetes-sigs/karpenter/pull/2935
**Branch:** `feat/pod-deletion-cost-management`
**Status:** OPEN, WIP, needs-ok-to-test

## Current State Assessment

The PR adds a feature-gated pod deletion cost management controller across 17 files (~2100 lines changed). The core implementation is solid and aligns well with the RFC. Recent refactoring has simplified the ranking engine to PodCount-only with three-tier drift partitioning, but several artifacts from the previous multi-strategy design remain, causing compilation failures in tests.

## What's Working

### Core Implementation (ranking.go) ✅
- **PodCount-only strategy** — `RankingEngine` is a zero-field struct with no strategy parameter. `RankNodes` sorts by pod count ascending within each tier. Matches RFC recommendation.
- **Three-tier drift partitioning** — `partitionNodes()` correctly separates drifted (ConditionTypeDrifted=True), normal, and do-not-disrupt nodes. Drifted get lowest cost, DND get highest. Matches RFC.
- **Dynamic base rank** — `baseRank = -len(nodes)`, so rank range is `[-n, -1]`. Matches RFC specification of `-n` base rank.
- **Deterministic tiebreak** — `sortByPodCount` breaks ties by node name for stable ordering.

### Annotation Manager (annotation.go) ✅
- Two-annotation protocol: `controller.kubernetes.io/pod-deletion-cost` + `karpenter.sh/managed-deletion-cost` sentinel.
- Customer-managed annotations correctly detected and skipped via `shouldUpdatePod()`.
- Graceful error handling: NotFound (pod deleted), Conflict (retry next cycle), other errors (log + continue).
- Metrics tracked per result category (success, skipped_customer_managed, error).

### Change Detector (changedetector.go) ✅
- SHA-256 hash of node names, creation timestamps, and pod counts.
- Separate node hash and pod hash for granular change detection.
- Correctly returns true on first call (empty state → any state = change).

### Controller (controller.go) ✅
- Feature gate check: `opts.FeatureGates.PodDeletionCostManagement`.
- Change detection toggle: `opts.PodDeletionCostChangeDetection`.
- Singleton reconciler pattern with 60-second interval.
- Proper event publishing for ranking completion, failures, and disabled state.

### Infrastructure ✅
- Feature gate added to `options.go` with parsing and defaults.
- Controller registered conditionally in `controllers.go`.
- RBAC updated: pods get `update` and `patch` verbs in kwok clusterrole.
- Event reason constants added to `pkg/events/reason.go`.
- Metrics: 5 metrics covering nodes ranked, pods updated, ranking duration, annotation duration, skipped-no-changes.

## What's Broken

### 1. Tests Won't Compile — CRITICAL

The test files reference symbols that don't exist in the current `ranking.go`:

| Symbol Referenced in Tests | Exists in ranking.go? | Issue |
|---|---|---|
| `deletioncost.RankingStrategyRandom` | ❌ | Constant removed during strategy simplification |
| `deletioncost.BaseRank` | ❌ | Not exported; computed locally as `-len(nodes)` |
| `deletioncost.NewRankingEngine(strategy)` | ❌ | `NewRankingEngine()` takes no parameters |

**Affected files:**
- `ranking_test.go` — All 6 test cases call `NewRankingEngine(RankingStrategyRandom)` and reference `BaseRank`
- `suite_test.go` — Sets `opts.PodDeletionCostRankingStrategy = "Random"` (field doesn't exist on Options)
- `controller_test.go` — Sets `opts.PodDeletionCostRankingStrategy = "Random"` in disabled-gate test

**Fix:** Update all test files to use `NewRankingEngine()` (no args), remove `RankingStrategyRandom` references, and either export `BaseRank` or compute expected values dynamically in tests.

### 2. Options Field Missing — CRITICAL

`suite_test.go` and `controller_test.go` reference `opts.PodDeletionCostRankingStrategy` which is not defined in `options.go`. The Options struct only has `PodDeletionCostChangeDetection`.

**Fix:** Remove `PodDeletionCostRankingStrategy` references from test setup, or add the field to Options if it's still needed (it shouldn't be, since ranking is PodCount-only now).

## What's Missing or Incomplete

### 3. README.md Describes 4 Strategies — MEDIUM

The README still documents Random, LargestToSmallest, SmallestToLargest, and UnallocatedVCPUPerPodCost strategies with configuration examples. This is stale after the PodCount simplification.

**Fix:** Rewrite README to describe PodCount as the only strategy. Remove strategy configuration section, multi-strategy examples, and `POD_DELETION_COST_RANKING_STRATEGY` env var references.

### 4. Third-Party Annotation Conflict Detection — DEFERRED (per RFC)

The RFC's "Risks and Mitigations" section describes tracking last-assigned values to detect when a third party modifies a Karpenter-managed annotation. The current implementation only checks sentinel presence, not value drift. If a third-party controller changes the `pod-deletion-cost` value on a sentinel-annotated pod, Karpenter will overwrite it on the next reconcile.

**Status:** Not implemented. Could be deferred to a follow-up PR since the sentinel protocol already prevents overwriting customer-set annotations (those without the sentinel). The gap is only for the case where a third party modifies a value that Karpenter previously set.

### 5. Bounded Node Labeling — DEFERRED (per RFC)

The RFC recommends limiting annotation updates to the top-N nodes per cycle and cleaning up orphaned annotations on nodes that fall out of the top-N. The current implementation processes all nodes every cycle.

**Status:** Not implemented. For small-to-medium clusters this is fine. For large clusters (500+ nodes), this could generate significant API write volume. Could be a follow-up optimization.

### 6. PR Description Outdated — MEDIUM

The current PR description still references 4 ranking strategies and doesn't mention three-tier drift partitioning or the PodCount-only design. It should be updated to reflect the current implementation.

## Recommended Next Steps

### Must-fix before merge:
1. **Fix test compilation** — Update all test files to match current `ranking.go` API (no strategy param, no exported BaseRank)
2. **Remove stale Options field references** — Clean up `PodDeletionCostRankingStrategy` from tests
3. **Update README.md** — Reflect PodCount-only strategy
4. **Update PR description** — Reflect current implementation state

### Can defer to follow-up PRs:
5. Third-party annotation value-drift detection
6. Bounded node labeling with orphan cleanup
7. Helm chart values.yaml cleanup (remove strategy env var example)

## Summary

The core algorithm is well-implemented and RFC-aligned: PodCount ranking, three-tier drift partitioning, dynamic base rank, change detection, customer annotation protection. The main issue is that a recent refactoring to simplify from 4 strategies to PodCount-only left the test files and README out of sync. The tests will not compile. Fixing the test/README/description mismatches is straightforward and should unblock the PR for review.
