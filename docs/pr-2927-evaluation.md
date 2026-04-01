# PR #2927 Evaluation: CFS-Based Disruption Method Scheduling

## Problem Summary

Karpenter's disruption controller runs five methods sequentially in priority order (Emptiness → StaticDrift → Drift → MultiNodeConsolidation → SingleNodeConsolidation). When any method succeeds, the loop restarts from the top. This means continuous drift work (e.g., frequent AMI updates) starves consolidation indefinitely — drift processes one node per call, restarts the loop, and consolidation never gets a turn.

Users with large clusters and frequent AMI changes see consolidation paused for 20+ minutes while drift processes nodes one-by-one at ~6 nodes/minute.

## Root Cause Analysis

Three fundamental issues in the current architecture create this problem:

### 1. Restart-on-success loop design

The core loop in `controller.go` Reconcile method iterates methods sequentially and returns `RequeueImmediately` on the first success. This is a strict priority queue — lower-priority methods only run when all higher-priority methods have zero work. With continuous drift, consolidation is mathematically impossible to reach.

### 2. Drift's single-node-per-call throughput

`Drift.ComputeCommands()` returns exactly one command (one node) per invocation. Unlike Emptiness/StaticDrift which batch, drift must call `SimulateScheduling` per candidate to verify pod reschedulability. Combined with the restart-on-success loop, drift gets at most 1 node per 10s polling cycle.

### 3. Globally coupled disruption budgets

`BuildDisruptionBudgetMapping` counts ALL `MarkedForDeletion()` nodes as disrupting regardless of reason. Drift consuming budget directly reduces consolidation's available budget, creating implicit coupling between independent disruption goals.

## Evaluation of Proposed Solution (CFS-Based Scheduling)

### Design Summary

Split the loop into two phases:
- **Phase 1**: Emptiness and StaticDrift always run (fast, batch operations)
- **Phase 2**: CFS scheduler picks among Drift, MultiNode, SingleNode based on `druntime` (disruption runtime consumed, weighted by priority)

Drift gets a time-capped loop (`disruptWithTimeCap`) to process multiple nodes per turn. Budget mappings computed once per cycle and shared.

### Strengths

1. **Mathematically guarantees consolidation gets turns.** The druntime mechanism ensures the lowest-runtime method always gets picked next — consolidation cannot be starved.
2. **Adapts to cluster conditions.** When a method exhausts candidates, it leaves the eligible set. When methods finish fast, they get more turns. CFS self-corrects between typical and worst-case execution profiles.
3. **Time-capped drift solves throughput.** Instead of 1 node per 10s cycle, drift can process multiple nodes within its time budget (~30 nodes in 10 minutes at weight 3).
4. **Budget computation optimization.** Computing `BuildDisruptionBudgetMapping` once per distinct reason (Drifted, Underutilized) instead of per-method reduces API calls.
5. **Wakeup logic prevents idle monopolization.** Inactive methods don't block others; on reentry they get `max(current_druntime, min(all_druntimes))` — fair but not bursty.

### Weaknesses

1. **Complexity.** CFS is well-understood in OS scheduling but novel in a Kubernetes controller. The druntime normalization, wakeup logic, and eligibility gating add significant state management to what is currently a simple sequential loop.
2. **Weight tuning is opaque.** The 3:2:1 weight ratio (Drift:Multi:Single) is presented without empirical justification. Different cluster profiles may need different ratios, but there's no mechanism for users to tune this.
3. **Charging failed evaluations equally.** A SingleNode scan that times out at 180s gets charged the same druntime as one that finds work in 5s. While the RFC argues this is correct (controller time was consumed), it means a cluster where SingleNode consistently times out will see it get very few turns — potentially fewer than needed to ever find a valid consolidation.
4. **Phase 1/Phase 2 boundary is rigid.** Emptiness and StaticDrift always run first unconditionally. If these become expensive in the future, there's no mechanism to include them in CFS scheduling.
5. **No user-facing controls.** Operators cannot adjust the drift-vs-consolidation balance for their specific workload patterns.

## Alternative: SLO-Based Drift Budget with Consolidation Priority

