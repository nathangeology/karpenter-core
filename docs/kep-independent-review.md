# Independent Review: KEP-NNNN ReplicaSet Consolidation-Aware Scale-In Strategy

**Reviewer:** karpenter/polecats/quartz
**Date:** 2026-04-03
**KEP PR:** https://github.com/nathangeology/enhancements/pull/2
**Status:** Provisional (targeting v1.37 alpha)

---

## Strengths

1. **Well-identified problem.** The tension between the ReplicaSet spreading heuristic and node consolidation is real and well-documented. The HPA scale-down example (20→10 replicas leaving every node partially occupied) is concrete and immediately understandable.

2. **Minimal, focused scope.** Rather than proposing a pluggable framework or webhook extension point, the KEP adds a single feature-gated heuristic. This is the right approach for getting something merged — previous broader proposals (#107598, #123541) died without implementation.

3. **Clean integration with existing sort order.** The modification to `ActivePodsWithRanks.Less()` slots into the existing priority chain without disrupting steps 1-4 or the age tiebreaker. PodDeletionCost (KEP-2255) retains precedence, which is correct.

4. **Thorough PRR questionnaire.** The production readiness section is complete with failure modes, rollback strategy, scalability analysis, and monitoring requirements. The stateless nature of the feature makes rollback trivial.

5. **Good alternatives analysis.** The three alternatives (extend PodDeletionCost, pluggable framework, webhook) are discussed with clear rationale for rejection.

6. **User stories are concrete and relatable.** Story 1 (HPA-driven consolidation) and Story 3 (off-peak cost optimization) directly address real operator pain points.

---

## Issues

### Blockers

**B1: Non-Goals section is empty.**
The Non-Goals section says "Will add these based on feedback." This will not pass sig-apps review. Non-Goals are required to scope the proposal and prevent scope creep during review. Reviewers need to know what this KEP explicitly does NOT do.

*Suggestion:* Add at minimum:
- Not modifying scheduling behavior (topology spread enforcement remains at schedule time only)
- Not providing per-workload opt-in/opt-out (the feature gate is cluster-wide)
- Not replacing or deprecating PodDeletionCost (KEP-2255)
- Not providing resource-aware scoring (CPU/memory utilization is not considered; only pod count)
- Not handling StatefulSet scale-down (different controller, different ordering semantics)

**B2: The "global view" pod counting crosses a significant architectural boundary.**
Step 6 counts *all active pods on the same node across all namespaces and controllers*. This means the ReplicaSet controller now makes deletion decisions based on workloads it doesn't own. This is a significant departure from the current model where the ReplicaSet controller only reasons about its own replicas.

Concrete concern: A node running 50 pods from DaemonSets + system workloads will always appear "full" and its pods will be deprioritized for deletion, even if it's the ideal candidate for consolidation from the node autoscaler's perspective. The pod count is a poor proxy for node utilization.

*Suggestion:* Acknowledge this limitation explicitly. Consider whether the count should exclude DaemonSet pods and mirror pods (which are present on every node and add noise). At minimum, document that pod count is a heuristic proxy and not a utilization measure. A sig-apps reviewer will ask about this.

**B3: Coupling to `karpenter.sh/do-not-disrupt` in alpha is problematic for a core Kubernetes KEP.**
The KEP proposes checking a vendor-specific annotation (`karpenter.sh/do-not-disrupt`) in the core ReplicaSet controller. This will be a hard sell for sig-apps and sig-architecture. Kubernetes core components should not reference vendor-specific annotations.

*Suggestion:* Introduce `controller.kubernetes.io/do-not-disrupt` from day one in alpha. If backward compatibility with existing Karpenter deployments is needed, check both annotations in alpha, but lead with the generic one. The current plan (generic annotation only in beta) means alpha ships with a vendor coupling that reviewers will flag immediately.

**B4: Missing "Prerequisite testing updates" section.**
The Test Plan section is missing the "Prerequisite testing updates" subsection required by the KEP template. This subsection should list existing tests that need modification and link to the relevant test files with coverage data.

*Suggestion:* Add the subsection. List the existing `ActivePodsWithRanks` tests in `controller_utils_test.go` that will need updating, and provide current coverage percentages for the files being modified.

### Should-Fix

**S1: The do-not-disrupt check in step 5 scans pods from "any source" — performance implications unclear.**
The KEP says step 5 checks for do-not-disrupt pods "from any source even outside the current replicaset" co-located on the node. This means for each candidate pod, the controller must scan all pods on that node for the annotation. With N candidate pods across M nodes, this is O(N×P) where P is the average pods-per-node.

The Scalability section claims O(N) but this appears to undercount. The pod-per-node counting via indexer is O(N), but the do-not-disrupt annotation scan requires iterating all pods on each node.

*Suggestion:* Clarify the actual complexity. Consider pre-computing a `nodeHasDoNotDisrupt` map in a single pass over all pods (O(total pods in cluster)) before the sort, rather than checking per-candidate. The KEP mentions `nodeHasDoNotDisrupt` maps as "ephemeral" but doesn't describe the computation strategy.

**S2: No discussion of interaction with PodDisruptionBudgets (PDBs).**
PDBs are the primary mechanism for controlling pod disruption in Kubernetes. The KEP doesn't discuss how the consolidation heuristic interacts with PDBs. If the heuristic selects pods for deletion that are protected by a PDB, the deletion will fail and the controller will retry — potentially thrashing.

*Suggestion:* Add a section on PDB interaction. Clarify whether PDB-protected pods should be deprioritized in the ranking (similar to do-not-disrupt) or whether the existing PDB enforcement at deletion time is sufficient.

**S3: The node informer is initialized but barely used in alpha.**
The KEP initializes a node informer "for future use" (resource-based scoring). Initializing infrastructure for future features adds complexity and memory overhead without current benefit. Sig-apps reviewers may push back on this.

*Suggestion:* Either remove the node informer from alpha scope entirely (add it when resource-based scoring is implemented), or provide a concrete alpha use case. "Future use" is not a justification for adding informer overhead.

**S4: kep.yaml is incomplete.**
- `authors` is `@TBD` — must be filled in
- `reviewers` and `approvers` are `TBD` — need actual sig-apps reviewers
- `metrics` is empty — should list the planned beta metrics
- No `prr-approvers` field

*Suggestion:* Fill in all TBD fields before submitting to sig-apps. Identify sig-apps reviewers and approvers. Add PRR approver.

**S5: Topology spread constraint interaction needs more depth.**
The Risks section acknowledges that consolidation may cause remaining pods to "temporarily violate the desired spread until the next scale-up event." This undersells the issue. If a Deployment scales from 20→10 and the consolidation heuristic packs the remaining 10 onto 2 nodes, topology spread constraints are violated until the *next scale-up*, which may never happen for a stable workload. This is a permanent violation, not temporary.

*Suggestion:* Be more precise about when spread is restored. Acknowledge that for workloads that scale down and stay down, the spread violation is persistent. Consider whether the heuristic should factor in topology spread constraints (even if the answer is "not in alpha, future work").

**S6: No discussion of interaction with pod priority and preemption.**
High-priority pods and preemption are part of the Kubernetes scheduling/eviction model. The KEP doesn't discuss whether pod priority should influence the consolidation ranking.

*Suggestion:* At minimum, state that pod priority is not considered in the consolidation heuristic and explain why (or add it as future work).

### Nice-to-Have

**N1: The "consolidation efficiency ratio" metric mentioned in the monitoring section would be valuable for alpha, not just future consideration.** Without it, operators have no way to measure whether the feature is actually improving consolidation.

**N2: Consider adding a worked example.** A concrete before/after showing pod placement across nodes with the spreading heuristic vs. the consolidation heuristic would make the Design Details section much clearer. Show a 5-node cluster, an HPA scale-down event, and the resulting pod distribution under each heuristic.

**N3: The Implementation History references a `consolidation-strategy` branch with a reference implementation.** Link to it. Reviewers will want to see the code.

**N4: The KEP number is still NNNN.** This needs a real number before formal submission. File an enhancement issue in kubernetes/enhancements to get one assigned.

---

## Assessment

**Ready for sig-apps review?** Not yet. The KEP is well-structured and addresses a real problem, but has several issues that would block approval:

1. Empty Non-Goals section (B1) — trivial to fix but will be rejected on sight
2. Vendor-specific annotation in core Kubernetes (B3) — needs architectural resolution before submission
3. Global pod counting crossing controller boundaries (B2) — needs explicit acknowledgment and discussion of limitations
4. Missing prerequisite testing updates (B4) — template compliance issue

**What needs to happen first:**
1. Fill in Non-Goals (B1) and kep.yaml fields (S4)
2. Lead with the generic `controller.kubernetes.io/do-not-disrupt` annotation from alpha (B3)
3. Add explicit discussion of the pod-count-as-proxy limitation and DaemonSet noise (B2)
4. Add Prerequisite testing updates section (B4)
5. Expand PDB interaction discussion (S2)
6. Clarify the do-not-disrupt scanning complexity (S1)
7. Either justify or remove the node informer from alpha (S3)

After addressing blockers B1-B4 and should-fixes S1-S3, this KEP would be in good shape for sig-apps review. The core idea is sound and fills a genuine gap in the Kubernetes scaling story.
