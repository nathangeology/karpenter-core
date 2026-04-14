# KEP Style Recommendations — Batch 2: sig-node / sig-autoscaling KEPs

**Source KEPs studied:**
- KEP-3063: Dynamic Resource Allocation (sig-node) — large, multi-release, ultimately withdrawn
- KEP-2400: Node Swap Support (sig-node) — controversial change requiring broad community buy-in
- KEP-4563: Eviction Request API (sig-node) — directly related to our problem space

**Our doc:** `problem-statement.md` for ReplicaSet Consolidation Scale-In (KEP-5982)

---

## Pattern Observations

### 1. Lead with a concrete, universally understood pain point

All three KEPs open with a problem that any Kubernetes operator can immediately recognize, even if they've never thought about the specific subsystem.

**KEP-2400 (Swap)** opens with the blunt fact that Kubernetes has never supported swap, then immediately lists the real-world consequences: OOM kills that could be avoided, overprovisioned nodes, edge devices that can't run Kubernetes at all. It doesn't start with kernel internals — it starts with "here's what breaks for you today."

**KEP-4563 (Eviction Request)** opens with a numbered list of seven concrete problems with today's eviction/PDB model, each linked to a real GitHub issue. The reader can scan the list and find their own pain point within seconds. This is effective because it builds a coalition: different readers care about different items, but they all agree the status quo is broken.

**KEP-3063 (DRA)** opens with the limitation of existing device plugins and immediately grounds it in hardware use cases (CXL-attached accelerators, FPGA pipelines). Even readers unfamiliar with DRA can understand "I have hardware that doesn't fit the current model."

**Pattern:** The problem statement earns the reader's attention by describing a situation they already experience, before introducing any proposed mechanism.

### 2. Separate the problem from the solution — and keep them proportional

**KEP-2400** is disciplined about this. The Motivation section is pure problem framing: scenarios, user stories, and constraints. The Proposal section is separate and explicitly scoped ("this KEP covers scenarios 1 and 2, not 3"). The reader never has to untangle "is this a requirement or a design choice?"

**KEP-4563** goes further: the Motivation section includes a structured list of standing PDB issues (with links), making it clear that the problem is well-documented and long-standing. The proposal is introduced only after the reader is convinced the problem is real and unsolved.

**KEP-3063** is a cautionary example. The problem statement is strong, but the design section grew so large and complex that it eventually contributed to the KEP being withdrawn. The lesson: a problem statement that implies a massive design surface should explicitly acknowledge the complexity cost and explain why it's worth paying.

**Pattern:** Keep the problem statement self-contained. A reader should be able to agree with the problem without having read the proposal. If the problem naturally implies a large solution, say so explicitly — don't let the reader discover it.

### 3. Handle controversy by being factual about tradeoffs, not by hedging

**KEP-2400 (Swap)** is the strongest example. Swap is controversial — it changes performance characteristics, reduces predictability, and has security implications. The KEP handles this by:
- Stating the risks plainly in a dedicated "Risks and Mitigations" section
- Providing concrete best practices (disable swap for system.slice, use a dedicated disk, use encrypted swap)
- Scoping the initial implementation narrowly (Burstable QoS only, no per-pod API, cgroupv2 only)
- Listing explicit non-goals to prevent scope creep
- Acknowledging what they learned during beta ("we found degradation of services if you allow system critical daemons to swap") — this honesty builds trust

**KEP-4563** handles the multi-stakeholder nature by explicitly listing adoption interest from external organizations (Uber, NVIDIA, Datadog, KubeVirt) with links to recordings. This transforms "we think people want this" into "here are the people who want this, and here's what they said."

**Pattern:** Don't hedge with "this might be controversial." Instead: state the tradeoff factually, show what you've learned from testing, scope the initial implementation to minimize risk, and let the reader evaluate. Evidence > opinion.

### 4. Use graduated scope to invite collaboration without losing direction

