# Pod Deletion Cost Controller

Automatically manages the `controller.kubernetes.io/pod-deletion-cost` annotation on pods
to influence Kubernetes' pod selection during scale-in events.

## Overview

When enabled via the `PodDeletionCostManagement` feature gate, this controller:

1. Ranks Karpenter-managed nodes using **PodCount** strategy with three-tier drift partitioning
2. Assigns deletion cost annotations to pods based on their node's rank
3. Protects customer-set annotations from being overwritten
4. Detects third-party annotation conflicts and releases management
5. Bounds annotation updates to the top 50 nodes per cycle with cleanup

## Ranking: Three-Tier Drift Partitioning

Nodes are partitioned into three tiers, each sorted by pod count ascending:

| Tier | Nodes | Deletion Cost |
|------|-------|---------------|
| 1 (lowest) | Drifted nodes | Deleted first |
| 2 (middle) | Normal nodes | Deleted second |
| 3 (highest) | Do-not-disrupt nodes | Deleted last |

Ranks start at `-len(nodes)` and increment sequentially across tiers.

## Components

- **RankingEngine** (`ranking.go`) — PodCount-based ranking with three-tier partitioning
- **AnnotationManager** (`annotation.go`) — Safe pod annotation updates with third-party conflict detection
- **ChangeDetector** — Uses `ConsolidationState` timestamp from `state.Cluster` to skip ranking when cluster state hasn't changed (O(1) comparison, zero API calls)
- **Controller** (`controller.go`) — Orchestrates ranking, bounded labeling, and cleanup

## Configuration

### Feature Gate
```
--feature-gates=PodDeletionCostManagement=true
```

## Annotations

| Annotation | Purpose |
|-----------|---------|
| `controller.kubernetes.io/pod-deletion-cost` | Kubernetes deletion priority (lower = deleted first) |
| `karpenter.sh/managed-deletion-cost` | Sentinel: Karpenter manages this pod's cost |

## Customer Annotation Protection

Pods with an existing `pod-deletion-cost` annotation but **without** the Karpenter sentinel
are considered customer-managed and will not be modified.

## Third-Party Conflict Detection

If a third party modifies a Karpenter-managed pod's deletion cost annotation:
- Karpenter detects the value differs from what it last set
- Removes the sentinel annotation (releases management)
- Skips the pod on future reconciles
- Emits a `PodDeletionCostThirdPartyConflict` warning event

## Failure Handling

Two rules, by scope.

**Cycle-wide faults return.** `RankNodes` returns its error. The controller logs it, emits
`PodDeletionCostDisabled`, and requeues. Without a ranking there is nothing to annotate, so the
cycle has no useful work left.

**Per-pod faults report and continue.** `UpdatePodDeletionCosts` returns nothing. A pod whose
write fails is logged, counted into `pods_updated_total{result="error"}`, evented as
`PodDeletionCostUpdateFailed` against that pod, and skipped. The remaining pods are still
annotated. One unwritable pod does not cost the other 49 nodes their annotations, and the next
reconcile retries it a minute later.

| Fault | Log | Event | Metric | Aborts cycle |
|---|---|---|---|---|
| `RankNodes` failure | `Error` | `PodDeletionCostDisabled` | — | yes |
| Pod write failure | `Error` | `PodDeletionCostUpdateFailed` (pod-scoped) | `pods_updated_total{result="error"}` | no |
| Pod write conflict | `V(1)` | — | `pods_updated_total{result="error"}` | no |
| Third-party conflict | — | `PodDeletionCostThirdPartyConflict` | `pods_updated_total{result="skipped_third_party_conflict"}` | no |
| Sentinel removal failure | `Error` | — | `pods_updated_total{result="error"}` | no |
| Pod gone (`NotFound`) | `V(1)` | — | `pods_updated_total{result="skipped_pod_deleted"}` | no |
| Node pod-list failure | `Error` | — | `nodes_failed_total{reason="pod_list_failed"}` | no |

