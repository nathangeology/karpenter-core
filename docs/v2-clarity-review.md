# v2 Problem Statement: Clarity and Accuracy Review

Reviewer: jasper (polecat)
Date: 2026-04-21
Document: `enhancements/mayor/rig/keps/sig-apps/NNNN-replicaset-consolidation-scale-in/problem-statement-v2.md`

---

## Summary

The document is well-structured and clearly written overall. The problem framing is accessible, the use cases are concrete, and the design options are fairly presented. Below are findings organized by severity.

---

## Findings

### 1. RS controller sort order description is slightly imprecise

**Section:** The Problem
**Severity:** Minor (technically correct but could mislead)

> "Today it spreads deletions evenly across nodes — removing a few pods from every node rather than concentrating removals to empty out specific nodes."

The RS controller doesn't literally spread "evenly." Step 5 of the 8-step sort order prefers pods on nodes with *more* co-located related pods (higher rank = deleted first). This produces a spreading *effect*, but it's not an even-distribution algorithm — it's a greedy heuristic that removes from the most-populated nodes first. The result is spreading, but the mechanism is "prefer doubled-up pods," not "distribute deletions evenly."

**Suggestion:** Consider: "Today its sort order preferentially removes pods from nodes that have the most co-located replicas — effectively spreading deletions across nodes rather than concentrating them." This is more precise about the mechanism while keeping the same conclusion.

### 2. The concrete example oversimplifies the sort order

**Section:** A concrete example
**Severity:** Minor

The example assumes the RS controller removes exactly 2 pods from each of 5 nodes. In practice, the 8-step sort order considers pod phase, readiness, deletion cost, ready-duration, restart count, and creation time *before* the co-location rank (step 5). If all pods are identical (same phase, readiness, no deletion-cost annotations, similar age), then yes, the rank-based spreading dominates. But the example doesn't note this assumption.

**Suggestion:** Add a brief qualifier like "Assuming all pods are healthy and equivalent (same age, no deletion-cost annotations)..." to make the example's assumptions explicit.

### 3. pod-deletion-cost is described as "a single integer" — accurate but incomplete

**Section:** Use Case 6 (Condensing multiple signals)
**Severity:** Minor

> "The `pod-deletion-cost` annotation (KEP-2255) is a single integer, and if multiple controllers try to set it, they conflict."

This is correct. The annotation `controller.kubernetes.io/pod-deletion-cost` accepts an integer in the range [-2147483648, 2147483647]. It's worth noting that pod-deletion-cost is step 4 of 8 in the sort order — it's not the highest-priority signal. Unassigned pods, pending pods, and not-ready pods are all deleted before deletion-cost is even consulted. This matters because it means deletion-cost can't override the controller's preference for removing unhealthy pods first.

**Suggestion:** Consider adding a parenthetical: "...is a single integer (and is only consulted after pod phase and readiness checks, so it can't override the controller's preference for removing unhealthy pods first)."

### 4. Option A description of the current heuristic is slightly off

**Section:** Where Should the Intelligence Live? — Option A
**Severity:** Minor

> "instead of 'prefer pods on nodes with more co-located replicas' (spreading), use 'prefer pods on nodes with fewer total pods' (consolidation)"

The current heuristic counts *related pods* (all pods owned by any ReplicaSet with the same owner), not just co-located replicas of the same RS. The proposed change to "fewer total pods" is also ambiguous — does it mean fewer pods from this RS, fewer related pods, or fewer total pods of any kind on the node? The KEP PR commit message says "prefer nodes with fewer active pods," which suggests total active pods on the node.

**Suggestion:** Be more precise: "instead of 'prefer pods on nodes with more related pods from the same owner' (spreading), use 'prefer pods on nodes with fewer total active pods' (consolidation)." This matches the KEP language and avoids ambiguity.

### 5. KEP 4563 ownership attribution is correct but could be clearer

**Section:** How to Participate
**Severity:** Nitpick

> "**sig-node** — owns the Eviction API (KEP 4563)"

This is correct — KEP 4563 (EvictionRequest API) is owned by sig-node with participating SIGs including sig-apps, sig-autoscaling, sig-cli, and sig-scheduling. No change needed, but noting for completeness that the participating SIGs list is broad.

### 6. "wg-serving" reference could use more context

**Section:** Use Case 2 (Load-aware scale-in)
**Severity:** Minor

> "The serving community has identified a need to bias scale-down toward replicas that an upstream traffic shaper is already draining"

For readers unfamiliar with Kubernetes working groups, "wg-serving" appears without introduction. The parenthetical "(wg-serving)" in the heading helps, but a first mention like "the Kubernetes Working Group for Serving (wg-serving)" would be clearer for the target audience of people "unfamiliar with Karpenter."

