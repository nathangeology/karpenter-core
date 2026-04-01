# Evaluation: PR #2930 — HTB-Based Disruption Budget Model

## Problem Summary

Karpenter's disruption budgets do not behave the way their configuration suggests. Users configure per-reason budgets (e.g., "10% for drift, 5% for consolidation") expecting independent pools, but the runtime uses a single shared disrupting counter across all reasons. When drift consumes 10 of 15 allowed disruptions, consolidation sees only 5 remaining — even if its configured budget is 5%. The YAML looks like a partition but behaves like a shared pool with soft hints.

This creates two user-facing problems:
1. Per-reason budgets are not independent — drift consumption directly reduces consolidation's effective budget
2. Budgets do not add up to a whole — "10% for drift" + "5% for consolidation" does not partition 15%

## Root Cause Analysis

The root cause is architectural: a mismatch between the configuration model (per-reason budgets suggesting independence) and the runtime model (a single global counter).

Specifically in the codebase:

1. **Global disrupting counter**: `BuildDisruptionBudgetMapping` (helpers.go:230) counts ALL `MarkedForDeletion()` nodes as `disrupting` regardless of why they were marked. There is no per-reason tracking of in-flight disruptions.

2. **Per-reason budget is filter-only**: `GetAllowedDisruptionsByReason` (nodepool.go:379) computes the minimum `allowedDisruptions` across matching budgets, but the subtraction `allowedDisruptions - disrupting` uses the global count. The per-reason budget only selects which ceiling applies — it never isolates the deduction.

3. **No reason on StateNode**: `MarkedForDeletion()` is a boolean. The disruption reason is written to the NodeClaim status condition (`ConditionTypeDisruptionReason` at queue.go:268) but is not tracked in the in-memory `StateNode`, so the budget computation cannot distinguish drift disruptions from consolidation disruptions.

4. **Drift and consolidation compete for the same budget**: Because the disruption controller processes methods sequentially (drift before consolidation), drift can exhaust the shared budget before consolidation ever runs, effectively starving cost optimization.

## Evaluation of Proposed Solution (HTB Model)

### Strengths

- **Fixes the core semantic gap**: Budgets would mean what users write. Each reason gets a guaranteed minimum (`rate`) and can borrow unused capacity from an excess pool up to a ceiling.
- **Work-conserving**: Unused budget from idle reasons flows to active ones — no capacity is wasted.
- **Backward compatible**: Existing configs with only a catch-all budget behave identically. HTB semantics activate only when per-reason budgets are present.
- **Extensible to cluster-wide scope**: The hierarchy naturally extends to cluster > NodePool > reason, enabling cross-NodePool budget coordination via a future DisruptionBudget CRD.
- **Well-understood model**: HTB is battle-tested in Linux traffic control (`tc-htb`). The mapping to disruption budgets is clean.

### Weaknesses

- **Significant implementation complexity**: Requires per-reason disrupting counts in StateNode, changes to MarkForDeletion, new HTB computation logic in BuildDisruptionBudgetMapping, and a new CRD for the full vision.
- **Excess pool fairness is undefined**: First-come-first-served allocation in a single-threaded controller means whichever method runs first in a cycle gets priority access to the excess pool. This could still starve consolidation if drift always runs first.
- **New CRD adds API surface**: The standalone DisruptionBudget CRD adds operational complexity — users must manage a separate resource and wire references.
- **Does not address the user's actual goal**: Users don't want to manage budget partitions — they want drift to complete on time without blocking cost savings. HTB gives precise control over the mechanism but doesn't directly express the desired outcome.
- **Open questions remain unresolved**: Multi-reason budgets, per-reason ceilings, and fairness allocation are acknowledged but not solved.

## Evaluation of SLO-Based Alternative

The SLO-based alternative (proposed by nathangeology in the PR discussion) takes a goal-oriented approach with two components:

### A. Drift SLO Annotation on NodePool