The last row is a second guard on a fault the first row already covers. `partitionNodes` calls
`node.Pods()` for every node before ranking completes, so a node whose pod list cannot be read has
already failed the cycle through `RankNodes`. The guard in `UpdatePodDeletionCosts` catches only the
transient case where the same list succeeds during ranking and fails moments later.

## Metrics

Node-scoped and pod-scoped outcomes are counted on separate metrics. `pods_updated_total` takes
exactly one increment per pod. A node whose pod list cannot be read increments
`nodes_failed_total` instead, because the pods lost to that fault cannot be counted: listing them
is what failed. Folding it into the pod counter would report `1` for a node that cost 50 pods their
annotations.

| Metric | Label | Meaning |
|---|---|---|
| `pods_updated_total` | `result="success"` | Annotation written |
| | `result="skipped_customer_managed"` | Cost annotation without the sentinel: customer opt-out, or a released third-party conflict on later cycles |
| | `result="skipped_third_party_conflict"` | A third party overwrote a Karpenter-managed value; management released |
| | `result="skipped_pod_deleted"` | Pod deleted between the list and the write |
| | `result="error"` | Write failed, conflicted, or the sentinel could not be removed |
| `nodes_failed_total` | `reason="pod_list_failed"` | Node's pod list unreadable; every pod on it un-annotated |
| `nodes_ranked_total` | — | Nodes ranked |
| `skipped_no_changes_total` | — | Reconciles skipped on unchanged cluster state |

`skipped_customer_managed` counts every pod carrying a cost annotation without the sentinel.
Customer opt-out is the main case and not the only one. Releasing management on a third-party
conflict deletes the sentinel and leaves the third party's cost in place, so from the next cycle on
that pod is cost-without-sentinel and `shouldUpdatePod` cannot tell it apart from a customer-set
annotation. A conflicted pod therefore counts `skipped_third_party_conflict` on the cycle it is
released and `skipped_customer_managed` on every cycle after.

Two consequences for anyone reading these counters. On a cluster with conflicts,
`skipped_customer_managed` is an upper bound on opt-out, not a measure of it. And
`skipped_third_party_conflict` is the rate at which takeover happens, not a running count of
conflicted pods.

A pod deleted mid-cycle is in neither counter. It carries its own label.

Every series is written on every pass, including with zero, so a result that has not happened yet
scrapes as `0` rather than being absent and making `rate()` return no data.

Errors are not returned for reporting alone. An error that the caller can only log and swallow is
better raised where the failing object is still in scope, which is why the pod-scoped event carries
the pod and a cycle-wide `DisabledEvent` does not.

Requeues stay on the fixed `reconcileInterval`. Returning a non-nil error alongside
`reconciler.Result{RequeueAfter: ...}` makes controller-runtime race its exponential backoff
against the explicit interval on a first-wins basis, so the cadence stops being predictable.

## Consolidation Priority Migration

With this controller auto-managing `pod-deletion-cost` for RS coordination, users who
previously set `pod-deletion-cost` to steer consolidation behavior should migrate to:

```yaml
annotations:
  karpenter.sh/consolidation-priority: "2147483647"  # high = expensive to disrupt
```

The consolidation scoring path (`EvictionCost`) applies this precedence:
1. `karpenter.sh/consolidation-priority` — if set, used directly
2. `controller.kubernetes.io/pod-deletion-cost` — used only if NOT auto-managed (no sentinel)
3. Default cost of 1.0

Auto-managed pods (those with `karpenter.sh/managed-deletion-cost: "true"`) have their
`pod-deletion-cost` ignored for consolidation scoring since it reflects RS coordination
ranking, not user intent about disruption cost.

## Bounded Node Labeling

Each reconcile cycle annotates at most **50 nodes**. When nodes drop out of the
top-50 set, their pod annotations are cleaned up automatically.

## Testing

```bash
go test ./pkg/controllers/pod/deletioncost/...
```

29 specs covering ranking, annotation management, change detection, third-party
conflict detection, bounded labeling, metric result labels, and controller reconciliation.
