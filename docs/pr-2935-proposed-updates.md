# Proposed Updates to PR #2935 — Pod Deletion Cost Controller RFC

**PR**: https://github.com/kubernetes-sigs/karpenter/pull/2935
**Review**: https://github.com/kubernetes-sigs/karpenter/pull/2935#pullrequestreview-4051634464
**Enhancement Issue**: https://github.com/kubernetes/enhancements/issues/5982

---

## Update 1: Fill in the ReplicaSet Controller Strategy Section

The RFC currently has a TODO placeholder under "Alternatives Considered":

> ### TODO: Changing the replicaset controller behavior directly
> TODO: Came up on 3/20 in a convo with James. This note is to remind me to address this possibility here.

**Replace with the following:**

---

### Changing the ReplicaSet Controller Behavior Directly

An alternative (and complementary) approach is to modify the ReplicaSet controller's pod deletion algorithm itself, rather than influencing it indirectly through annotations. This is being pursued in parallel as a Kubernetes enhancement: [KEP: ReplicaSet Consolidation-Aware Scale-In Strategy (kubernetes/enhancements#5982)](https://github.com/kubernetes/enhancements/issues/5982).

#### Short-Term: Pod Deletion Cost Labels (This RFC)

The pod-deletion-cost approach proposed in this RFC is the near-term deliverable. It works with the existing ReplicaSet controller by using the `controller.kubernetes.io/pod-deletion-cost` annotation — a communication channel that already exists in Kubernetes (KEP-2255). This approach:

- Ships entirely within Karpenter (no upstream Kubernetes changes required)
- Works with all existing Kubernetes versions that support pod-deletion-cost
- Can be feature-gated and iterated on independently
- Provides 30-80% disruption reduction in benchmarks today

The tradeoff is operational complexity: Karpenter must continuously maintain annotations, handle change detection, and manage the two-annotation protocol to avoid conflicts with customer-set values. It also adds API server write load proportional to the number of managed pods.

#### Long-Term: Direct ReplicaSet Controller Change