**Suggestion:** Expand the first reference to wg-serving.

### 7. The "30–80% reduction" benchmark claim lacks context

**Section:** Use Case 1 (Node consolidation)
**Severity:** Moderate

> "Early benchmarks from the Karpenter Pod Deletion Cost Controller RFC show 30–80% reduction in unnecessary disruptions, depending on deployment patterns."

This is a strong quantitative claim attributed to the RFC. Readers will want to know: reduction compared to what baseline? What deployment patterns produce 30% vs 80%? Is this from simulation, production data, or synthetic benchmarks? Without this context, the range is so wide (30–80%) that it's hard to evaluate.

**Suggestion:** Either add a sentence describing the benchmark methodology (e.g., "in simulated scale-down events across N-node clusters with uniform deployments") or soften to "initial analysis suggests significant reduction in unnecessary disruptions." If the RFC has specific numbers, link to the relevant section.

### 8. The timing diagram could be more explicit about the coordination gap

**Section:** Why this is hard to fix after the fact
**Severity:** Minor

The 5-step sequence is clear, but step 2 says "RS controller immediately selects pods to delete using its built-in heuristic" — the word "immediately" is key but easy to miss. The core insight is that the RS controller acts *synchronously* within the same reconciliation loop, with no hook for external input. Making this more explicit would strengthen the argument.

**Suggestion:** Consider: "RS controller *synchronously* selects pods to delete using its built-in heuristic — there is no extension point or callback for external systems to influence this decision."

### 9. Option B limitation about timing deserves more detail

**Section:** Where Should the Intelligence Live? — Option B
**Severity:** Minor

> "Annotations must be set before scale-down starts — timing is tricky"

This is the crux of the practical challenge with Option B but it's stated very briefly. The issue is that Karpenter must predict or react to HPA decisions and update annotations *before* the RS controller's next reconciliation loop processes the replica count change. This is a race condition. Expanding on this would help readers understand why Option B, while workable, has inherent limitations.

**Suggestion:** Add a sentence: "Karpenter must update annotations before the RS controller processes the replica count change — a race that requires either predictive annotation updates or a watch on HPA status."

### 10. Missing mention of StatefulSet and Job controllers

**Section:** Option A limitations
**Severity:** Noted (already mentioned, but worth emphasizing)

> "Only affects ReplicaSet-managed pods (not StatefulSets, Jobs, or custom controllers)"

This is correctly stated. However, the problem statement's opening focuses entirely on ReplicaSet behavior, which might lead readers to think the problem only exists for Deployments. StatefulSets have their own ordered deletion semantics, and Jobs have different lifecycle concerns. A brief note in the problem section acknowledging that this document focuses on RS-managed workloads (the most common case) would set scope expectations early.

### 11. Links and references appear correct

**Section:** Related Work, all links
**Severity:** Verified

All referenced KEPs, issues, and PRs exist and are correctly attributed:
- kubernetes/enhancements#5982 — RS controller consolidation (exists, sig-apps, alpha stage)
- KEP-2255 — Pod Deletion Cost (exists, sig-apps)
- KEP-2185 — Random Pod Selection (exists, sig-apps)
- KEP 4563 — Eviction Request API (exists, sig-node, alpha in v1.36)
- kubernetes#107598 — configurable scale-down behavior (exists, open)
- kubernetes#123541 — load-aware scale-in (exists)
- Karpenter PR #2935 — Pod Deletion Cost Controller RFC (referenced correctly)
- Karpenter PR #2842 — consolidation override proposal (referenced correctly)

### 12. The call for use cases is clear and well-targeted

**Section:** Call for Use Cases
**Severity:** Positive (no change needed)

The questions are specific enough to elicit useful responses without being leading. The framing "even partial descriptions help" lowers the barrier to participation. This section works well.

### 13. "These aren't mutually exclusive" section is well-balanced

**Section:** Where Should the Intelligence Live? — combined approach
**Severity:** Positive (no change needed)

The three options are presented fairly with honest strengths and limitations. The suggested sequencing (B → A → C) is logical and well-justified. The ask for community input on sequencing is appropriate.

---

## Overall Assessment

The document is strong. It's accessible to readers outside the Karpenter community, the technical claims are substantively accurate, and the design options are fairly presented. The main areas for improvement are:

1. **Precision on the RS controller sort order** (findings 1, 2, 4) — the spreading behavior is described correctly at a high level but the mechanism details could be tightened
2. **The benchmark claim** (finding 7) — needs methodology context or softening
3. **Option B timing challenge** (finding 9) — deserves a bit more detail given it's the short-term path

None of these are factual errors — they're opportunities to make an already-good document more precise.