**KEP-2400** is masterful at this. The graduation criteria are detailed and specific for each stage (Alpha → Alpha2 → Beta1 → Beta2 → Beta3 → GA), with each stage having clear exit criteria. Future extensions (pod-level swap control, swap-aware scheduling, swap-aware evictions) are listed as separate KEPs with issue numbers. This tells collaborators: "here's where you can contribute" without making the current KEP responsible for everything.

**KEP-4563** uses a similar pattern with its "Future Improvements" section: Workload API Support, Preemption Support, and new EvictionRequest types are all explicitly deferred with enough detail that interested parties can start designing them. The Alpha2 graduation criteria include specific items to "consider" — this is an invitation to collaborate on design without committing to implementation.

**KEP-3063** tried to do too much in one KEP. The control-plane controller approach, the PodSchedulingContext negotiation protocol, and the cluster autoscaler interaction were all in scope simultaneously. When the community decided the complexity wasn't worth it, the entire KEP was withdrawn rather than scoped down. The lesson: if your KEP has natural decomposition points, use them.

**Pattern:** Define a minimal viable first step with clear boundaries. List future extensions as named items (ideally with issue numbers) so collaborators can self-select. Make graduation criteria specific enough that "done" is unambiguous.

### 5. Ground cross-SIG changes in the other SIG's vocabulary

**KEP-4563** is effective here because it frames the eviction problem in terms that sig-apps (PDBs, ReplicaSets), sig-scheduling (preemption), sig-node (kubelet, node drain), and sig-autoscaling (cluster autoscaler) all understand. Each user story maps to a different SIG's domain. The "Adoption" section explicitly names which components in each SIG would need changes.

**KEP-2400** grounds its proposal in CRI changes (sig-node), kubelet configuration (sig-node), and cgroup semantics (kernel). It doesn't try to speak to sig-scheduling or sig-apps because it doesn't need to — the scope is deliberately node-local.

**Pattern:** For cross-SIG proposals, frame the problem using each SIG's native concepts. Don't assume readers from sig-apps know sig-node terminology, or vice versa. Map your proposal's impact to each SIG explicitly.

---

## Specific Recommendations for Our Problem Statement

### What the doc does well

1. **The example is excellent.** The 5-node, 20-replica scenario with concrete numbers (before/after) makes the problem immediately tangible. This is on par with the best KEP examples studied.

2. **The timing problem section is strong.** The numbered sequence (HPA → RS controller → Karpenter) clearly shows why the problem can't be solved at the autoscaler layer alone. This is the kind of structural argument that convinces reviewers.

3. **The related work section is thorough.** Linking to prior discussions, existing KEPs, and active proposals shows the authors have done their homework.

4. **The "Design Questions" framing is good.** Asking specific questions invites focused feedback rather than open-ended bikeshedding.

### Recommendations for improvement

#### A. Strengthen the opening by leading with impact, not mechanism

The current opening paragraph jumps into the coordination gap between autoscalers and the RS controller. This is accurate but assumes the reader already cares about this coordination.

**Suggestion:** Open with the user-visible impact first. Something like: "When workloads scale down in clusters using node autoscalers, pod disruption is roughly 2x what it needs to be. Half the disruption comes from the scale-down itself; the other half comes from the autoscaler cleaning up what the scale-down left behind. This is because..." Then introduce the mechanism.

KEP-2400 and KEP-4563 both lead with "here's what breaks for you" before explaining why.

#### B. Add a "Non-Goals" section to bound the scope

The doc currently has "Design Questions" but no explicit non-goals. For a cross-SIG proposal, non-goals are critical because they tell each SIG what they don't need to worry about.

**Suggestion:** Add non-goals like:
- Changing scheduler placement decisions
- Modifying StatefulSet or Job scale-down behavior (in this KEP)
- Replacing or deprecating pod-deletion-cost
- Guaranteeing optimal node packing (this is a heuristic improvement, not an optimal solution)

KEP-2400's non-goals section is a good model — it's specific and explains why each item is excluded.

#### C. Be more factual about the tradeoff with topology spread