The [kubernetes/enhancements#5982](https://github.com/kubernetes/enhancements/issues/5982) KEP proposes adding a `ConsolidatingScaleDown` feature gate to `kube-controller-manager`. When enabled, the ReplicaSet controller's pod deletion sort order changes to prefer deleting pods on nodes with *fewer* total active pods (a consolidation heuristic), reversing the current spreading heuristic. It also respects do-not-disrupt signals to deprioritize pods on protected nodes.

This approach:

- Eliminates the need for an external annotation-management controller
- Has zero API server overhead (no annotation writes, no reconcile loop)
- Applies universally to all ReplicaSets, not just those on Karpenter-managed nodes
- Is complementary to KEP-2255 — `pod-deletion-cost` annotations take precedence in the existing sort order, so both mechanisms coexist cleanly

The tradeoff is timeline: upstream Kubernetes changes require a KEP, sig-apps review, and multi-release graduation (alpha → beta → GA). The design is still in the proposal stage.

#### Relationship Between the Two Approaches

These approaches are complementary, not competing. Either one alone delivers the core benefit (consolidation-aware scale-in). Having both doesn't conflict — the ReplicaSet controller's sort order already checks `pod-deletion-cost` before its built-in heuristics, so if Karpenter sets annotations AND the RS controller has consolidation-aware sorting, the annotations simply reinforce the same preference.

The recommended path is:

1. **Ship the pod-deletion-cost controller now** (this RFC) — immediate value, no upstream dependency
2. **Pursue the RS controller KEP in parallel** — broader impact, zero-overhead solution
3. **Deprecate the annotation controller** once the RS controller change reaches GA and the minimum supported Kubernetes version includes it

---

## Update 2: Address Placement/Spreading Constraint Concerns

The author's self-review at https://github.com/kubernetes-sigs/karpenter/pull/2935#pullrequestreview-4051634464 notes:

> Left a note to myself to address spreading constraints in the RFC and how to think about those. This proposal focuses on binpacking and doesn't address that topic enough yet.

And the inline comment on the "Risks and Mitigations" section:

> Note to self to address topology spreading and topology here.

**Add the following new section after "Risks and Mitigations" (or as a new subsection within it):**

---

### Placement and Spreading Constraints

A natural concern is whether optimizing for consolidation (concentrating pod deletions on specific nodes) conflicts with topology spread constraints, pod affinity/anti-affinity rules, or zone distribution requirements. This section addresses that concern directly.

#### The current RS scale-down logic doesn't address placement concerns either

The existing ReplicaSet controller scale-down heuristic is: prefer pending pods over running, respect `pod-deletion-cost`, spread terminations across nodes (prefer nodes with more colocated replicas), prefer newer pods, then break ties randomly. This spreading heuristic does not consider topology spread constraints, pod affinity/anti-affinity, or zone distribution. It optimizes for a single goal — even distribution of deletions across nodes — and ignores all scheduling constraints.

The proposed change is not regressing from a placement-aware system. It is offering an alternative heuristic that optimizes for a different goal (lower pod disruption and better consolidation) while being equally unaware of scheduling constraints.

#### This proposal is an alternative heuristic, not a scheduling-aware system

The proposal does not attempt to link scheduling logic to RS scale-in decisions. It offers a different tiebreaker for the ReplicaSet controller's pod deletion sort:

- **Current heuristic**: prefer pods on nodes with more colocated replicas (spreading)
- **Proposed heuristic**: prefer pods on underutilized/consolidation-target nodes (consolidation)

Both are simple heuristics that don't reason about scheduling constraints. The difference is which operational goal they optimize for. The current heuristic optimizes for even distribution; the proposed heuristic optimizes for enabling node consolidation.

#### Balancing all concerns is a fundamentally harder problem

If we wanted RS scale-in to simultaneously consider topology spread constraints, pod affinity/anti-affinity, zone distribution, AND consolidation impact, we would need to scalarize and weight all of these concerns — essentially building a mini-scheduler for scale-down. That is a fundamentally different (and much more complex) proposal that would require:

- Reading and interpreting all scheduling constraints from pod specs
- Evaluating constraint satisfaction across candidate deletion sets
- Defining weights or priorities across competing objectives
- Handling constraint conflicts (e.g., "spread evenly across zones" vs. "consolidate onto fewer nodes")

This level of sophistication is out of scope for this RFC and would be a separate KEP entirely.

#### Current and proposed behaviors are symmetric in their simplicity

| Aspect | Current (Spreading) | Proposed (Consolidation) |
|--------|---------------------|--------------------------|
| Optimizes for | Even distribution across nodes | Enabling node removal |
| Considers scheduling constraints | No | No |
| Considers topology spread | No | No |
| Considers pod affinity | No | No |
| Considers zone distribution | No | No |
| Heuristic complexity | Simple (count colocated replicas) | Simple (node consolidation rank) |

Neither approach is "correct" in all cases — they represent different tradeoffs for different operational priorities. Clusters that prioritize even distribution can leave the feature disabled. Clusters that prioritize cost efficiency and reduced disruption can enable it.

#### A future scalarized approach could subsume both

If the community wants to build a weighted multi-objective scale-down scorer, both the current spreading heuristic and the proposed consolidation heuristic could become weighted factors in that system. The `ConsolidatingScaleDown` KEP ([kubernetes/enhancements#5982](https://github.com/kubernetes/enhancements/issues/5982)) is a step in this direction — it adds consolidation as an alternative heuristic behind a feature gate, leaving room for future work that combines multiple objectives. But that future work is a separate effort and should not block shipping a pragmatic improvement today.

---

## Update 3: Add Risk Entry for Spreading Constraint Interaction

**Add the following row to the "Risks and Mitigations" table:**

---

- **Consolidation-optimized deletions may conflict with topology spread constraints (Low):** When the controller concentrates deletions on specific nodes, the resulting pod distribution may temporarily violate `topologySpreadConstraints` until the scheduler places replacement pods. *Mitigation:* This is the same behavior as the current spreading heuristic — neither approach guarantees constraint satisfaction during scale-down. The Kubernetes scheduler enforces topology spread when placing new pods, so any temporary imbalance is corrected on the next scheduling cycle. Operators with strict spreading requirements can leave the feature disabled.

---

## Summary of Changes

| Section | Action | Description |
|---------|--------|-------------|
| Alternatives Considered → "TODO: Changing the replicaset controller behavior directly" | **Replace** | Fill in with short-term (pod-deletion-cost) vs. long-term (RS controller KEP) strategy, referencing kubernetes/enhancements#5982 |
| After "Risks and Mitigations" | **Add new section** | "Placement and Spreading Constraints" addressing reviewer concerns about topology spread, affinity, and zone distribution |
| Risks and Mitigations table | **Add row** | New risk entry for topology spread constraint interaction |