### A. Drift SLO Annotation

A `karpenter.sh/drift-slo: 7d` annotation on NodePool defines the time window to complete all drifts. Karpenter dynamically computes drift throughput:

```
drift_budget_share = remaining_drifted_nodes / (remaining_time * total_budget)
```

- Early in the window: drift gets minimal budget, consolidation runs freely
- Near deadline: drift dominates the budget
- No need to track druntime or implement a scheduler — budget allocation IS the scheduling mechanism

**Advantages over CFS:**
- User-visible, declarative control (SLO is a concept operators understand)
- Self-tuning: budget allocation adapts to actual drift volume and remaining time
- Simpler implementation: no per-method state, no normalization, no wakeup logic
- Decouples drift and consolidation entirely — they don't compete for "turns," they compete for budget

**Disadvantages:**
- Requires a new API field on NodePool
- Doesn't address the time-starvation problem (SingleNode's 3-minute timeout still blocks the loop)
- Needs a fallback for when no SLO is set

### B. Prioritize Drifted Nodes in Consolidation Candidate Selection

When consolidation evaluates candidates, sort drift-marked nodes higher in the candidate list. This means:

- Consolidation naturally picks drifted underutilized nodes first
- One disruption serves both drift and consolidation purposes
- Validation scope is simpler because consolidation doesn't need to track drift state changes

Currently in `consolidation.sortCandidates()`, candidates are sorted by `DisruptionCost`. Adding a secondary sort key that prioritizes `NodeClaim.StatusConditions().Get("Drifted").IsTrue()` nodes would be a minimal change (~5 lines) with significant impact.

**This is the highest-value, lowest-complexity change.** It doesn't solve the starvation problem directly, but it reduces the practical impact: if consolidation does get a turn, it preferentially removes drifted nodes, achieving drift progress as a side effect.

## How Drift-Priority Reduces the Validation Problem

The current validation flow in `validation.go` re-runs `GetCandidates`, `BuildDisruptionBudgetMapping`, and `SimulateScheduling` to verify a consolidation decision is still valid. When drift state changes between computation and validation (because drift disrupted a node), consolidation's validation fails — the cluster state shifted.

With drift-priority in consolidation:
- Consolidation targets the same nodes drift would target
- If consolidation removes a drifted node, drift has less work → fewer state changes during validation windows
- The two methods converge on the same candidates rather than competing, reducing the window for validation invalidation

## Comparison

| Dimension | CFS Scheduling (PR #2927) | SLO-Based + Drift Priority |
|-----------|--------------------------|---------------------------|
| **Complexity** | High — new scheduler, per-method state, normalization | Low-Medium — SLO field + sort key change |
| **Starvation guarantee** | Yes, mathematical | Partial — SLO controls budget but doesn't fix time-blocking |
| **User control** | None (hardcoded weights) | Yes (SLO annotation is declarative) |
| **Drift throughput** | Improved via time-capped loop | Improved via dynamic budget allocation |
| **Consolidation during drift** | Guaranteed turns via CFS | Guaranteed budget via SLO; drift-priority reduces wasted turns |
| **Validation failures** | Not addressed | Reduced by candidate convergence |
| **API changes** | None | New annotation on NodePool |
| **Operational reasoning** | Requires understanding CFS semantics | SLO is intuitive ("finish drift in 7 days") |

## Recommendation

The CFS approach in PR #2927 is a well-engineered solution to a real problem, but it introduces significant complexity for what is fundamentally a resource allocation problem. The SLO-based alternative is simpler, more operator-friendly, and addresses the root cause (budget allocation) rather than the symptom (loop scheduling).

The highest-impact immediate change would be **drift-priority in consolidation candidate selection** — it's a minimal code change that makes consolidation and drift cooperative rather than competitive. Combined with an SLO-based drift budget, this would address the core user complaints without requiring a CFS scheduler.

If the CFS approach is pursued, the weight ratio should be configurable (at minimum via a feature flag or NodePool annotation) rather than hardcoded, and the charging-on-failure behavior should be reconsidered for methods with long timeouts.
