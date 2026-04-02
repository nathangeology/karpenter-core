# Deep Review: PR #2930 — HTB Disruption Budget RFC

## Strengths (What to Keep)

1. **Correct problem diagnosis.** The RFC precisely identifies the core semantic gap: per-reason budgets share a single global disrupting counter, so `{nodes: "10%", reasons: [Drifted]}` and `{nodes: "5%", reasons: [Underutilized]}` look like independent pools but behave as soft hints on a shared pool. This is well-articulated with concrete code references (`BuildDisruptionBudgetMapping`, `MarkedForDeletion()`).

2. **Clean conceptual mapping.** The HTB-to-disruption-budget mapping table is intuitive: `rate` → guaranteed minimum, `ceil` → global cap, parent → catch-all budget, surplus → excess pool. The analogy to Linux `tc-htb` is apt and gives the model a well-understood theoretical foundation.

3. **Work-conserving design.** Unused budget from idle reasons flows to active ones rather than being wasted. This is the right default — strict isolation wastes capacity when one reason has no work.

4. **Backward compatibility via opt-in CRD.** Existing inline budgets keep current behavior. HTB activates only when a `DisruptionBudget` CR is referenced. No silent semantic changes. This is the correct migration strategy.

5. **Walkthroughs are effective.** The "No Excess Pool" and "With Excess Pool" scenario tables make the model concrete and verifiable. They demonstrate the key properties clearly.

6. **Extensibility to cluster-wide scope.** The hierarchy naturally extends to cluster > NodePool > reason, which is a real gap in the current model.

## Complexity Concerns (What to Simplify)

1. **The DisruptionBudget CRD is premature.** The RFC proposes a new CRD before validating that per-reason budget isolation is what users actually need. A new CRD adds API surface, operational complexity (separate resource lifecycle, reference wiring, RBAC), and maintenance burden. The per-reason HTB computation could be implemented within the existing NodePool budget spec first — the YAML mapping section of the RFC itself shows this works without a new CRD.

2. **Multi-level hierarchy is speculative.** The cluster > NodePool > reason hierarchy is presented as a natural extension, but no user stories or concrete demand are cited. Building a three-level HTB tree adds significant implementation complexity (cross-NodePool state coordination, hierarchical budget recomputation) for a use case that may not exist yet.

3. **Excess pool fairness is hand-waved.** Open Question #1 acknowledges the excess pool is first-come-first-served in the single-threaded controller. This is not a minor detail — it means whichever disruption method runs first in a cycle gets priority access to borrowed capacity. Since drift runs before consolidation today, HTB with an excess pool still favors drift over consolidation. The RFC does not propose a solution.

4. **Three open questions are load-bearing.** Multi-reason budgets (Q2), per-reason ceilings (Q3), and fairness (Q1) are not edge cases — they determine the actual behavior of the system. An RFC with unresolved questions on core semantics is not ready for implementation.

5. **The `ceil` parameter is underspecified.** The RFC sets `ceil = global_cap` for all reasons, making it redundant. If `ceil` is always equal to the parent rate, it adds no value. If per-reason ceilings are needed (Q3), the API must expose them — but the RFC defers this decision.

## Simpler Alternatives for Each Major Feature

### Per-reason budget isolation → Drift-priority consolidation sorting

The primary pain point is drift starving consolidation. Rather than building a full HTB budget allocator, modify consolidation's candidate sorting to prefer drifted nodes. One disruption then serves both drift and consolidation purposes, reducing total disruptions rather than just partitioning them differently. This is ~5 lines of code vs. a new budget computation engine.

### Guaranteed minimum budget per reason → SLO-based drift annotation

Users don't want to manually partition budget percentages — they want drift to complete on time without blocking cost savings. A `karpenter.sh/drift-slo: 7d` annotation lets Karpenter dynamically compute the drift budget share based on remaining work and remaining time. Early in the window, consolidation runs freely. Near the deadline, drift dominates. This is self-tuning where HTB requires manual percentage tuning.

### Cluster-wide disruption cap → Defer entirely

No concrete user demand is cited. The current per-NodePool model works. If cluster-wide caps are needed later, they can be added independently of per-reason budget isolation. Bundling them into the same RFC increases scope without increasing value.

### Excess pool / work-conserving behavior → Not needed with cooperation

If consolidation preferentially removes drifted nodes, drift and consolidation are no longer competing consumers of a shared budget. They converge on the same candidates. The need for an excess pool mechanism disappears because the two primary reasons are cooperative rather than adversarial.

## Problem Statement Assessment

The problem is stated clearly and the root cause is correctly identified: the shared disrupting counter in `BuildDisruptionBudgetMapping` creates a semantic gap between configuration and runtime behavior. The RFC does a good job distinguishing the symptom (consolidation starvation during drift) from the cause (global counter shared across reasons).

