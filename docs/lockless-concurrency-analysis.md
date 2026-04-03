# Karpenter Lockless Concurrency Analysis

## Overview

Karpenter's concurrency model is built around a central in-memory cluster state (`state.Cluster`) that is updated by informer-driven controllers and read by decision-making controllers (provisioning, disruption). Rather than using distributed locks or pessimistic locking against the Kubernetes API, Karpenter relies on:

1. **Fine-grained in-process mutexes** to protect shared state
2. **Snapshot-based decision making** via `DeepCopyNodes()`
3. **Optimistic concurrency** via Kubernetes resource versions for API writes
4. **Compute-then-validate patterns** to detect stale decisions
5. **`sync.Map`** for high-contention, independent key-value data

The system is designed so that stale reads cause safe no-ops (retry next loop) rather than unsafe actions.

---

## Pattern Catalog

### 1. Dual-Mutex Cluster State

**Where:** `pkg/controllers/state/cluster.go:63-88`

**What:** `state.Cluster` uses two separate `sync.RWMutex` instances plus multiple `sync.Map` fields:

```go
mu                   sync.RWMutex    // protects nodes, bindings, nodeNameToProviderID, etc.
clusterStateMu       sync.RWMutex    // protects clusterState timestamp (consolidation state)
unsyncedTimeMu       sync.Mutex      // protects unsynced timing fields
```

The `mu` lock guards the core node map and all derived maps (bindings, name-to-providerID lookups, nodePoolResources). The `clusterStateMu` is deliberately separate because `MarkUnconsolidated()` is called from within methods that already hold `mu` (e.g., `DeletePod` at line 516, `cleanupNodeClaim` at line 685).

**Best practice:** The separate mutex prevents deadlocks when state mutations need to signal consolidation changes. Every method that modifies node state calls `MarkUnconsolidated()` to advance the consolidation timestamp.

**Pitfall:** If a contributor adds a new method that holds `mu` and also tries to acquire `clusterStateMu` in a write path not through `MarkUnconsolidated()`, they could introduce unexpected lock ordering. Always use `MarkUnconsolidated()` rather than touching `clusterStateMu` directly.

### 2. sync.Map for Independent Key-Value Tracking

**Where:** `pkg/controllers/state/cluster.go:74-84`

**What:** Pod-level tracking uses `sync.Map` for fields like:
- `podAcks` — when Karpenter first saw a pod as pending
- `podsSchedulingAttempted` — when scheduling was attempted
- `podsSchedulableTimes` — when a pod was first marked schedulable
- `podToNodeClaim` — pod-to-nodeclaim mapping
- `daemonSetPods` — cached daemonset pod templates
- `antiAffinityPods` — pods with required anti-affinity

**Why it works:** Each key (pod NamespacedName or DaemonSet key) is independent. Operations on one pod never need to atomically coordinate with operations on another pod. `sync.Map` avoids holding the main `mu` lock for these high-frequency, low-contention operations.

**Pitfall:** `sync.Map` provides no way to atomically read-modify-write across multiple keys. The `ClearPodSchedulingMappings()` method (line 526) deletes from five separate `sync.Map` fields — if a concurrent reader checks some but not all of these maps between deletions, it may see an inconsistent snapshot. In practice this is safe because each map is consumed independently, but contributors should not add cross-map invariants.

### 3. Snapshot-Based Decision Making (DeepCopyNodes)

**Where:** `pkg/controllers/state/cluster.go:249-256`, consumed in `pkg/controllers/disruption/helpers.go:57-60` and `pkg/controllers/provisioning/provisioner.go:280`

**What:** Both the provisioner and disruption controller take a deep copy of all nodes at the start of their decision loop:

```go
// provisioner.go:280 — Schedule()
nodes := p.cluster.DeepCopyNodes()

// helpers.go:57 — SimulateScheduling()
nodes := cluster.DeepCopyNodes()
```

The deep copy holds `mu.RLock()` for the duration of the copy, then releases it. All subsequent scheduling simulation operates on the snapshot, not live state.

**Why it works:** Decisions are made against a consistent point-in-time view. If the cluster changes during simulation, the decision is validated before execution (see Pattern 5). This avoids holding locks during expensive scheduling computations.

**Known issue:** `DeepCopyNodes()` copies every node in the cluster. For large clusters (1000+ nodes), this is expensive. The comment at line 252 acknowledges this: "NOTE: This is very inefficient so this should only be used when DeepCopying is absolutely necessary."

**Best practice:** Use `Nodes()` (line 235) for read-only iteration that doesn't need mutation safety. Use `DeepCopyNodes()` only when the caller will modify the returned nodes or needs a snapshot that outlives the lock.

### 4. Singleton Controllers vs. Parallel Reconcilers

