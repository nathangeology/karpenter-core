# Coordinating Workload Scale-In with Node Autoscaling: Use Cases and Community Discussion

**Authors:** Nathaniel Jones (@nathangeology)
**Status:** Community Discussion Draft (v2)
**Date:** 2026-04-21
**Related:** [kubernetes/enhancements#5982](https://github.com/kubernetes/enhancements/issues/5982), [kubernetes/kubernetes#107598](https://github.com/kubernetes/kubernetes/issues/107598), [kubernetes/kubernetes#123541](https://github.com/kubernetes/kubernetes/issues/123541), [Karpenter Pod Deletion Cost Controller RFC](https://github.com/kubernetes-sigs/karpenter/pull/2935)

---

## The Problem

When Kubernetes workloads scale down, the ReplicaSet controller picks which pods to delete. Today it spreads deletions evenly across nodes — removing a few pods from every node rather than concentrating removals to empty out specific nodes. This works against node autoscalers like Karpenter and cluster-autoscaler, which need empty nodes to remove.

The result: after a scale-down event, every node is still partially occupied. The autoscaler then has to evict more pods to consolidate, causing disruption that the original scale-down could have avoided.

This is a coordination gap between workload controllers and infrastructure controllers. The workload controller doesn't know which nodes the autoscaler wants to drain. The autoscaler can't influence which pods the workload controller deletes. Both are doing reasonable things independently, but the combined result is worse than either would produce alone.

### A concrete example

5 nodes, each running 4 pods from one Deployment (20 replicas). HPA scales down to 10.

**Today:** The RS controller removes 2 pods from each node. All 5 nodes still have 2 pods. No node is empty. The autoscaler must evict more pods to consolidate — more disruption on top of the 10 already deleted.

**With coordination:** Remove all 4 pods from 2 nodes, plus 2 from a third. Same 10 deletions, but 2 nodes are now empty and can be removed immediately. Zero additional disruption.

At scale — hundreds of nodes, frequent HPA-driven scaling, diurnal traffic patterns — this gap creates a steady state where nodes are uniformly partially occupied. That's the worst case for cost efficiency.

### Why this is hard to fix after the fact

The timing matters. Scale-in happens first, then the autoscaler reacts:

1. HPA decides replicas should decrease
2. RS controller immediately selects pods to delete using its built-in heuristic
3. Pods are terminated
4. Autoscaler notices underutilized nodes
5. Autoscaler consolidates — evicting more pods to empty nodes it can remove

By step 4, the spreading has already happened. Improving step 5 (better consolidation) helps, but it can't undo the suboptimal deletions from step 2. To really fix this, we need to change what happens when pods are selected for deletion.

---

## Known Use Cases

We've identified several scenarios where smarter scale-in coordination would help. **We're actively looking for more — see the call for use cases below.**

### 1. Node consolidation for cost efficiency

The core case described above. When workloads scale down, concentrate pod deletions on nodes that are closest to empty so the autoscaler can remove them without additional evictions. Early benchmarks from the [Karpenter Pod Deletion Cost Controller RFC](https://github.com/kubernetes-sigs/karpenter/pull/2935) show 30–80% reduction in unnecessary disruptions, depending on deployment patterns.

### 2. Load-aware scale-in (wg-serving)

The serving community has identified a need to bias scale-down toward replicas that an upstream traffic shaper is already draining ([kubernetes#123541 comment](https://github.com/kubernetes/kubernetes/issues/123541#issuecomment-2127743851)). When a load balancer is steering traffic away from certain instances, those instances are the natural candidates for removal. This is load-aware rather than node-aware, but it touches the same pod deletion selection logic.

### 3. Node maintenance and drift

Nodes marked for drift, approaching expiration, or flagged for maintenance need replacement anyway. Scale-in events that preferentially drain these nodes help both consolidation and maintenance progress. Today, the RS controller has no awareness of node conditions when selecting pods for deletion.

### 4. Scheduler constraints and pod placement quality

Some pods are better candidates for removal than others based on scheduling constraints. A pod that barely fits its current node (or violates soft constraints) might be a better deletion candidate than one that's well-placed. The scheduler has this information, but the RS controller doesn't use it.

### 5. Co-located workloads with different disruption tolerances

Nodes often run a mix of workloads: long-running batch jobs, pods with `do-not-disrupt` annotations, stateful workloads. When choosing which replicas to remove, it makes sense to prefer nodes where the remaining workloads are easier to relocate — not nodes anchored by workloads that are expensive to move.

### 6. Condensing multiple signals into a single deletion priority

Several of these use cases point to the same idea: there are multiple reasons to prefer deleting a pod on one node over another. Node utilization, drift status, co-located workload sensitivity, load-balancer drain state — these are all inputs to a "which pod should go first?" decision. Today there's no good way to combine them. The `pod-deletion-cost` annotation (KEP-2255) is a single integer, and if multiple controllers try to set it, they conflict.

---

## Call for Use Cases

**We want to hear from you.** What scenarios do you have where smarter pod deletion selection during scale-in would help?

Some questions to consider:

- Do you run workloads where HPA-driven scale-down causes unnecessary pod disruption?
- Do you use `pod-deletion-cost` annotations today? If so, what sets them and why?
- Do you have workloads where certain replicas are clearly better candidates for removal (draining, idle, on underutilized nodes)?
- Do you run mixed workloads on shared nodes where some pods are much more expensive to disrupt than others?
- Are there scale-in scenarios specific to your domain (ML training, CI/CD, gaming, edge) that we should consider?

Please share your use cases in the discussion thread. Even partial descriptions help — we're trying to understand the full landscape before committing to a design.

---

## Where Should the Intelligence Live?

This is the key design question. There are several places where scale-in coordination logic could run, each with different tradeoffs.

### Option A: ReplicaSet controller — simple heuristics only

The RS controller already has a pod deletion sort order. We could change one step: instead of "prefer pods on nodes with more co-located replicas" (spreading), use "prefer pods on nodes with fewer total pods" (consolidation). This is behind a `ConsolidatingScaleDown` feature gate ([kubernetes/enhancements#5982](https://github.com/kubernetes/enhancements/issues/5982)).

**Strengths:**
- No external dependencies — uses information already in the informer cache
- Simple to understand and reason about
- Works for all ReplicaSet-managed workloads immediately
- Feature-gated, opt-in

**Limitations:**
- Only affects ReplicaSet-managed pods (not StatefulSets, Jobs, or custom controllers)
- Can't incorporate external signals (load, drift status, autoscaler intent)
- A single heuristic can't optimize for all use cases simultaneously

The RS controller should remain a simple consumer of signals, not a decision engine. It shouldn't take in autoscaler or scheduler preferences directly, and it shouldn't become a "smusher" of multiple priority signals. But it can read existing annotations and apply straightforward heuristics.

### Option B: Autoscaler (Karpenter) — reconcile multiple signals, set annotations

Karpenter already reasons about node utilization, drift, and consolidation. It's well-positioned to reconcile multiple inputs (node cost, workload sensitivity, drift status) and express the result as `pod-deletion-cost` annotations that the RS controller consumes.

**Strengths:**
- Can incorporate rich context: node utilization, drift, topology, workload mix
- Works with any controller that respects `pod-deletion-cost` (including custom controllers)
- Karpenter already has the information needed to make good decisions
- Short-term path: the [Karpenter Pod Deletion Cost Controller RFC](https://github.com/kubernetes-sigs/karpenter/pull/2935) implements this today

**Limitations:**
- Requires annotation management (API server write load that scales with pod count)
- Annotations must be set before scale-down starts — timing is tricky
- Couples the RS controller's behavior to an external system
- Only works when Karpenter (or a similar controller) is running

### Option C: Scheduler — advise on scale-in via scheduler library

There is an active effort to upstream Karpenter's provisioning intelligence into the kube-scheduler. If the scheduler gains awareness of node provisioning and bin-packing, it could also advise on scale-in — not just where to place new pods, but which existing pods are the best candidates for removal.

**Strengths:**
- The scheduler already knows about topology spread, pod affinity, and zone distribution
- Natural integration point as Karpenter upstreams into the scheduler
- Could provide a general-purpose "scale-in advisor" that any controller consults

**Limitations:**
- Longer timeline — depends on the Karpenter upstreaming effort
- Scheduling constraints don't currently consider node efficiency or cross-Deployment bin-packing
- Mechanism for the scheduler to advise on deletions doesn't exist yet (Eviction API interceptor? Extension point? Advisory annotation?)

### These aren't mutually exclusive

A likely path forward combines these approaches:

1. **Short term:** Karpenter sets `pod-deletion-cost` annotations (Option B) — works today, no upstream changes needed
2. **Medium term:** RS controller gets a simple consolidation heuristic (Option A) — helps even without Karpenter, feature-gated
3. **Long term:** Scheduler integration (Option C) — as Karpenter upstreams, the scheduler becomes the natural place for cross-workload scale-in intelligence

We'd like community input on this sequencing. Does this progression make sense? Should we prioritize differently?

---

## Related Work

- [kubernetes/enhancements#5982](https://github.com/kubernetes/enhancements/issues/5982) — RS controller consolidation heuristic proposal
- [KEP-2255 (Pod Deletion Cost)](https://github.com/kubernetes/enhancements/tree/master/keps/sig-apps/2255-pod-cost) — existing annotation mechanism
- [KEP-2185 (Random Pod Selection)](https://github.com/kubernetes/enhancements/tree/master/keps/sig-apps/2185-random-pod-select-on-replicaset-downscale) — randomized pod selection alternative
- [KEP 4563 (Eviction Request API)](https://github.com/kubernetes/enhancements/blob/master/keps/sig-node/4563-eviction-request-api/README.md) — eviction-layer coordination
- [Karpenter Pod Deletion Cost Controller RFC](https://github.com/kubernetes-sigs/karpenter/pull/2935) — Karpenter-side annotation management
- [kubernetes#107598](https://github.com/kubernetes/kubernetes/issues/107598) — configurable scale-down behavior discussion
- [kubernetes#123541](https://github.com/kubernetes/kubernetes/issues/123541) — load-aware scale-in from wg-serving
- [Karpenter consolidation override proposal](https://github.com/kubernetes-sigs/karpenter/pull/2842) — user input into consolidation decisions

---

## How to Participate

This problem sits at the intersection of several SIGs and working groups:

- **sig-apps** — owns the ReplicaSet controller and pod deletion logic
- **sig-autoscaling** — owns autoscaler behavior and the Karpenter integration
- **sig-node** — owns the Eviction API (KEP 4563)
- **sig-scheduling** — owns the scheduler and the Karpenter upstreaming effort
- **wg-serving** — has identified load-aware scale-in as a need

**Where to comment:**
- This document's discussion thread
- [kubernetes/enhancements#5982](https://github.com/kubernetes/enhancements/issues/5982) for the RS controller proposal specifically
- [Karpenter Pod Deletion Cost Controller RFC](https://github.com/kubernetes-sigs/karpenter/pull/2935) for the Karpenter-side approach
- [#karpenter](https://kubernetes.slack.com/archives/C02SFFZSA2K) and [#karpenter-dev](https://kubernetes.slack.com/archives/C04JW2J5J5P) on Kubernetes Slack

**What we're looking for:**
- Use cases we haven't considered
- Feedback on where the intelligence should live (RS controller, autoscaler, scheduler, or a combination)
- Experience with `pod-deletion-cost` annotations in production
- Perspectives from operators running at scale with frequent scaling events
- Input on sequencing: what should we build first?

We want to get this right, and getting it right means hearing from the people who run these systems in production. Your input shapes the design.