However, the RFC conflates two distinct problems:

1. **Budget semantics mismatch** — per-reason budgets don't behave independently (the actual bug)
2. **Lack of cluster-wide budget coordination** — NodePools are budget islands (a feature request)

These should be separate proposals. Solving #1 does not require solving #2, and bundling them increases the RFC's scope and complexity without proportional benefit.

The failure modes are well-characterized for the simple cases (the walkthrough tables), but the RFC does not characterize failure modes for the excess pool under contention, for multi-reason budgets, or for the interaction between HTB and the controller's sequential method execution order.

## Missing Alternatives or Tradeoffs

1. **No comparison with simpler approaches.** The RFC does not consider whether the problem can be solved without per-reason budget isolation at all. The SLO-based approach and drift-priority sorting (proposed by nathangeology in the PR discussion) are not evaluated as alternatives. An RFC should demonstrate why the proposed approach was chosen over simpler options.

2. **No analysis of the controller execution order interaction.** HTB fixes budget allocation but does not address the restart-on-success loop that causes consolidation starvation. Even with independent budgets, if drift always runs first and restarts the loop on success, consolidation may still not get turns. The RFC assumes budget isolation is sufficient but does not prove it.

3. **No cost-benefit analysis.** The implementation requires: per-reason tracking on StateNode, changes to MarkForDeletion, new HTB computation in BuildDisruptionBudgetMapping, a new CRD with webhook validation, controller changes to consume the CRD, and documentation. This is a large change. The RFC does not compare this cost against simpler alternatives that achieve 80% of the value.

4. **No discussion of drift-consolidation cooperation.** The RFC treats drift and consolidation as independent consumers that need isolated budgets. It does not consider that a drifted underutilized node can be handled by a single consolidation action — one disruption serving both purposes. This cooperation-based approach reduces total disruptions rather than just partitioning them.

5. **GnatorX's own PR #2927 (CFS scheduling) is not cross-referenced.** The two PRs address overlapping problems (drift-consolidation contention) with different mechanisms (budget allocation vs. method scheduling). The RFC should discuss how HTB interacts with or replaces CFS scheduling.

## Specific Suggestions for Improving the RFC

1. **Split the RFC into two proposals**: (a) per-reason budget isolation within a NodePool (the core fix), and (b) cluster-wide budget coordination via a new CRD (a separate feature). Ship (a) first within the existing API.

2. **Resolve the three open questions before seeking approval.** Multi-reason budget mapping, per-reason ceilings, and excess pool fairness are not optional — they determine the system's behavior under real workloads.

3. **Add an alternatives section** comparing HTB against: (a) drift-priority consolidation sorting, (b) SLO-based drift annotation, (c) simple per-reason counter isolation without borrowing. Explain why HTB's additional complexity (excess pool, borrowing, ceilings) is justified over these simpler approaches.

4. **Address the controller execution order.** Explain whether HTB alone is sufficient to prevent consolidation starvation, or whether it must be combined with changes to the disruption controller's method scheduling (e.g., PR #2927's CFS approach).

5. **Add failure mode analysis for the excess pool.** What happens when both drift and consolidation want to borrow simultaneously? What happens when the excess pool is exhausted? How does the first-come-first-served allocation interact with the controller's sequential method execution?

6. **Consider whether the DisruptionBudget CRD can be deferred.** The per-reason HTB computation can be implemented within the existing NodePool budget spec. The CRD adds value only for cluster-wide scope, which is a separate concern. Shipping the computation change first validates the model before committing to a new API resource.

## Summary Assessment

The HTB RFC correctly identifies a real problem (budget semantics mismatch) and proposes a theoretically sound solution (hierarchical token bucket allocation). The problem diagnosis is the strongest part of the document. The HTB model itself is well-explained and the walkthroughs are effective.

However, the RFC overbuilds for the problem at hand. It proposes a new CRD, a multi-level hierarchy, and a borrowing mechanism when the primary user pain (drift starving consolidation) can be addressed more simply through drift-consolidation cooperation (sorting change) and outcome-based controls (drift SLO annotation). The RFC solves the mechanism ("reserve X% per reason") rather than the outcome ("complete drift in N days without blocking cost savings").

The three unresolved open questions (fairness, multi-reason mapping, per-reason ceilings) are not edge cases — they are core design decisions that determine real-world behavior. The RFC is not ready for implementation until these are resolved.

Recommendation: The valuable insight from this RFC (per-reason budgets should mean what they say) should be preserved, but the implementation path should start with simpler cooperative approaches (drift-priority sorting + SLO annotation) before committing to the full HTB machinery. If those prove insufficient, the HTB model remains a valid escalation path — but it should be validated incrementally, not shipped as a monolithic change with a new CRD.