The doc mentions that the pod-count-per-node heuristic "doesn't reason about topology spread, pod affinity, or zone distribution" and then says "Neither does the current spreading heuristic." This is accurate but slightly defensive.

**Suggestion:** State the tradeoff directly: "The consolidation heuristic may temporarily violate soft topology spread preferences during scale-down. We believe this is acceptable because [the current heuristic also doesn't preserve them / topology spread is re-established when pods are rescheduled / the alternative is additional pod disruption from consolidation]. We are collecting data on this tradeoff as part of the Karpenter RFC."

KEP-2400 handles its performance tradeoff this way: "swap changes a system's behaviour under memory pressure" — stated plainly, then mitigated with specific controls.

#### D. Add concrete evidence from the Karpenter RFC

The doc references the Karpenter Pod Deletion Cost Controller RFC (PR #2935) and says "early results suggest it's effective." This is the weakest claim in the doc.

**Suggestion:** Include specific data if available — even preliminary numbers. KEP-4563 includes links to PoC implementations and recorded presentations. KEP-2400 includes specific test grid results. If the Karpenter RFC has simulation results, benchmark numbers, or even a worked example with real cluster data, include them or link to them. "Early results suggest" is exactly the kind of hedging that KEP-2400 avoids.

#### E. Frame the cross-SIG nature more explicitly

The doc mentions sig-apps, sig-node, sig-autoscaling, and wg-serving but doesn't map the proposal's impact to each SIG the way KEP-4563 does.

**Suggestion:** Add a brief section or table:

| SIG | Impact | What we need from them |
|-----|--------|----------------------|
| sig-apps | RS controller sort-order change | Review and approval of the heuristic change |
| sig-autoscaling | Primary beneficiary | Validation that the heuristic improves consolidation |
| sig-node | Eviction Request API interaction | Alignment on sequencing (RS change first, EvictionRequest later) |
| wg-serving | Related use case (load-aware scale-down) | Feedback on whether the mechanism is general enough |

This makes it easy for each SIG's reviewers to find their part.

#### F. Strengthen the "Proposed Approach" with graduation criteria

The doc proposes a feature gate (`ConsolidatingScaleDown`) but doesn't describe what Alpha, Beta, and GA look like. KEP-2400's detailed graduation criteria (with specific test requirements at each stage) are a model.

**Suggestion:** Even at the problem-statement stage, sketch the graduation path:
- Alpha: Feature gate, opt-in, RS controller change only, metrics for pod-count-per-node decisions
- Beta: Data from production clusters showing consolidation improvement, no regression in topology spread
- GA: Positive feedback from autoscaler maintainers, no increase in pod churn

This signals to reviewers that you've thought about the path to production, not just the initial implementation.

#### G. Consider adding user stories in KEP format

The doc's example is good, but KEP-2400 and KEP-4563 both use structured "User Stories" sections that map to specific personas (cluster admin, application owner, operator). Our doc could benefit from 2-3 user stories:

1. "As a cluster operator running Karpenter on 200+ nodes with HPA-driven workloads, I want scale-down events to naturally empty nodes so that Karpenter can remove them without additional pod disruption."
2. "As a platform team managing shared clusters, I want the RS controller's scale-down behavior to work with (not against) our node autoscaler, without requiring application teams to set pod-deletion-cost annotations."
3. "As a serving infrastructure owner (wg-serving), I want the ability to bias scale-down toward pods that are already being drained by my traffic shaper."

---

## Summary of Key Patterns

| Pattern | Our doc status | Priority |
|---------|---------------|----------|
| Lead with user-visible impact | Partially — opens with mechanism | High |
| Explicit non-goals | Missing | High |
| Factual tradeoff statements | Slightly hedged on topology spread | Medium |
| Concrete evidence / data | "Early results suggest" — needs specifics | High |
| Cross-SIG impact mapping | Mentioned but not structured | Medium |
| Graduation criteria sketch | Missing | Medium |
| Structured user stories | Has example but not in KEP format | Low |
