# PR #2935 RFC Feedback Response

Evaluation of co-worker review feedback on the Pod Deletion Cost Controller RFC.

Status key: ✅ Already fixed | 🔧 Will fix | 📌 Defer | ❌ Disagree

---

## 1. TODO on RS controller alternative
**Status: ✅ Already fixed**

The RFC now contains a full "Changing the ReplicaSet Controller Behavior Directly" section under Alternatives Considered. It covers the short-term annotation approach (this RFC), the long-term KEP #5982 approach, and the relationship between them including a deprecation path. The TODO has been replaced with substantive analysis.

## 2. Default strategy contradicts recommendation
**Status: ✅ Already fixed**

The configuration table now shows `PodCount` as the default. The RFC text recommends PodCount and the default matches. The Random-as-default inconsistency is resolved.

## 3. Too many ranking strategies for alpha
**Status: 🔧 Will fix**

Valid concern. Four strategies plus Random is over-scoped for alpha. The RFC argues convincingly that PodCount is the right default because it mirrors Karpenter's consolidation ranking.

**Proposed change:** Trim alpha to two strategies: `PodCount` (default) and `Random` (baseline for benchmarking). Move `UnallocatedVCPUPerPodCost`, `LargestToSmallest`, and `SmallestToLargest` to a "Future strategies" subsection, noting they can be added in beta with motivating examples. Update the configuration table and ranking strategies section accordingly.

## 4. 30-80% claim has no linked evidence
**Status: 🔧 Will fix**

The RFC says "30-80% in benchmarks" in the Summary, Motivation/Evidence, and Goals sections but never links methodology, workload shapes, or artifacts.

**Proposed change:** Soften the Summary claim to: "we observe significant reductions in voluntary pod disruption in simulations, with gains varying by cluster topology and workload shape." In the Evidence section, add a sentence: "Benchmark methodology and results will be published alongside the implementation PR. Preliminary simulations tested clusters of 10-100 nodes with 5-50 pods/node across uniform and skewed workload distributions." Remove the specific "30-80%" from Goals and replace with "Measurably reduce voluntary pod disruption rate, validated by published benchmarks."

## 5. Example only shows best case
**Status: 🔧 Will fix**

The 3-node/9-pod example scales from 9→6, which perfectly empties one node. Real scale-downs rarely align this cleanly.

**Proposed change:** Add a partial-drain example after the existing one:

> **Partial-drain case:** Same cluster, scale from 9 to 7. The ReplicaSet controller removes 2 pods from Node A (lowest cost). Node A still has 1 pod — not empty yet. But Node A is now the least-occupied node, so Karpenter's consolidation ranking keeps it as the top target. On the next scale-down event (9→7→5, or a subsequent HPA adjustment), the remaining pod on Node A is removed first. Convergence takes multiple scale-down events rather than one, but each event moves the system in the right direction. Without the controller, the 2 deletions would spread across nodes, and no node would be closer to empty.

## 6. Motivation conflates two problems
**Status: 🔧 Will fix**

The motivation paragraph mixes (1) disruption from consolidation itself with (2) the coordination gap that prevents consolidation from finding empty nodes. The RFC only solves (2).

**Proposed change:** Split the motivation into two clearly labeled paragraphs:

> **Disruption from consolidation (context, not solved here):** When Karpenter consolidates nodes, it disrupts running pods. [existing text about costs of disruption]. Reducing this disruption directly is addressed by other proposals (consolidation cost thresholding, node size limiting).
>
> **Coordination gap (solved here):** The ReplicaSet controller and Karpenter's consolidation controller share no information about each other's intent. [existing text about spreading heuristic and entropy]. This RFC closes that coordination gap.

## 7. ConsolidateWhenEmpty vs WhenUnderutilized
**Status: 🔧 Will fix**

The motivation leads with WhenEmpty but most production clusters use WhenUnderutilized. The value proposition differs and the RFC should be upfront about it.

**Proposed change:** Add a paragraph after the coordination gap section:

> **Value by consolidation policy:** For `ConsolidateWhenEmpty`, the benefit is direct — concentrating deletions creates empty nodes that qualify for removal. For `WhenUnderutilized`, the benefit is indirect but still meaningful — concentrating deletions on already-underutilized nodes pushes them closer to the utilization threshold faster, reducing the number of consolidation moves (and therefore disruptions) needed to reach optimal state. The magnitude of improvement is larger for WhenEmpty policies.

## 8. "Positive feedback loop" oversells the mechanism
**Status: 🔧 Will fix**

The reviewer is right — there's no feedback in the control-theory sense. The ReplicaSet controller doesn't observe the result of its previous deletion and amplify it. It just follows the annotation each time.

**Proposed change:** Replace "positive feedback loop" with "signal alignment" throughout. Rewrite the numbered list in the "Why node-level ranking is the right signal" section:

> The practical consequence is a signal alignment between two controllers:
> 1. Karpenter ranks nodes by consolidation priority
> 2. Pods on those nodes get the lowest deletion costs
> 3. ReplicaSet scale-down removes pods from those nodes first
> 4. Those nodes become empty or closer to empty
> 5. Karpenter can consolidate them with less disruption
>
> Each step follows from the previous, but there is no amplification — the ReplicaSet controller simply follows the annotation on each scale-down event independently.

## 9. Watch event amplification during active scaling
**Status: 🔧 Will fix**

The RFC acknowledges watch events are "bounded by the 60-second reconcile interval" but doesn't estimate magnitude during active scaling, which is exactly when the controller writes.

**Proposed change:** Add to Appendix C Performance section:

> **Active scaling fan-out estimate:** In a worst case of 1,000 nodes with 50 pods/node, a full re-ranking produces 50,000 pod update events per reconcile cycle. Every controller watching pods (scheduler, operators, admission webhooks) receives these events. During active scaling, cluster state changes every cycle, so change detection provides no relief. Mitigation: annotation value diffing (planned for beta) would reduce writes to only pods whose rank actually changed — typically a small fraction of the total during incremental scaling. For alpha, operators should monitor `pods_updated_total` and consider disabling the feature if API server latency correlates with reconcile cycles.

## 10. -1000 starting rank breaks at scale
**Status: 🔧 Will fix**

The RFC documents its own breakage: "this will need to be adjusted for clusters with >1000 nodes." The annotation value is an int32.

**Proposed change:** Replace the -1000 starting rank with negative rank order: start at `-n` where `n` is the total number of nodes being ranked, and increment by 1. For a 500-node cluster, Group A starts at -500. This scales to any realistic cluster size within int32 bounds. Remove the TODO comment. Update the example to use this scheme and update the diagram.

## 11. Two-annotation protocol only works for two writers
**Status: 📌 Defer (acknowledged)**

Valid limitation. The sentinel annotation protocol works for exactly two writers: the customer and Karpenter. A third controller managing pod-deletion-cost would conflict.

**Proposed change:** Add an explicit acknowledgment in the Risks section:

> **Multi-writer limitation (Low):** The two-annotation protocol assumes exactly two writers: the customer (or their controller) and Karpenter. If a third controller also manages `pod-deletion-cost`, last-writer-wins semantics apply and behavior is undefined. This is an accepted limitation for alpha. A future solution could use server-side apply with distinct field managers to support multiple writers.

## 12. Reconcile interval has no justification
**Status: 🔧 Will fix**

"Every 60 seconds" is stated without reasoning.

**Proposed change:** Add a sentence after "A singleton reconciler runs every 60 seconds":

> The 60-second interval balances convergence speed against API server write load. At 30 seconds, annotations converge faster after scaling events but write load doubles. At 120 seconds, writes halve but annotations can lag behind rapid scaling by up to two minutes, reducing effectiveness during burst scale-downs. 60 seconds is a pragmatic starting point; the interval is configurable and can be tuned based on cluster-specific API server capacity and scaling patterns.

## 13. Appendices too long
**Status: 🔧 Will fix**

Appendices A-F plus Non-Goals are longer than the proposal. Much of the content (specific metric names, structured logging levels, unit test case lists, rollback shell scripts) is implementation detail.

**Proposed change:** Trim appendices to decisions-only content:
- **Keep:** Appendix B (do-not-disrupt example — illustrates a design decision), Appendix C security section (RBAC review needed), the Placement and Spreading Constraints section (addresses a design concern)
- **Move to implementation PR:** Appendix A (component descriptions — code speaks for itself), Appendix D metrics table (belongs in code/docs), Appendix E test lists (belongs in test files), Appendix F rollback script and migration steps (belongs in release docs)
- **Condense:** Appendix C performance section into 2-3 sentences in the main Risks section. Keep the fan-out estimate from point 9 above.

---

## Summary

| # | Point | Status |
|---|-------|--------|
| 1 | TODO on RS controller alternative | ✅ Already fixed |
| 2 | Default strategy contradicts recommendation | ✅ Already fixed |
| 3 | Too many strategies for alpha | 🔧 Trim to PodCount + Random |
| 4 | 30-80% claim unlinked | 🔧 Soften claims, promise published benchmarks |
| 5 | Example only shows best case | 🔧 Add partial-drain example |
| 6 | Motivation conflates two problems | 🔧 Split into two labeled paragraphs |
| 7 | WhenEmpty vs WhenUnderutilized | 🔧 Add value-by-policy paragraph |
| 8 | "Positive feedback loop" oversells | 🔧 Reframe as signal alignment |
| 9 | Watch event amplification | 🔧 Add active-scaling fan-out estimate |
| 10 | -1000 starting rank breaks at scale | 🔧 Use negative rank order (-n) |
| 11 | Two-annotation protocol limitation | 📌 Acknowledge explicitly, defer fix |
| 12 | Reconcile interval unjustified | 🔧 Add reasoning sentence |
| 13 | Appendices too long | 🔧 Trim to decisions-only, move rest to impl PR |
