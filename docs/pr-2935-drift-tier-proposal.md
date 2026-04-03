# Proposed RFC Update: Three-Tier Node Ranking with Drifted Priority

| Field | Value |
|-------|-------|
| **Target RFC** | `designs/pod-deletion-cost-controller.md` (branch: `Pod-Deletion-Cost-RFC`) |
| **Related** | `docs/rfc-slo-drift-budget.md` (SLO drift budget cooperative principle) |
| **Change Type** | Additive — extends two-group partitioning to three groups |

## Rationale

The current RFC partitions nodes into two groups: Group A (normal, consolidate-able) and Group B (do-not-disrupt). This misses an opportunity: drifted nodes (`ConditionTypeDrifted=True`) need replacement anyway for compliance/security reasons. Directing RS scale-down deletions toward drifted nodes first means scale-down events naturally drain drifted nodes, helping both consolidation AND drift progress — the same cooperative drift-consolidation principle from the SLO drift budget RFC (`docs/rfc-slo-drift-budget.md`), applied at the pod-deletion-cost layer.

Three-tier ranking:

1. **Group A: Drifted nodes** — lowest deletion costs. Nodes with `ConditionTypeDrifted=True`, sorted by pod count ascending.
2. **Group B: Normal nodes** — middle deletion costs. Non-drifted nodes with no do-not-disrupt pods, sorted by pod count ascending.
3. **Group C: Do-not-disrupt nodes** — highest deletion costs. Nodes with at least one do-not-disrupt pod (protected from both consolidation and drift), sorted by pod count ascending.

---

## Section 1: Summary

**Current text (paragraph 1):**
> This RFC proposes a new feature-gated controller for Karpenter that ranks nodes by consolidation preference and propagates that ranking to pods via the `controller.kubernetes.io/pod-deletion-cost` annotation.

**Proposed replacement:**
> This RFC proposes a new feature-gated controller for Karpenter that ranks nodes by consolidation preference — with drifted nodes prioritized for early draining — and propagates that ranking to pods via the `controller.kubernetes.io/pod-deletion-cost` annotation.

**Rationale:** Surfaces the three-tier ranking in the summary so readers immediately understand drifted nodes are treated as a distinct priority class.

---

## Section 2: "Why node-level ranking is the right signal"

**Current text (paragraph starting "This logic extends naturally..."):**
> This logic extends naturally to nodes that Karpenter cannot consolidate. Some pods carry the `karpenter.sh/do-not-disrupt` annotation, which tells Karpenter to leave their node alone entirely. If the ReplicaSet controller deletes pods from one of these nodes during scale-down, that deletion has zero consolidation value because Karpenter can't act on the node regardless. The ranking engine accounts for this by partitioning nodes into two groups before ranking: Group A contains nodes with no do-not-disrupt pods (normal, consolidate-able nodes), and Group B contains nodes with at least one do-not-disrupt pod (protected nodes). Group A always receives lower deletion costs than Group B, so the ReplicaSet controller removes pods from nodes Karpenter can consolidate first.

**Proposed replacement:**
> This logic extends naturally to nodes that Karpenter cannot consolidate and to nodes that are already marked for replacement. The ranking engine partitions nodes into three groups before ranking:
>
> - **Group A (Drifted):** Nodes with `ConditionTypeDrifted=True` and no do-not-disrupt pods. These nodes need replacement anyway for compliance or security reasons. Draining them first via RS scale-down means scale-down events naturally assist drift progress — the same cooperative drift-consolidation principle described in the [SLO drift budget RFC](../docs/rfc-slo-drift-budget.md). Sorted by pod count ascending (fewest pods = lowest cost = drained first).
> - **Group B (Normal):** Nodes that are not drifted and have no do-not-disrupt pods. Standard consolidation targets. Sorted by pod count ascending.
> - **Group C (Do-not-disrupt):** Nodes with at least one `karpenter.sh/do-not-disrupt` pod. Karpenter cannot act on these nodes regardless, and they are also protected from drift. Deleting pods from them has zero consolidation or drift value. Sorted by pod count ascending.
>
> Group A always receives the lowest deletion costs, Group B the middle range, and Group C the highest, so the ReplicaSet controller removes pods from drifted nodes first, then normal consolidation targets, and protected nodes last.

**Rationale:** Replaces the two-group description with three groups. Explicitly connects to the drift budget RFC's cooperative principle.

---

## Section 3: Ranking Engine Description (under "How it works")

**Current text (step 4-6):**
> 4. Partitions nodes into Group A (no do-not-disrupt pods) and Group B (has do-not-disrupt pods)
> 5. Ranks each group independently by pod count
> 6. Assigns sequential integer ranks starting at -n (where n is the total number of Karpenter-managed nodes) for Group A, continuing for Group B

**Proposed replacement:**
> 4. Partitions nodes into Group A (drifted, no do-not-disrupt pods), Group B (not drifted, no do-not-disrupt pods), and Group C (has do-not-disrupt pods)
> 5. Ranks each group independently by pod count ascending
> 6. Assigns sequential integer ranks starting at -n (where n is the total number of Karpenter-managed nodes) for Group A, continuing for Group B, then Group C

**Rationale:** Aligns the numbered steps with the three-tier partitioning.

---

## Section 4: Diagram (under "How it works")

**Current diagram excerpt:**
```
│  │        Ranking Engine                │
│  │  1. Partition by do-not-disrupt      │
│  │  2. Rank Group A (normal)            │
│  │  3. Rank Group B (protected)         │
│  │  4. Assign sequential ranks          │
│  │     A: -n, -(n-1), ...               │
│  │     B: continues after A             │
```

