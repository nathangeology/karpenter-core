# Consolidation Scaling Analysis

Analysis of [kubernetes-sigs/karpenter#2972](https://github.com/kubernetes-sigs/karpenter/issues/2972):
high CPU usage during node consolidation at scale (500 nodes, ~3 vCPUs sustained).

## 1. Algorithmic Complexity of Single-Node Consolidation

### Call Chain

```
SingleNodeConsolidation.ComputeCommands()
  for each candidate (N candidates):
    computeConsolidation(candidate)
      SimulateScheduling(ctx, kubeClient, cluster, provisioner, candidate)
        cluster.DeepCopyNodes()                          // O(N) — copies all N state nodes
        provisioner.NewScheduler(ctx, pods, stateNodes)
          nodepoolutils.ListManaged()                    // API call, O(P) nodepools
          cloudProvider.GetInstanceTypes() per pool       // O(P) API calls
          NewTopology(ctx, ..., pods)
            buildDomainGroups(nodePools, instanceTypes)  // O(P * I * K) — pools × instance types × topology keys
            topology.Update() per pod                    // O(pods * topology groups)
          getDaemonSetPods()                             // API call
          scheduling.NewScheduler(ctx, ...)
            filterInstanceTypesByRequirements() per pool // O(P * I)
            getDaemonOverhead(templates, daemonSetPods)  // O(T * D) — templates × daemon pods
            getDaemonHostPortUsage(templates, daemonPods)// O(T * D)
            calculateExistingNodeClaims(stateNodes, daemonSetPods)
              for each stateNode (N-1 nodes):
                getCompatibleDaemonPods(node, daemonSetPods)
                  for each daemon pod (D pods):
                    isDaemonPodCompatibleWithNode(pod, taints, labels)
                      NewLabelRequirements(nodeLabels)   // 15+ map allocs
                      NewStrictPodRequirements(pod)      // additional map allocs
        scheduler.Solve(pods)                            // ~1% of total time
```

### Complexity Per SimulateScheduling Call

| Operation | Complexity | Notes |
|-----------|-----------|-------|
| `DeepCopyNodes` | O(N) | Deep copies all N state nodes |
| `ListManaged` (nodepools) | O(P) | API list call, P nodepools |
| `GetInstanceTypes` | O(P) | One API call per nodepool |
| `NewTopology` | O(P·I·K + pods·topo_groups) | Domain group construction + per-pod topology update |
| `getDaemonSetPods` | O(1) | API list call |
| `getDaemonOverhead` | O(T·D) | T templates × D daemon pods |
| `getDaemonHostPortUsage` | O(T·D) | Same |
| `calculateExistingNodeClaims` | **O(N·D)** | N-1 nodes × D daemon pods, each with allocs |
| `scheduler.Solve` | O(pods · (N + new_claims)) | Pod scheduling loop |

**Dominant term per call:** O(N·D) from `calculateExistingNodeClaims`, plus O(N) from
`DeepCopyNodes`, plus O(P·I) from instance type filtering.

### Total Single-Node Consolidation Complexity

Single-node consolidation calls `SimulateScheduling` once per candidate. With N
candidates (up to all active nodes):

**Total: O(N · (N·D + N + P·I·K))  =  O(N²·D + N² + N·P·I·K)**

For the reported scenario (N=500, D≈10-20 daemon pods, P≈3 pools, I≈hundreds of
instance types, K≈5-10 topology keys):

- `N²·D` ≈ 500² × 15 = **3.75 million** daemon compatibility checks
- Each check allocates `NewLabelRequirements` (15+ `sets.Set[string]` maps) and
  `NewStrictPodRequirements` — massive allocation pressure
- `N²` ≈ 250,000 deep copy operations of state nodes

This is **O(N²)** in the number of nodes, which matches the observed super-linear
CPU growth from the issue reporter's data:

| Nodes | Expected O(N²) ratio | Observed CPU ratio (vs 10 nodes) |
|-------|----------------------|----------------------------------|
| 10    | 1×                   | 1× (0.1 vCPU)                   |
| 60    | 36×                  | 4× (0.4 vCPU)                   |
| 110   | 121×                 | 7× (0.7 vCPU)                   |
| 360   | 1296×                | 21× (2.1 vCPU)                  |
| 500   | 2500×                | 30× (3.0 vCPU)                  |

The observed growth is sub-quadratic because: (a) the 3-minute timeout truncates
evaluation before all candidates are checked at large N, and (b) early-exit on
finding a valid consolidation command means not all N candidates are always evaluated.

## 2. Complexity of Multi-Node Consolidation

Multi-node consolidation uses binary search over the first `min(N, 100)` candidates
to find the largest set that can be consolidated together.

```
MultiNodeConsolidation.ComputeCommands()
  sortCandidates(candidates)                    // O(N log N)
  firstNConsolidationOption(candidates, 100)
    binary search [min=1, max=min(N,100)]:      // O(log N) iterations
      computeConsolidation(candidates[0..mid])
        SimulateScheduling(...)                  // Same as above: O(N·D + N + P·I·K)
```

**Total: O(log(min(N,100)) · (N·D + N + P·I·K))**

With the cap at 100 candidates, this is effectively **O(log(100) · N·D) ≈ O(7·N·D)**,
which is **O(N)** — linear in node count. The cap at 100 candidates is the key
difference that makes multi-node consolidation scale better than single-node.

However, each individual `SimulateScheduling` call is still expensive at large N
because `DeepCopyNodes` and `calculateExistingNodeClaims` still iterate all N nodes.

## 3. Bottleneck Identification

From the pprof data in the issue:

| Component | % of SimulateScheduling CPU | Root Cause |
|-----------|---------------------------|------------|
| `NewScheduler` | **78.85%** | Reconstructs full scheduler state per candidate |
| `DeepCopyNodes` | **18.95%** | Deep copies all N nodes per candidate |
| `Scheduler.Solve` | **1.12%** | Actual scheduling is cheap |
| GC overhead | **39.5% of total** | Allocation churn from the above |

### Why NewScheduler is 78.85%

Inside `NewScheduler`, the dominant cost is `calculateExistingNodeClaims`:

```go
func (s *Scheduler) calculateExistingNodeClaims(ctx, stateNodes, daemonSetPods) {
    for _, node := range stateNodes {           // N-1 nodes
        daemons := s.getCompatibleDaemonPods(ctx, node, taints, daemonSetPods)
        // ...
    }
}

func (s *Scheduler) getCompatibleDaemonPods(ctx, node, taints, daemonSetPods) {
    for _, p := range daemonSetPods {           // D daemon pods
        if s.isDaemonPodCompatibleWithNode(p, taints, node.Labels()) {
            // ...
        }
    }
}

func (s *Scheduler) isDaemonPodCompatibleWithNode(p, taints, nodeLabels) bool {
    scheduling.NewLabelRequirements(nodeLabels)     // allocates 15+ maps
    scheduling.NewStrictPodRequirements(p)          // allocates more maps
    // ...
}
```

**None of this depends on which candidate is being removed.** The daemon pod
compatibility with existing nodes is invariant across candidate evaluations within
a single consolidation pass. The only thing that changes is which node is excluded
from `stateNodes`.

### Why DeepCopyNodes is 18.95%

`cluster.DeepCopyNodes()` acquires a read lock and deep-copies every `StateNode`.
Each `StateNode` contains a `Node`, `NodeClaim`, and several maps of resource
requests/limits. At 500 nodes, this is ~500 deep copies per candidate evaluation,
totaling 250,000 deep copies across a full single-node consolidation pass.

## 4. Evaluation of Proposed Fixes

### 4a. Scheduler Caching Patch (mcornea's experiment)

The community member cached the `Scheduler` construction across candidate evaluations,
avoiding redundant `NewScheduler` calls.

**Why it helped at 60 nodes but not 500:**

At 60 nodes, `NewScheduler` construction (O(N·D)) is a large fraction of total work.
Caching it eliminates the dominant cost. At 500 nodes, even with caching:

1. `DeepCopyNodes` still runs per candidate: 500 × O(500) = O(250K) copies
2. The cached scheduler's `existingNodes` list is stale — it was built with all N
   nodes, but each candidate evaluation needs N-1 nodes (excluding the candidate).
   The patch likely doesn't correctly handle this, meaning the scheduling simulation
   may produce incorrect results.
3. GC pressure from `DeepCopyNodes` alone is substantial at 500 nodes.
4. The `Topology` is also rebuilt per call via `provisioner.NewScheduler`, which
   calls `NewTopology`. This involves `buildDomainGroups` (O(P·I·K)) and per-pod
   topology updates. Caching the scheduler doesn't cache the topology.

**Fundamental issue:** The patch addresses a constant factor (scheduler construction)
but doesn't change the algorithmic complexity. The O(N²) structure remains.

### 4b. Simplified Single-Node Approach (maintainer's suggestion)

The maintainer suggests: for single-node consolidation, only consider the "new
capacity" path — can this node be replaced with a cheaper one? Don't simulate
scheduling pods onto existing nodes.

**How it works:**
- For each candidate, check if a cheaper instance type exists that satisfies the
  same requirements
- No `SimulateScheduling` needed — decision based only on the candidate node and
  its nodepool's available instance types

**Complexity:** O(N · I) — for each of N candidates, scan I instance types for a
cheaper option. This is **O(N)** in node count.

**Tradeoffs:**

| Aspect | Current | Simplified |
|--------|---------|-----------|
| Complexity | O(N²·D) | O(N·I) |
| Decision type | Delete or Replace | Replace only (always delete) |
| Pod placement | Verified by simulation | Deferred to provisioner |
| Churn risk | Low (verified) | Higher (may cycle if pods don't fit) |
| Consolidation quality | Optimal (considers existing capacity) | Suboptimal (misses "pods fit on existing nodes" case) |

**Key risk:** Without simulation, Karpenter can't verify that evicted pods will
actually schedule somewhere. This could cause:
- Pods stuck in Pending if no existing node has capacity and the replacement node
  is insufficient
- Consolidation loops: delete node → pods pending → provision new node → new node
  is same price → no consolidation → but pods shifted, triggering another evaluation

**Mitigation needed:** A feedback mechanism to detect and break consolidation cycles
(e.g., track recent consolidation actions and suppress re-consolidation of recently
created nodes).

### 4c. Bounded Multi-Node (already implemented)

Multi-node consolidation already caps at 100 candidates, giving O(log(100) · N·D)
≈ O(N) scaling. This is a good pattern that single-node consolidation lacks.

## 5. Fundamental Lower Bounds

### What must be computed?

For single-node consolidation, the fundamental question per candidate is: "If I
remove this node, can its pods be placed elsewhere (existing nodes or a cheaper
replacement)?"

**Lower bound for exact answer:** Ω(N) per candidate — you must at minimum check
whether the remaining N-1 nodes have capacity for the evicted pods. Over N candidates,
this gives Ω(N²) for an exact answer for all candidates.

**Can we do better?** Yes, if we accept approximate or incremental answers:

1. **Precompute invariants once:** Daemon pod compatibility, topology domain groups,
   and instance type filtering don't change across candidates. Computing them once
   reduces per-candidate cost from O(N·D + P·I·K) to O(pods_on_candidate).

2. **Incremental state updates:** Instead of deep-copying all N nodes per candidate,
   maintain a single scheduler state and incrementally remove/restore one node at a
   time. This reduces `DeepCopyNodes` from O(N) per candidate to O(1) amortized.

3. **Capacity summary structures:** Precompute aggregate free capacity across all
   nodes. For each candidate, check if its pods' total resource requests fit within
   the aggregate free capacity minus the candidate's contribution. This is O(1) per
   candidate after O(N) precomputation, but doesn't account for fragmentation,
   topology constraints, or affinity rules.

### Theoretical bounds

| Approach | Per-candidate | Total (N candidates) | Accuracy |
|----------|--------------|---------------------|----------|
| Current (exact simulation) | O(N·D) | O(N²·D) | Exact |
| Cached invariants + incremental | O(P_c) | O(N·P_c + N·D) | Exact |
| Capacity summary (approximate) | O(1) | O(N) | Approximate |
| Simplified new-capacity-only | O(I) | O(N·I) | Partial |

Where P_c = pods on the candidate node (typically small, ~10-50).

## 6. Architectural Changes for O(N) or O(N log N)

### Approach A: Precompute + Incremental Simulation

**Core idea:** Separate the invariant setup (done once) from the per-candidate
evaluation (done N times).

```
Phase 1: Setup (once, O(N·D + P·I·K))
  - DeepCopyNodes once
  - Build topology once
  - Compute daemon overhead once
  - Compute daemon compatibility per node once
  - Build base scheduler state with all N nodes

Phase 2: Per-candidate evaluation (N times, O(P_c) each)
  - Remove candidate from scheduler's existing nodes list    // O(1)
  - Add candidate's pods to the scheduling queue             // O(P_c)
  - Run Solve() with the modified state                      // O(P_c · N) worst case
  - Restore candidate to existing nodes list                 // O(1)
```

**Total: O(N·D + N·P_c·N) = O(N·D + N²·P_c)**

If P_c << D (typical: ~20 pods per node vs ~15 daemon pods), this is still O(N²)
in the worst case due to `Solve()` scanning all existing nodes per pod. However,
the constant factor is dramatically smaller because:
- No repeated `NewScheduler` construction (saves 78.85% of current cost)
- No repeated `DeepCopyNodes` (saves 18.95%)
- `Solve()` is already only 1.12% of current cost

**Practical improvement:** ~50-100× faster for the 500-node case, bringing 3 vCPU
down to ~30-60 mCPU.

### Approach B: Two-Phase Filter + Simulate

**Core idea:** Use a cheap O(1) filter to eliminate most candidates, then run full
simulation only on promising ones.

```
Phase 1: Capacity analysis (once, O(N))
  - Compute total free capacity across all nodes
  - For each candidate, check: does (total_free + candidate_capacity) >= candidate_pod_requests?
  - If no: candidate cannot be consolidated (skip)
  - If yes: candidate is a consolidation prospect

Phase 2: Full simulation (K prospects, O(K · cost_per_sim))
  - Run current SimulateScheduling only for prospects
```

**Total: O(N + K · N·D)** where K << N in practice.

This doesn't change worst-case complexity (K could equal N) but dramatically reduces
average case. In a well-utilized cluster, most nodes can't be consolidated, so K is
small.

### Approach C: Simplified Single-Node + Bounded Multi-Node (maintainer's suggestion)

**Core idea:** Accept that single-node consolidation doesn't need full simulation.

- Single-node: O(N·I) — just check if a cheaper instance type exists
- Multi-node: O(log(100) · N·D) — already bounded, keep as-is
- Add cycle detection to prevent consolidation loops

**Total: O(N·I + N·D) = O(N·(I+D))**

This is the simplest path to O(N) but sacrifices consolidation quality for
single-node decisions. The "delete and let provisioner handle it" approach means:
- All single-node consolidation becomes delete (not replace)
- Provisioner may create a same-cost or more-expensive node
- Need anti-churn mechanisms

### Recommendation

**Short term (highest impact, lowest risk):** Approach A — precompute invariants.
This preserves exact simulation semantics while eliminating ~97% of per-candidate
cost. The key changes:

1. Hoist `DeepCopyNodes` out of `SimulateScheduling` — call it once and pass the
   result in
2. Hoist `provisioner.NewScheduler` construction out — build the scheduler once with
   all nodes, then for each candidate, temporarily remove it from `existingNodes`
3. Cache daemon pod compatibility results — `isDaemonPodCompatibleWithNode` results
   don't change across candidates

**Medium term:** Approach B — add a capacity pre-filter. After implementing A, the
remaining cost is `Solve()` which is O(P_c · N). A capacity pre-filter can skip
candidates whose pods clearly won't fit, reducing the number of `Solve()` calls.

**Long term:** Approach C — simplified single-node. This requires careful design of
anti-churn mechanisms and is a behavioral change that affects all users. Should be
gated behind a feature flag and validated at scale before becoming default.

## 7. Summary

| Metric | Current | With Approach A | With Approach C |
|--------|---------|----------------|----------------|
| Single-node complexity | O(N²·D) | O(N·D + N²·P_c) | O(N·I) |
| 500-node CPU (estimated) | 3 vCPU | ~30-60 mCPU | ~5-10 mCPU |
| Consolidation accuracy | Exact | Exact | Approximate |
| Implementation risk | — | Low (refactor) | Medium (behavioral change) |
| Breaking change | — | No | Yes (feature-gated) |

The root cause is clear: `SimulateScheduling` rebuilds the entire scheduler state
from scratch for every candidate, when ~99% of that state is invariant across
candidates. The fix is to separate invariant computation from per-candidate
evaluation.
