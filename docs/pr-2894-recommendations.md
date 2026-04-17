# PR #2894 vs RFC #2935 — Merge Readiness Recommendations

## 1. Gap Analysis: RFC vs PR Implementation

| Aspect | RFC (#2935) Specifies | PR (#2894) Implements | Gap |
|--------|----------------------|----------------------|-----|
| **Ranking strategy** | PodCount only (rank by pod count ascending) | Four strategies: Random, LargestToSmallest, SmallestToLargest, UnallocatedVCPUPerPodCost | **MAJOR** — RFC explicitly says PodCount; PR ships four alternatives and defaults to Random |
| **Strategy configuration** | No strategy flag (single strategy) | `--pod-deletion-cost-ranking-strategy` CLI flag + `POD_DELETION_COST_RANKING_STRATEGY` env var | **MAJOR** — must be removed along with the strategies |
| **Three-tier partitioning** | Group A (drifted), Group B (normal), Group C (do-not-disrupt) | Two groups only: Group A (no do-not-disrupt) and Group B (do-not-disrupt). No drifted tier. | **MAJOR** — drifted nodes are not identified or prioritized |
| **Rank assignment** | Sequential starting at -n (n = total managed nodes) | Sequential starting at BaseRank = -1000 (hardcoded) | **MINOR** — RFC says -n for tighter range; PR uses fixed -1000 |
| **Third-party annotation conflict** | Two-annotation protocol (managed-by sentinel) | Implemented: checks `karpenter.sh/managed-deletion-cost` before updating | ✅ Matches RFC |
| **Third-party detection & yield** | Track last-assigned value; if current differs from last-set, remove sentinel and skip pod | Not implemented — only checks presence of sentinel annotation, not value drift | **MAJOR** — no external-modification detection |
| **Node labeling bound** | Limit nodes labeled per reconcile (e.g., top 50); clean up orphaned annotations on nodes no longer in top-N | Not implemented — processes all nodes every cycle | **MAJOR** — unbounded API writes per reconcile |
| **Orphan annotation cleanup** | Clean up annotations from previous cycles on nodes no longer in top-N | Not implemented | **MAJOR** — stale annotations left on pods |
| **Change detection** | SHA-256 hash of node/pod state; skip when unchanged | Implemented in `changedetector.go` | ✅ Matches RFC |
| **Reconcile interval** | 60 seconds | 60 seconds (`reconcileInterval = time.Minute`) | ✅ Matches |
| **Feature gate** | `PodDeletionCostManagement` | Implemented | ✅ Matches |
| **RBAC** | Add `update` and `patch` on pods | Added in `kwok/charts/templates/clusterrole.yaml` | ✅ Matches |
| **Events** | RankingCompleted, UpdateFailed, Disabled | Implemented in `events.go` | ✅ Matches |
| **Metrics** | nodes_ranked_total, pods_updated_total, ranking_duration_seconds, annotation_duration_seconds, skipped_no_changes_total | All implemented in `metrics.go` | ✅ Matches |
| **Tests** | PR claims 55+ test cases | No test files in the PR diff | **MAJOR** — zero tests shipped |

## 2. Specific Code Changes Needed for Each Concern

### Concern 1: Third-Party Annotation Modification Detection

**Problem:** The PR's `shouldUpdatePod()` only checks whether the sentinel annotation exists. If a third-party controller modifies the `pod-deletion-cost` value on a pod that has the sentinel, Karpenter will overwrite it on the next reconcile, fighting the third party.

**Required changes:**

- **`annotation.go`** — Add a `lastAssignedValues map[string]int` field to `AnnotationManager` (keyed by pod UID or namespace/name). Before updating a pod:
  1. Read the current `pod-deletion-cost` value
  2. Look up what Karpenter last set for this pod
  3. If the current value differs from what Karpenter last set AND the sentinel annotation is present → external modification detected
  4. Remove the `karpenter.sh/managed-deletion-cost` annotation from the pod
  5. Skip the pod going forward (it will now fail the `shouldUpdatePod` check)
  6. Log a warning event

- **`annotation.go`** — After successfully updating a pod, record the new value in `lastAssignedValues`.

- **`controller.go`** — The `lastAssignedValues` map should live on the `Controller` struct (survives across reconciles) and be passed to `AnnotationManager`, or `AnnotationManager` should be a long-lived struct on the controller (it already is).

**Files:** `annotation.go`, possibly `controller.go`
**Complexity:** Medium — straightforward map tracking, but needs careful handling of pod restarts (new UID = new entry).

### Concern 2: Bound the Labeling Work

**Problem:** The PR processes ALL Karpenter-managed nodes every reconcile cycle. For large clusters (500+ nodes), this means potentially thousands of pod annotation writes per cycle.

**Required changes:**

- **`controller.go`** — After collecting nodes and ranking them, take only the top N (e.g., 50) nodes for annotation updates. The rest should have their annotations cleaned up if they were previously annotated.

- **`annotation.go`** — Add a `CleanupOrphanedAnnotations` method that:
  1. Accepts the set of nodes NOT in the current top-N
  2. For each pod on those nodes that has the `karpenter.sh/managed-deletion-cost` sentinel:
     - Remove both `pod-deletion-cost` and `managed-deletion-cost` annotations
  3. Track which nodes were labeled in the previous cycle to know what to clean up

- **`controller.go`** — Add a `previouslyLabeledNodes` set to track which nodes were in the top-N last cycle. On each reconcile, compute the diff and clean up nodes that fell out.

- **`controller.go`** — Add a configurable constant or CLI flag for the max-nodes-per-cycle limit (default 50).

**Files:** `controller.go`, `annotation.go`
**Complexity:** Medium-High — the cleanup logic needs to handle edge cases (node deleted between cycles, pods rescheduled, etc.).

### Concern 3: Simplify to RFC-Recommended Strategy Only (PodCount)

**Problem:** The PR implements four ranking strategies. The RFC recommends PodCount only. The extra strategies add code surface, configuration complexity, and testing burden without RFC backing.

**Required changes:**

- **`ranking.go`** — Remove:
  - `RankingStrategyRandom`, `RankingStrategyLargestToSmallest`, `RankingStrategySmallestToLargest`, `RankingStrategyUnallocatedVCPUPerPodCost` constants
  - `rankNodesRandom()` function
  - `rankNodesBySize()` function
  - `calculateNormalizedCapacity()` function
  - `rankNodesByUnallocatedVCPUPerPod()` function
  - `calculateUnallocatedVCPUPerPod()` function
  - The `switch r.strategy` block — replace with a single `rankNodesByPodCount()` call

- **`ranking.go`** — Add `rankNodesByPodCount()`:
  ```go
  func rankNodesByPodCount(ctx context.Context, kubeClient client.Client, nodes []*state.StateNode) ([]*state.StateNode, error) {
      sorted := make([]*state.StateNode, len(nodes))
      copy(sorted, nodes)
      counts := make(map[string]int)
      for _, node := range sorted {
          pods, err := node.Pods(ctx, kubeClient)
          if err != nil { return nil, err }
          counts[node.Name()] = len(pods)
      }
      sort.SliceStable(sorted, func(i, j int) bool {
          ci, cj := counts[sorted[i].Name()], counts[sorted[j].Name()]
          if ci == cj { return sorted[i].Name() < sorted[j].Name() }
          return ci < cj // ascending: fewest pods = lowest cost = drained first
      })
      return sorted, nil
  }
  ```

- **`ranking.go`** — Remove `RankingEngine.strategy` field. The engine always uses PodCount. `NewRankingEngine` no longer takes a strategy parameter.

- **`controller.go`** — Remove `RankingStrategy(opts.PodDeletionCostRankingStrategy)` from `NewRankingEngine` call. Remove the lazy init pattern (just create in `NewController`).

- **`options.go`** — Remove:
  - `PodDeletionCostRankingStrategy` field from `Options`
  - The `--pod-deletion-cost-ranking-strategy` flag registration
  - The `POD_DELETION_COST_RANKING_STRATEGY` env var

- **`kwok/charts/values.yaml`** — Remove the `POD_DELETION_COST_RANKING_STRATEGY` example from the env block.

- **`metrics.go`** — Remove `strategyLabel` from `NodesRankedTotal` and `RankingDurationSeconds` (or keep it hardcoded to "PodCount").

- **`README.md`** — Rewrite to describe PodCount as the only strategy. Remove all strategy examples and configuration.

**Files:** `ranking.go`, `controller.go`, `options.go`, `kwok/charts/values.yaml`, `metrics.go`, `README.md`
**Complexity:** Medium — mostly deletion. The new `rankNodesByPodCount` is simple.

### Concern 4: Three-Tier Ranking (Drifted / Normal / Do-Not-Disrupt)

**Problem:** The PR partitions into two groups (normal vs do-not-disrupt). The RFC specifies three tiers: drifted (lowest cost) → normal (middle) → do-not-disrupt (highest cost).

**Required changes:**

- **`ranking.go`** — Replace `partitionNodesByDoNotDisrupt` with `partitionNodesThreeTier`:
  ```go
  func partitionNodesThreeTier(ctx context.Context, kubeClient client.Client, nodes []*state.StateNode) (
      drifted, normal, doNotDisrupt []*state.StateNode, err error) {
      for _, node := range nodes {
          hasDND, err := hasDoNotDisruptPods(ctx, kubeClient, node)
          if err != nil { return nil, nil, nil, err }
          if hasDND {
              doNotDisrupt = append(doNotDisrupt, node)
          } else if isDrifted(node) {
              drifted = append(drifted, node)
          } else {
              normal = append(normal, node)
          }
      }
      return
  }
  ```

- **`ranking.go`** — Add `isDrifted()` helper that checks `ConditionTypeDrifted` on the node's `StateNode`. Need to check how drift status is exposed — likely via `node.NodeClaim.StatusConditions` or similar. Look at existing disruption code for the pattern.

- **`ranking.go`** — Update `RankNodes` to:
  1. Call `partitionNodesThreeTier` instead of `partitionNodesByDoNotDisrupt`
  2. Rank each of the three groups by pod count ascending
  3. Assign sequential ranks: drifted first (lowest), then normal, then do-not-disrupt (highest)

- **`ranking.go`** — Update `NodeRank` struct to include a `Tier` field (or replace `HasDoNotDisrupt bool` with a tier enum).

**Files:** `ranking.go`
**Complexity:** Medium — need to find the right API for reading drift status from `StateNode`. The partitioning logic itself is straightforward.

## 3. Files to Modify, Add, or Remove

| Action | File | Reason |
|--------|------|--------|
| **Modify** | `pkg/controllers/pod/deletioncost/ranking.go` | Remove 4 strategies, add PodCount, add three-tier partitioning, add drift detection |
| **Modify** | `pkg/controllers/pod/deletioncost/annotation.go` | Add third-party detection (value tracking), add orphan cleanup method |
| **Modify** | `pkg/controllers/pod/deletioncost/controller.go` | Add node-count bound, remove strategy config, add cleanup orchestration, add tracking state |
| **Modify** | `pkg/controllers/pod/deletioncost/metrics.go` | Simplify strategy label (hardcode "PodCount" or remove) |
| **Modify** | `pkg/controllers/pod/deletioncost/README.md` | Rewrite for PodCount-only, three-tier, bounded updates |
| **Modify** | `pkg/operator/options/options.go` | Remove `PodDeletionCostRankingStrategy` field and flag |
| **Modify** | `kwok/charts/values.yaml` | Remove strategy env var example |
| **Add** | `pkg/controllers/pod/deletioncost/controller_test.go` | Unit tests (currently zero tests in PR) |
| **Add** | `pkg/controllers/pod/deletioncost/ranking_test.go` | Ranking unit tests |
| **Add** | `pkg/controllers/pod/deletioncost/annotation_test.go` | Annotation manager unit tests |
| **Keep** | `pkg/controllers/pod/deletioncost/changedetector.go` | Matches RFC, no changes needed |
| **Keep** | `pkg/controllers/pod/deletioncost/events.go` | Matches RFC, no changes needed |
| **Keep** | `pkg/controllers/controllers.go` | Feature gate wiring is correct |
| **Keep** | `pkg/events/reason.go` | Event reason constants are correct |
| **Keep** | `kwok/charts/templates/clusterrole.yaml` | RBAC changes are correct |

## 4. Estimated Complexity of Each Change

| Change | Complexity | Effort | Risk |
|--------|-----------|--------|------|
| Remove extra strategies, implement PodCount | Medium | 1-2 days | Low — mostly deletion + simple sort |
| Three-tier partitioning (drifted/normal/DND) | Medium | 1 day | Medium — need to find drift condition API |
| Third-party annotation conflict detection | Medium | 1-2 days | Medium — map lifecycle, pod UID handling |
| Bound node labeling + orphan cleanup | Medium-High | 2-3 days | High — cleanup edge cases, state tracking |
| Add unit tests | High | 3-5 days | Low — but large effort; PR has zero tests |
| Options/config cleanup | Low | 0.5 day | Low — straightforward removal |
| README/docs update | Low | 0.5 day | Low |

**Total estimated effort: 9-14 days**

## 5. Suggested Order of Implementation

1. **Simplify to PodCount strategy** (Concern 3) — Do this first because it removes ~200 lines of code and simplifies everything downstream. Touches: `ranking.go`, `controller.go`, `options.go`, `values.yaml`, `metrics.go`, `README.md`.

2. **Three-tier ranking** (Concern 4) — Build on the simplified ranking engine. Add drift detection and three-group partitioning. Touches: `ranking.go`.

3. **Third-party annotation conflict detection** (Concern 1) — Add value tracking to `AnnotationManager`. This is independent of the ranking changes. Touches: `annotation.go`, `controller.go`.

4. **Bound node labeling + orphan cleanup** (Concern 2) — This is the most complex change and benefits from having the other pieces stable first. Touches: `controller.go`, `annotation.go`.

5. **Add unit tests** — Write tests after the implementation stabilizes. Cover: three-tier partitioning, PodCount ranking, third-party detection/yield, bounded labeling, orphan cleanup, change detection integration.

6. **Final cleanup** — Update README, verify metrics labels, ensure Helm chart examples are accurate.

### Blocking Issues for Merge

The PR cannot merge until at minimum:
- **Concern 3** (strategy simplification) is addressed — the RFC is explicit about PodCount only
- **Concern 4** (three-tier ranking) is addressed — core RFC requirement
- **Tests are added** — the PR claims 55+ test cases but ships zero test files

Concerns 1 and 2 (third-party detection, bounded labeling) could potentially be deferred to a follow-up PR if the maintainers agree, since they are optimizations/safety features rather than core algorithm requirements. However, the RFC's "Risks and Mitigations" section explicitly calls out both, so deferral should be a conscious decision.