**Proposed replacement:**
```
│  │        Ranking Engine                │
│  │  1. Partition: drifted / normal /    │
│  │     do-not-disrupt                   │
│  │  2. Rank Group A (drifted)           │
│  │  3. Rank Group B (normal)            │
│  │  4. Rank Group C (do-not-disrupt)    │
│  │  5. Assign sequential ranks          │
│  │     A: -n, -(n-1), ...              │
│  │     B: continues after A            │
│  │     C: continues after B            │
```

**Rationale:** Diagram must reflect the three-tier partitioning logic.

---

## Section 5: Example — Add Drifted Node Priority

**Add after the existing "Partial drain converges" example:**

> ### Example: Drifted node drains first via three-tier ranking
>
> A cluster with 3 nodes. Node A is drifted (pending AMI replacement):
>
> ```
> Node A (m5.xlarge):  3 pods, drifted (ConditionTypeDrifted=True)
> Node B (m5.2xlarge): 3 pods, normal
> Node C (m5.large):   3 pods, normal
> ```
>
> Three-tier ranking:
>
> ```
> Group A (drifted):
>   Node A (3 pods) → rank -3
>
> Group B (normal), sorted by pod count ascending:
>   Node B (3 pods) → rank -2
>   Node C (3 pods) → rank -1
> ```
>
> **Scale from 9 to 6 replicas.** The ReplicaSet controller sees all 3 pods on Node A have the lowest deletion cost (-3). It removes all 3 from Node A. Node A is now empty. Karpenter consolidates it with zero disruption — and the drifted node is replaced, achieving both cost savings and drift progress in a single scale-down event.
>
> **Without three-tier ranking:** Node A would compete equally with Nodes B and C for deletions. The drifted node might not drain first, delaying drift compliance while providing no additional consolidation benefit.

**Rationale:** Demonstrates the concrete benefit of prioritizing drifted nodes.

---

## Section 6: Appendix B — Do-Not-Disrupt Partitioning

**Current text:**
> Partitioning and ranking (by pod count, fewest first):
>
> ```
> Group A (normal), sorted by pod count ascending:
>   Node E (2 pods) → rank -4 (consolidation target)
>   Node G (3 pods) → rank -3
>   Node F (5 pods) → rank -2
>
> Group B (do-not-disrupt), sorted by pod count ascending:
>   Node D (4 pods) → rank -1  (always above all Group A nodes)
> ```

**Proposed replacement (add a drifted node to the example):**

> Suppose Node G is also drifted (`ConditionTypeDrifted=True`). Partitioning and ranking:
>
> ```
> Group A (drifted, no do-not-disrupt), sorted by pod count ascending:
>   Node G (3 pods) → rank -4 (drifted consolidation target)
>
> Group B (normal, not drifted, no do-not-disrupt), sorted by pod count ascending:
>   Node E (2 pods) → rank -3
>   Node F (5 pods) → rank -2
>
> Group C (do-not-disrupt), sorted by pod count ascending:
>   Node D (4 pods) → rank -1 (always above all Group A and B nodes)
> ```
>
> The ReplicaSet controller removes pods from drifted Node G first (rank -4), then normal Node E (rank -3). Node D's ML training job is never touched. Drifted Node G drains before non-drifted Node E despite Node E having fewer pods, because drift priority takes precedence over pod count across groups.

**Rationale:** Shows all three tiers in action with a concrete example. Demonstrates that drift priority overrides pod-count ordering across groups.

---

## Section 7: Appendix A — RankingEngine Component Description

**Current text:**
> **RankingEngine (ranking.go):** Ranks nodes by pod count (mirroring Karpenter's consolidation candidate sorting). Before ranking, it partitions nodes into two groups: Group A (no do-not-disrupt pods) and Group B (has at least one do-not-disrupt pod). Each group is ranked independently. Group A gets lower ranks and Group B gets higher ranks (continuing sequentially after Group A). Uses `sort.SliceStable` with deterministic tie-breaking by node name.

**Proposed replacement:**
> **RankingEngine (ranking.go):** Ranks nodes by pod count (mirroring Karpenter's consolidation candidate sorting). Before ranking, it partitions nodes into three groups: Group A (drifted, no do-not-disrupt pods), Group B (not drifted, no do-not-disrupt pods), and Group C (has at least one do-not-disrupt pod). Each group is ranked independently by pod count ascending. Group A gets the lowest ranks, Group B the middle range, and Group C the highest (continuing sequentially). Uses `sort.SliceStable` with deterministic tie-breaking by node name. Drift status is read from `ConditionTypeDrifted` on the node's `StateNode`.

**Rationale:** Aligns the component description with three-tier partitioning and specifies the drift condition source.

---

## Section 8: Ranking Strategy (under "Proposal")

**Current text:**
> The controller ranks nodes the same way Karpenter ranks consolidation candidates: by pod count (disruption cost).

**Proposed addition (append after existing sentence):**
> Within this ranking, drifted nodes (`ConditionTypeDrifted=True`) are placed in the lowest-cost tier, ahead of non-drifted nodes. This follows the cooperative drift-consolidation principle from the [SLO drift budget RFC](../docs/rfc-slo-drift-budget.md): since drifted nodes need replacement anyway, directing RS deletions toward them first means scale-down events naturally assist drift progress without additional disruptions.

**Rationale:** Connects the ranking strategy to the drift cooperation principle at the point where ranking is first described in the Proposal section.
