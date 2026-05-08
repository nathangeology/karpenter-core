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
| `karpenter.sh/last-assigned-deletion-cost` | Persists the last value Karpenter wrote for restart-safe conflict detection |

## Three-Annotation Protocol

The controller uses three annotations to safely manage pod deletion costs:

1. **`controller.kubernetes.io/pod-deletion-cost`** — The Kubernetes-standard annotation that the ReplicaSet controller reads for scale-down ordering.
2. **`karpenter.sh/managed-deletion-cost`** — A sentinel indicating Karpenter is actively managing this pod's deletion cost. Its presence distinguishes Karpenter-managed pods from customer-managed ones.
3. **`karpenter.sh/last-assigned-deletion-cost`** — Persists the exact value Karpenter last wrote to `pod-deletion-cost`. This enables third-party conflict detection to survive controller restarts without relying on in-memory state.

On each reconcile, conflict detection compares `pod-deletion-cost` against the last-assigned value. If they differ and the sentinel is set, a third party modified the value and Karpenter yields control.

## Customer Annotation Protection

Pods with an existing `pod-deletion-cost` annotation but **without** the Karpenter sentinel
are considered customer-managed and will not be modified.

## Third-Party Conflict Detection

If a third party modifies a Karpenter-managed pod's deletion cost annotation:
- Karpenter detects the value differs from what it last set (using in-memory cache or the persisted `last-assigned-deletion-cost` annotation after restart)
- Removes the sentinel and last-assigned annotations (releases management)
- Skips the pod on future reconciles
- Emits a `PodDeletionCostThirdPartyConflict` warning event

### Restart Race Condition

Without the `last-assigned-deletion-cost` annotation, Karpenter loses its expected-value state on restart. A third-party controller could modify `pod-deletion-cost` during the restart window and Karpenter would be unable to detect the change (the in-memory map starts empty). The persisted annotation makes conflict detection stateless — any Karpenter instance can detect third-party modifications by comparing the two annotation values on the pod itself.

## Bounded Node Labeling

Each reconcile cycle annotates at most **50 nodes**. When nodes drop out of the
top-50 set, their pod annotations are cleaned up automatically.

## Testing

```bash
go test ./pkg/controllers/pod/deletioncost/...
```

25 tests covering ranking, annotation management, change detection, third-party
conflict detection, bounded labeling, and controller reconciliation.