A `karpenter.sh/drift-slo: 7d` annotation defines the time window to complete all drifts. Karpenter computes required drift throughput dynamically:

```
drift_rate = remaining_drifted_nodes / remaining_time
```

Early in the window, drift gets minimal budget and consolidation runs freely. As the deadline approaches, drift claims more budget. This is adaptive — no manual tuning of per-reason percentages.

**Strengths:**
- Expresses the user's actual intent (complete drift within N time) rather than a mechanism (reserve X% of budget)
- Self-tuning: budget allocation adjusts automatically based on progress and remaining time
- Simpler interface: one annotation vs. multi-level budget hierarchies
- Naturally handles mid-drift annotation changes (raise/lower the SLO, drift speeds up/slows down)

**Weaknesses:**
- Requires tracking drift progress over time (remaining drifted nodes, elapsed time)
- Edge case: what happens when disruption limits prevent achieving the SLO? (Likely: emit warning, complete as fast as possible)
- Doesn't generalize to other disruption reasons — it's drift-specific
- Defining "drift action" boundaries needs thought (per-node? per-batch?)

### B. Prefer Drifted Nodes in Consolidation Candidate Sorting

When evaluating single/multi-node consolidation candidates, sort nodes pending drift higher in the candidate list. This makes consolidation naturally remove drifted nodes first — achieving drift progress as a side effect of cost optimization.

**Strengths:**
- Elegant synergy: one disruption serves two purposes (cost savings + drift progress)
- Reduces total disruption count — fewer separate actions needed
- Minimal implementation: modify `sortCandidates` in consolidation.go to boost drifted nodes
- No new API surface or CRD needed

**Weaknesses:**
- Only helps when drifted nodes are also consolidation candidates (underutilized). If drifted nodes are fully utilized, consolidation won't touch them.
- Doesn't guarantee drift completion — it's opportunistic, not deterministic.

## Comparison

| Dimension | HTB Model | SLO-Based Alternative |
|-----------|-----------|----------------------|
| **User interface** | Complex: multi-level budget hierarchy, new CRD | Simple: one annotation + sorting change |
| **Expresses intent** | Mechanism (reserve X% per reason) | Outcome (complete drift in N time) |
| **Implementation scope** | Large: per-reason tracking, HTB computation, new CRD | Medium: drift progress tracking, candidate sort tweak |
| **Generality** | General: works for any disruption reason | Drift-specific (but drift is the primary pain point) |
| **Drift/consolidation synergy** | None: they remain independent consumers | Yes: consolidation preferentially removes drifted nodes |
| **Self-tuning** | No: user must manually set percentages | Yes: adapts based on progress vs. deadline |
| **Total disruptions** | Same or more (drift and consolidation act independently) | Fewer (combined actions reduce total disruptions) |
| **Backward compatibility** | Good (opt-in via new CRD) | Good (opt-in via annotation) |
| **Operational complexity** | Higher (new CRD, budget math to understand) | Lower (one knob, intuitive SLO semantics) |

## Recommendation

The SLO-based alternative is simpler, more aligned with user intent, and more operationally sound for the primary use case (drift competing with consolidation). It should be pursued first:

1. **Phase 1**: Modify consolidation candidate sorting to prefer drifted nodes. This is low-risk, low-effort, and immediately reduces the drift-vs-consolidation conflict.

2. **Phase 2**: Implement the drift SLO annotation with adaptive rate limiting. This gives users direct control over drift completion timelines without manual budget partitioning.

3. **Phase 3 (if needed)**: If users demonstrate a need for precise per-reason budget isolation beyond drift, the HTB model remains a valid future option. The SLO work does not preclude it.

The HTB model solves a real problem (budget semantics mismatch) but does so by giving users more knobs to turn. The SLO approach solves the same problem by aligning the system's behavior with what users actually want: timely drift completion without sacrificing cost optimization. Simpler interfaces that express outcomes rather than mechanisms are generally more operationally sound.