**Where:**
- Disruption controller: `pkg/controllers/disruption/controller.go:113-116` (singleton)
- Disruption queue: `pkg/controllers/disruption/queue.go:113-121` (parallel, up to 1000 concurrent)
- Provisioner: `pkg/controllers/provisioning/provisioner.go:108-112` (singleton)
- State informers: `pkg/controllers/state/informer/pod.go:82-87` (parallel, up to 3000 concurrent)

**What:** The disruption evaluation loop and provisioner are singletons — only one goroutine runs `Reconcile()` at a time. This is critical because their decision logic (candidate selection, scheduling simulation, validation) is inherently sequential and assumes no concurrent mutations to the decision state.

The disruption queue, however, runs many parallel reconcilers to process commands (wait for replacements, delete candidates). State informers also run in parallel to keep cluster state fresh.

**Why it works:** The singleton pattern for decision-making eliminates the need for complex distributed coordination. Only one disruption decision is being computed at any time. The queue handles parallel execution of already-committed decisions.

**Pitfall:** The singleton provisioner means provisioning throughput is bounded by a single goroutine's scheduling simulation speed. The `Batcher` (see Pattern 6) mitigates this by accumulating pods into batches.

### 5. Compute-Then-Validate (Consolidation)

**Where:** `pkg/controllers/disruption/validation.go:195-215` (ConsolidationValidator.isValid), `pkg/controllers/disruption/consolidation.go:42` (consolidationTTL = 15s)

**What:** Consolidation follows a three-phase pattern:

1. **Compute:** Take a snapshot, find candidates, simulate scheduling, produce a `Command`
2. **Wait:** Sleep for `consolidationTTL` (15 seconds) to let the cluster settle
3. **Validate:** Re-fetch candidates, re-check budgets, re-simulate scheduling, verify the command is still valid

The validation in `isValid()` (line 195) performs candidate validation, command validation, then a second candidate validation:

```go
func (c *ConsolidationValidator) isValid(ctx context.Context, cmd Command, validationPeriod time.Duration) error {
    // ... wait for validationPeriod ...
    validatedCandidates, err := c.validateCandidates(ctx, cmd.Candidates...)
    if err != nil { return err }
    if err := c.validateCommand(ctx, cmd, validatedCandidates); err != nil { return err }
    // Revalidate candidates after validating the command (mitigates race in #1167)
    if _, err = c.validateCandidates(ctx, validatedCandidates...); err != nil { return err }
    return nil
}
```

