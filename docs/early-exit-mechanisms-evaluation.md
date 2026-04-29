# Early-Exit Mechanisms for Cost-Justified Consolidation Controller

**Bead:** hq-gfpc
**Date:** 2026-04-29
**Status:** Evaluation / Review-only

## Context

The pod deletion cost controller ([PR #2894](https://github.com/kubernetes-sigs/karpenter/pull/2894), [RFC #2935](https://github.com/kubernetes-sigs/karpenter/pull/2935)) introduces a SHA-256 hash-based change detector to skip annotation updates when cluster state hasn't changed. This document evaluates that approach against existing Karpenter early-exit patterns and alternative mechanisms.

---

## 1. Current Hash-Based Approach (PR #2894)

### How It Works

`ChangeDetector` in `pkg/controllers/pod/deletioncost/changedetector.go` computes two SHA-256 hashes per reconcile cycle:

- **Node hash:** Sorted node names + creation timestamps + pod counts
- **Pod hash:** Sorted pod namespace/name + assigned node name pairs

If both hashes match the previous cycle, the entire ranking + annotation update is skipped.

### Cost of Data Collection

This is the core concern. To compute the hashes, the detector must:

1. Iterate all `StateNode` objects from `state.Cluster`
2. For **each node**, call `node.Pods(ctx, kubeClient)` — which lists pods from the API server cache filtered by node name
3. Build sorted string slices and hash them

The pod listing happens **twice** — once for `computeNodeHash` (to get pod counts) and once for `computePodHash` (to get pod assignments). For a cluster with N nodes and P pods, this is O(N) API cache reads for node hash + O(N) API cache reads for pod hash = **2N list operations per cycle**, even when nothing has changed.

This means the "optimization" to skip work still performs the most expensive part of the work (data collection) on every cycle. The hash comparison only saves the annotation writes, not the reads.

### Correctness

The hash inputs are well-chosen for detecting changes that matter to the ranking:
- Node additions/removals change the node hash
- Pod scheduling/termination changes both hashes
- Pod count changes on a node change the node hash

However, the hash **misses** drift status changes (`ConditionTypeDrifted`) and do-not-disrupt annotation changes, both of which affect the three-tier partitioning. A node transitioning from normal to drifted would not change the hash (same name, same timestamp, same pod count) but should trigger re-ranking because the node moves from Tier 2 to Tier 1.

### Complexity

The implementation is straightforward (~140 lines) but introduces a parallel state-tracking mechanism that doesn't integrate with any existing Karpenter change detection infrastructure.

---

## 2. Existing Early-Exit Patterns in Karpenter

### Pattern A: ConsolidationState Timestamp (disruption controller)

**Location:** `pkg/controllers/state/cluster.go` — `ConsolidationState()` / `MarkUnconsolidated()`

**Mechanism:** A monotonically increasing timestamp updated whenever cluster state changes meaningfully (node added/removed, pod scheduled/completed, node initialized, node marked for deletion). The disruption controller's consolidation methods (`Emptiness`, `SingleNodeConsolidation`, `MultiNodeConsolidation`) compare their `lastConsolidationState` against `cluster.ConsolidationState()`. If equal, they return immediately with zero work.

**Triggers that update the timestamp:**
- `UpdatePod` (pod binding changes)
- `DeletePod`
- `UpdateNode` (new node, node state changes)
- `DeleteNode`
- `UpdateNodeClaim`
- `DeleteNodeClaim`
- Node initialization state changes
- Node deletion marking changes
- Automatic 5-minute staleness reset

**Performance:** O(1) timestamp comparison. Zero data collection cost. The state cluster already tracks all changes as part of its normal operation — no additional API calls needed.

**What it catches that the hash misses:** Drift status changes, do-not-disrupt annotation changes, node initialization changes, NodeClaim deletion — all of which affect consolidation decisions.

### Pattern B: Batcher + Trigger (provisioning controller)

**Location:** `pkg/controllers/provisioning/batcher.go`

**Mechanism:** The provisioning controller doesn't poll. It waits on a channel that is triggered only when a provisionable pod or disrupted node is detected. A 1-second idle timeout + max batch window prevents both busy-looping and unbounded waiting.

**Performance:** Zero CPU while idle. Only wakes when there's actual work to do.

### Pattern C: Event Predicates (various controllers)

**Location:** Controller-runtime watch predicates

**Mechanism:** Controllers like `nodeclaim.podevents` use custom predicates that only enqueue reconciliation for specific state transitions (e.g., pod newly bound, pod terminal). The `nodeoverlay` controller uses `predicate.GenerationChangedPredicate{}` to only reconcile on spec changes.

**Performance:** Prevents reconcile invocations entirely for irrelevant events.

### Pattern D: go-cache TTL Rate Limiting (consistency, drift controllers)

**Location:** `pkg/controllers/nodeclaim/consistency/controller.go`, `pkg/controllers/nodeclaim/disruption/drift.go`

**Mechanism:** In-memory caches with TTL that rate-limit expensive operations. The consistency controller scans each NodeClaim at most once per 10 minutes. The drift controller caches instance-type-found checks for 30 minutes.

**Performance:** Amortizes expensive operations over time windows.

### Pattern E: DeepEqual Patch Guards (universal)

**Location:** Every controller that patches resources

**Mechanism:** `equality.Semantic.DeepEqual(stored, modified)` before issuing API server patches. Skips the write if nothing changed.

**Performance:** O(1) comparison, saves an API server round-trip per no-op.

---

## 3. Alternative Approaches

### Alternative 1: Piggyback on ConsolidationState (Recommended)

**Approach:** Replace the SHA-256 `ChangeDetector` with a simple timestamp comparison against `cluster.ConsolidationState()`, identical to how the disruption controller's consolidation methods work.

```go
type Controller struct {
    // ...
    lastConsolidationState time.Time
}

func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
    currentState := c.cluster.ConsolidationState()
    if currentState == c.lastConsolidationState {
        return reconciler.Result{RequeueAfter: reconcileInterval}, nil
    }
    // ... do work ...
    c.lastConsolidationState = currentState
    return reconciler.Result{RequeueAfter: reconcileInterval}, nil
}
```

| Criterion | Assessment |
|-----------|------------|
| **Data collection cost** | **Zero.** No API calls, no pod listing, no hashing. Single timestamp comparison. |
| **Correctness** | **Better than hash.** Catches drift status changes, do-not-disrupt changes, node initialization — all of which the hash misses. Includes the 5-minute staleness reset as a safety net. |
| **Staleness** | Same as disruption controller. Reacts within one reconcile interval (60s) of any cluster state change. 5-minute forced re-evaluation catches external changes. |
| **Complexity** | **Minimal.** One field, one comparison. Deletes ~140 lines of ChangeDetector code. |
| **Risk** | Low. This is the exact same mechanism that gates all consolidation decisions in Karpenter. If it's good enough for deciding whether to drain and delete nodes, it's good enough for deciding whether to update annotations. |

**Caveat:** ConsolidationState is updated on many events that don't affect deletion cost ranking (e.g., a pod completing on a node that's not in the top 50). This means the controller will occasionally re-rank when it doesn't strictly need to. But the ranking computation itself is cheap (sort N nodes, iterate pods on top 50) — the expensive part is the annotation writes, which are already guarded by the bounded labeling (top 50) and the DeepEqual patch guards in the annotation manager.

### Alternative 2: Event-Driven with Watch Predicates

**Approach:** Convert from a singleton timer-based controller to a watch-based controller that reconciles on specific events: pod bound/terminated, node added/removed, NodeClaim drift status changed.

| Criterion | Assessment |
|-----------|------------|
| **Data collection cost** | Zero for the trigger. Still needs to collect state for ranking. |
| **Correctness** | Excellent — reacts to exactly the events that matter. |
| **Staleness** | Near-zero — reacts as soon as the event is processed. |
| **Complexity** | **High.** Requires careful predicate design, debouncing (to avoid re-ranking on every single pod event during a scale-down), and changes the controller's fundamental architecture from singleton to per-event. |
| **Risk** | Medium. Event storms during large scale-downs could cause excessive re-ranking. Would need a batcher similar to the provisioning controller. |

This is architecturally cleaner but significantly more complex to implement correctly. Not recommended for the initial implementation.

### Alternative 3: ResourceVersion Comparison

**Approach:** Track the ResourceVersion of nodes and pods, skip if unchanged.

| Criterion | Assessment |
|-----------|------------|
| **Data collection cost** | Still requires listing resources to get their ResourceVersions. |
| **Correctness** | ResourceVersion changes on any field update, including status updates that don't affect ranking. Would cause many false positives. |
| **Staleness** | Good — reacts to any change. |
| **Complexity** | Medium. Need to track per-resource versions. |
| **Risk** | Karpenter deliberately avoids ResourceVersion in production controllers (only used in tests). Going against this convention would be surprising. |

Not recommended. ResourceVersion is too fine-grained (false positives) and goes against Karpenter conventions.

### Alternative 4: Generation-Based Change Detection

**Approach:** Use `metadata.generation` on NodePools and NodeClaims to detect spec changes.

| Criterion | Assessment |
|-----------|------------|
| **Data collection cost** | Low — generation is on the object metadata. |
| **Correctness** | **Poor for this use case.** Generation only increments on spec changes. Pod scheduling, pod termination, and node status changes (which are the primary triggers for re-ranking) don't change generation. |
| **Staleness** | Very high — would miss most relevant changes. |
| **Complexity** | Low. |
| **Risk** | Would miss the vast majority of changes that should trigger re-ranking. |

Not recommended. Generation tracks the wrong signal for this controller.

### Alternative 5: Hybrid — ConsolidationState + Drift Watch

**Approach:** Use ConsolidationState as the primary gate (Alternative 1), but also watch for `ConditionTypeDrifted` changes on NodeClaims to catch drift transitions that ConsolidationState already covers (since `UpdateNodeClaim` calls `MarkUnconsolidated`).

This is unnecessary — ConsolidationState already captures drift changes because `UpdateNodeClaim` triggers `MarkUnconsolidated()`. Listed for completeness.

---

## 4. Recommendation

**Use Alternative 1: Piggyback on ConsolidationState.**

Rationale:

1. **Zero data collection cost.** The current hash approach's main weakness is that it performs expensive pod listing on every cycle just to determine if it should skip. ConsolidationState eliminates this entirely.

2. **Strictly better correctness.** ConsolidationState captures drift transitions, do-not-disrupt changes, and node lifecycle events that the hash misses.

3. **Battle-tested.** This is the same mechanism that gates all consolidation decisions — the most critical and expensive operations in Karpenter. It has been in production across thousands of clusters.

4. **Minimal code.** Replaces ~140 lines of ChangeDetector + test code with a single timestamp field and comparison.

5. **Consistent with codebase conventions.** Every other controller that needs to skip work when the cluster is stable uses either ConsolidationState (disruption), event predicates (informers), or TTL caches (consistency). The SHA-256 hash approach is an outlier.

The only downside is occasional unnecessary re-ranking when ConsolidationState changes for reasons unrelated to deletion cost (e.g., a pod completing on a node outside the top 50). This is acceptable because:
- The ranking computation is O(N log N) sort + O(50 * pods_per_node) annotation checks — cheap
- Annotation writes are already guarded by DeepEqual (no API writes if values unchanged)
- The 60-second reconcile interval naturally rate-limits re-ranking regardless

### Secondary Recommendations

1. **Remove the `PodDeletionCostChangeDetection` CLI flag.** With ConsolidationState, change detection is always on and always cheap. There's no reason to make it optional.

2. **Fix the hash correctness gap.** If the hash approach is kept despite this recommendation, add drift status and do-not-disrupt annotation to the hash inputs.

3. **Consider event-driven architecture for v2.** If the controller proves valuable in production, converting to a watch-based controller with a batcher (Alternative 2) would provide the best reactivity. But this is a larger refactor that should be deferred.

---

## 5. Survey of All Early-Exit Patterns

For reference, here is the complete catalog of early-exit / no-op detection patterns found across all Karpenter controllers:

| Controller | Pattern | Mechanism | Cost |
|-----------|---------|-----------|------|
| Disruption (all methods) | Cluster sync gate | `cluster.Synced()` atomic bool | O(1) |
| Disruption (consolidation) | ConsolidationState | Timestamp comparison | O(1) |
| Disruption (all methods) | Zero candidates | Empty slice check | O(N) filter |
| Disruption (all methods) | NoOpDecision filter | Slice length check | O(1) |
| Disruption (queue) | Already-disrupting guard | Map lookup | O(1) |
| Disruption (consolidation) | Price-based filtering | Instance type price comparison | O(I) |
| Disruption (consolidation) | Spot-to-spot guard | Feature flag + count check | O(1) |
| Disruption (validation) | TTL re-validation | Sleep + re-evaluate | 15s delay |
| Provisioning | Batcher idle timeout | Channel wait | 1s block |
| Provisioning | Cluster sync gate | `cluster.Synced()` | O(1) after first sync |
| Provisioning | Empty pods check | Slice length | O(1) |
| Provisioning | IsProvisionable filter | Pod condition checks | O(1) per pod |
| Provisioning | ChangeMonitor | Hash + TTL cache | O(1) per check |
| State cluster | hasSynced fast path | Atomic bool | O(1) |
| State cluster | Unbound pod skip | NodeName == "" | O(1) |
| State cluster | Binding dedup | Map lookup | O(1) |
| State cluster | 5-min staleness reset | Time comparison | O(1) |
| NodeClaim lifecycle | DeepEqual patch guard | Semantic equality | O(fields) |
| NodeClaim lifecycle | Status condition state machine | Condition check | O(1) |
| NodeClaim drift | Instance type cache | go-cache 30min TTL | O(1) lookup |
| NodeClaim drift | Hash annotation comparison | String equality | O(1) |
| NodeClaim consistency | Scan rate limiter | go-cache 10min TTL | O(1) lookup |
| NodeClaim podevents | 10s dedup window | Timestamp comparison | O(1) |
| NodePool counter | Create-only predicate | Event filter | O(1) |
| NodePool reg health | ObservedGeneration | Generation comparison | O(1) |
| NodeOverlay | GenerationChangedPredicate | Event filter | O(1) |
| NodeOverlay | Atomic store swap | atomic.Pointer | O(1) read |
| Pod deletion cost | SHA-256 hash (current) | Hash comparison | **O(N * pods) — expensive** |
| Pod deletion cost | ConsolidationState (proposed) | Timestamp comparison | **O(1) — free** |
| All controllers | IsManaged predicate | CloudProvider check | O(1) |
| All controllers | DeletionTimestamp check | Timestamp nil check | O(1) |
| All controllers | Watch-level predicates | Event filtering | O(1) per event |