**Why it works:** The 15-second TTL gives time for in-flight changes (pod scheduling, node registration) to propagate through informer caches into cluster state. The double-validation of candidates (before and after command validation) mitigates the race condition documented in [kubernetes-sigs/karpenter#1167](https://github.com/kubernetes-sigs/karpenter/issues/1167) where a pod could be nominated to a candidate between the scheduling simulation and the candidate check.

**Known race window:** Between the final `validateCandidates` call and the actual `StartCommand` execution in the queue, the cluster can still change. This is mitigated by:
- Tainting candidates with `karpenter.sh/disruption` NoSchedule before launching replacements
- Checking `queue.HasAny()` to prevent double-disruption
- The provisioner's `NominateNodeForPod` mechanism

**Pitfall:** The validation re-runs `GetCandidates()` which does a fresh `DeepCopyNodes()`. If the cluster is under heavy churn, validation may repeatedly fail, causing consolidation to make no progress. This is safe (no incorrect actions) but can delay cost optimization.

### 6. Pod Batching (Batcher)

**Where:** `pkg/controllers/provisioning/batcher.go`

**What:** The `Batcher` accumulates pod trigger events into time-windowed batches using idle and max duration timers. It uses a channel for signaling and `sync.RWMutex` + `sets.Set` for deduplication:

```go
type Batcher[T comparable] struct {
    trigger chan struct{}       // signaling channel (capacity 1)
    mu      sync.RWMutex
    elems   sets.Set[T]        // deduplication set
}
```

`Trigger()` is idempotent per element — if the same pod UID triggers twice, the second call is a no-op (checked via `elems.Has()`). The channel send uses a non-blocking select to arm the trigger exactly once.

**Why it works:** The provisioner singleton blocks on `batcher.Wait()`. When pods arrive, the batcher starts an idle timer. If more pods arrive within the idle window, the timer resets. The batch closes when either the idle timer or max duration timer fires. This naturally groups related pods (e.g., a Deployment scale-up) into a single scheduling pass.

**Best practice:** The deduplication prevents the same pod from re-triggering a batch that's already considering it. The `Wait()` method clears the element set at the end, so the next batch starts fresh.

### 7. Optimistic State Tracking (MarkForDeletion)

**Where:** `pkg/controllers/disruption/queue.go:296-310` (StartCommand), `pkg/controllers/state/cluster.go:298-312` (MarkForDeletion)

**What:** When the disruption queue commits to a command, it:
1. Taints candidate nodes (API write with conflict retry)
2. Creates replacement NodeClaims
3. Calls `cluster.MarkForDeletion()` to update in-memory state
4. Manually calls `cluster.UpdateNodeClaim()` for new NodeClaims

The ordering is critical (documented in queue.go:296):
```
// IMPORTANT: We must MarkForDeletion AFTER we launch the replacements
// The reason is to avoid producing double-launches
```

If `MarkForDeletion` happened before replacement creation, the provisioner could see the terminating pods as needing new capacity and launch duplicates.

**Why it works:** The in-memory state update (`MarkForDeletion`) is immediately visible to the provisioner's next `DeepCopyNodes()` call. The provisioner filters out `MarkedForDeletion` nodes from its active set (provisioner.go:286). This prevents double-provisioning without requiring cross-controller locks.

Similarly, `cluster.UpdateNodeClaim()` is called manually after creating a NodeClaim (provisioner.go:340) to avoid races with the informer cache:
```go
// Update the nodeclaim manually in state to avoid eventual consistency delay
// races with our watcher.
p.cluster.UpdateNodeClaim(nodeClaim)
```

**Pitfall:** If Karpenter crashes between creating replacements and marking candidates for deletion, the candidates won't be marked. On restart, the disruption controller cleans up stale `karpenter.sh/disruption` taints from nodes not in the queue (controller.go:139-147).

### 8. Consolidation State Monotonic Clock

**Where:** `pkg/controllers/state/cluster.go:537-563`

**What:** `ConsolidationState()` returns a monotonically increasing timestamp that changes whenever something in the cluster might make consolidation possible. The `IsConsolidated()` check in consolidation methods compares the last-seen timestamp against the current one:

```go
func (c *consolidation) IsConsolidated() bool {
    return c.lastConsolidationState.Equal(c.cluster.ConsolidationState())
}
```

If nothing has changed, consolidation methods short-circuit and return empty commands. The timestamp auto-advances every 5 minutes (line 560) to force periodic re-evaluation.

**Why it works:** This is a lightweight change-detection mechanism. Rather than diffing the entire cluster state, a single timestamp comparison tells the consolidation controller whether it's worth re-evaluating. Every state mutation (node add/remove, pod bind/unbind, initialization change) calls `MarkUnconsolidated()`.

**Pitfall:** External changes (e.g., new instance type availability, spot price changes) don't trigger `MarkUnconsolidated()`. The 5-minute auto-advance handles this, but there's a window where consolidation opportunities from external changes are delayed.

### 9. NodePoolState Separate Locking

**Where:** `pkg/controllers/state/statenodepool.go`

**What:** `NodePoolState` has its own `sync.RWMutex` separate from `Cluster.mu`. It tracks per-NodePool node counts (active, deleting, pending disruption) and reservation limits using `atomic.Int64`.

The `ReserveNodeCount` method (line 131) uses a CAS (Compare-And-Swap) loop:
```go
for {
    currentlyReserved := n.nodePoolNameToNodePoolLimit[np].Load()
    remainingLimit := limit - int64(active+deleting+pendingdisruption) - currentlyReserved
    // ...
    if n.nodePoolNameToNodePoolLimit[np].CompareAndSwap(currentlyReserved, currentlyReserved+grantedLimit) {
        return grantedLimit
    }
}
```

**Why it works:** The CAS loop allows concurrent reservation attempts without holding the mutex during the entire operation. The mutex is held to read the node counts, but the atomic CAS handles the actual reservation race-free.

**Pitfall:** The mutex is held during `ReserveNodeCount` (line 131 acquires `mu.Lock()`), so the CAS loop is actually redundant with the mutex — no other goroutine can modify the counts while the lock is held. The CAS pattern would only be necessary if the lock were released between reading counts and updating the reservation. This is defensive coding rather than a bug.

### 10. Informer Cache Consistency

**Where:** `pkg/controllers/state/informer/*.go`, `pkg/controllers/state/cluster.go:118-210` (Synced)

**What:** State informer controllers (Pod, Node, NodeClaim, DaemonSet, NodePool) run as parallel controller-runtime reconcilers. Each reconciler fetches the latest object from the API server and calls the corresponding `Cluster.Update*()` method. These methods acquire `mu.Lock()` and update the in-memory state.

The `Synced()` method (line 118) validates that the in-memory state matches the API server by listing all NodeClaims and Nodes and comparing them against the internal maps. Both the provisioner and disruption controller gate on `Synced()` before making decisions:

```go
if !c.cluster.Synced(ctx) {
    return reconciler.Result{RequeueAfter: time.Second}, nil
}
```

**Why it works:** The sync check prevents decisions based on incomplete state during startup or after cache invalidation. The informers use `RequeueAfter: stateRetryPeriod` (1 minute) to periodically re-sync even without watch events.

**Known issue:** There's an inherent race between informer cache updates and decision-making. The provisioner addresses this by collecting nodes *before* pods (provisioner.go:274-279):
```go
// We collect the nodes with their used capacities before we get the list
// of pending pods. This ensures that the node capacities we schedule against
// are always >= what the actual capacity is at any given instance.
```

This ordering ensures over-estimation of used capacity (safe: may under-provision) rather than under-estimation (unsafe: may over-provision).

---

## Known Issues and Proposed Fixes

### 1. DeepCopyNodes Scalability

**Problem:** Every disruption evaluation and provisioning loop deep-copies all nodes. At 1000+ nodes, this creates significant GC pressure and latency.

**Proposed fix:** Implement a copy-on-write or generation-based snapshot mechanism. Nodes that haven't changed since the last snapshot could share references. The `triggerConsolidationOnChange` method already tracks what changed — this could be extended to maintain incremental snapshots.

### 2. Consolidation Starvation Under Churn

**Problem:** If the cluster is under constant churn (pods scheduling/terminating), the compute-then-validate pattern may never succeed because validation always finds the state has changed.

**Current mitigation:** The 5-minute auto-advance of consolidation state and the `IsConsolidated()` short-circuit prevent infinite retries. Single-node consolidation has a 3-minute timeout; multi-node has a 1-minute timeout.

**Proposed fix:** Consider a "good enough" validation mode that tolerates minor state changes (e.g., pod count changed by <5%) rather than requiring exact match.

### 3. Gap Between Validation and Execution

**Problem:** After validation succeeds, `StartCommand` taints nodes, creates replacements, and marks for deletion. Between validation and taint application, a new pod could schedule to the candidate node.

**Current mitigation:** The taint is applied first (before replacements), and the provisioner's `NominateNodeForPod` mechanism tracks which nodes have pending pods. Validation checks `IsNodeNominated()`.

**Proposed fix:** This is largely mitigated. The remaining window is small (milliseconds) and the consequence is a validation failure on the next attempt, not data loss.

### 4. sync.Map Memory Growth

**Problem:** Pod tracking maps (`podAcks`, `podsSchedulableTimes`, etc.) use `sync.Map` which doesn't shrink. If many pods cycle through the system, these maps accumulate entries.

**Current mitigation:** `ClearPodSchedulingMappings()` is called on pod deletion. `Reset()` clears everything (used in tests).

**Proposed fix:** The current cleanup is adequate for production use. The `sync.Map` entries are small (NamespacedName → time.Time) and are cleaned up on pod deletion.

---

## Guidelines for Contributors

### Adding New Cluster State Fields

1. **Choose the right synchronization primitive:**
   - Use fields under `mu` if the data must be consistent with the node map
   - Use `sync.Map` if keys are independent and don't need cross-key atomicity
   - Use a separate mutex only if the field is accessed from within `mu`-holding code paths

2. **Always call `MarkUnconsolidated()`** when modifying state that could affect consolidation decisions.

3. **Never hold `mu` while making API calls.** Take a snapshot, release the lock, then make the call.

### Adding New Controllers

1. **Decision-making controllers should be singletons** unless they can tolerate concurrent decisions on the same resources.

2. **Gate on `cluster.Synced()`** before making scheduling or disruption decisions.

3. **Use `DeepCopyNodes()` for mutation-safe snapshots.** Use `Nodes()` for read-only iteration.

### Modifying Disruption Logic

1. **Respect the compute-then-validate pattern.** Never skip validation for consolidation commands.

2. **Order matters in StartCommand:**
   - Taint candidates first (prevent new scheduling)
   - Create replacements second (ensure capacity exists)
   - MarkForDeletion third (update in-memory state)

3. **Manually update cluster state after API writes** (e.g., `cluster.UpdateNodeClaim()` after creating a NodeClaim) to avoid informer cache lag.

### Lock Ordering

If you must acquire multiple locks, follow this order to prevent deadlocks:
1. `Cluster.mu`
2. `Cluster.clusterStateMu` (only via `MarkUnconsolidated()`)
3. `Cluster.unsyncedTimeMu`
4. `NodePoolState.mu`

Never acquire `mu` while holding `clusterStateMu`. The code is structured so that `clusterStateMu` is only acquired from within `mu`-holding methods via `MarkUnconsolidated()`, which acquires `clusterStateMu` independently.
