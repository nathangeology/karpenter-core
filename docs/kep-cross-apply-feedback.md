# Cross-Apply: Karpenter RFC Feedback → KEP for ReplicaSet Consolidation-Aware Scale-In

| Field | Value |
|-------|-------|
| **KEP** | [nathangeology/enhancements#2](https://github.com/nathangeology/enhancements/pull/2) — `ConsolidatingScaleDown` |
| **RFC** | `designs/pod-deletion-cost-controller.md` (branch: `Pod-Deletion-Cost-RFC`) |
| **RFC Feedback** | `mayor/rig/docs/pr-2935-feedback-response.md` (13 items) |
| **Drift Tier Proposal** | `docs/pr-2935-drift-tier-proposal.md` (branch: `polecat/jasper/kp-5u3@mnj3oh1x`) |

## Feedback Cross-Application Table

| # | RFC Feedback Item | Applies to KEP? | Rationale | Proposed KEP Change |
|---|-------------------|-----------------|-----------|---------------------|
| 1 | RS controller alternative — relationship between KEP and RFC | **Yes** | The KEP and RFC are complementary approaches to the same problem. The KEP changes the RS controller directly (long-term); the RFC uses annotations (short-term). The KEP should reference the RFC as the near-term solution and explain the deprecation path. | Add a paragraph in Alternatives referencing the Karpenter RFC as the complementary short-term approach. See [Proposed Text: Item 1](#item-1-reference-karpenter-rfc-as-complementary-approach). |
| 2 | Default strategy contradicts recommendation | **No** | The KEP has a single heuristic (consolidation by pod count) behind a feature gate, not a configurable strategy selection. There is no default-vs-recommendation inconsistency to address. | None. |
| 3 | Too many strategies for alpha | **No** | The KEP proposes exactly one heuristic (consolidation by total active pod count). There is no strategy proliferation concern. | None. |
| 4 | Benchmark claims — 30-80% unlinked | **Partial** | The KEP does not make specific percentage claims, but Story 1 says "reduce my cloud spend" and the Monitoring section references "node count reduction" without quantifying expected improvement. The KEP should be careful not to imply specific gains without evidence. | Add a sentence in Motivation noting that the magnitude of improvement depends on workload shape, cluster topology, and consolidation policy. See [Proposed Text: Item 4](#item-4-qualify-improvement-expectations). |
| 5 | Example only shows best case | **Yes** | The KEP has no worked examples at all. The User Stories describe ideal outcomes (Story 1: Karpenter reclaims empty nodes; Story 3: "preferentially empty out nodes"). Adding a partial-drain example would strengthen the proposal. | Add a worked example in Design Details showing both a clean-drain and a partial-drain case. See [Proposed Text: Item 5](#item-5-add-worked-examples). |
| 6 | Motivation conflates two problems | **Partial** | The KEP's Motivation is cleaner than the RFC's — it focuses on the coordination gap between the RS controller's spreading heuristic and node autoscaler consolidation. However, it could more explicitly separate "the spreading heuristic is suboptimal" from "no coordination mechanism exists." | Add a subheading to split the two concerns. See [Proposed Text: Item 6](#item-6-split-motivation-concerns). |
| 7 | ConsolidateWhenEmpty vs WhenUnderutilized | **Yes** | The KEP mentions Karpenter and cluster-autoscaler generically but doesn't distinguish the value proposition by consolidation policy. The benefit is direct for WhenEmpty (creates empty nodes) and indirect for WhenUnderutilized (pushes nodes closer to utilization thresholds). | Add a paragraph in Motivation explaining value by consolidation policy. See [Proposed Text: Item 7](#item-7-value-by-consolidation-policy). |
| 8 | "Positive feedback loop" oversells | **No** | The KEP does not use "positive feedback loop" or similar language. The mechanism description is straightforward. | None. |
| 9 | Watch event amplification | **No** | The KEP modifies the RS controller's in-memory sort comparator. It does not write annotations, so there are no watch events generated. This feedback is specific to the RFC's annotation-writing approach. | None. |
| 10 | Rank scaling (-1000 breaks at scale) | **No** | The KEP does not use annotation values or integer ranks. The consolidation ranking is computed in-memory per scale-down event using live pod counts. No scaling concern. | None. |
| 11 | Multi-writer annotation protocol | **No** | The KEP does not write annotations. It reads `do-not-disrupt` annotations but does not participate in a multi-writer protocol. | None. |
| 12 | Reconcile interval unjustified | **No** | The KEP has no reconcile loop. The consolidation heuristic runs inline during each scale-down event in the RS controller's sync cycle. | None. |
| 13 | Appendices too long | **Partial** | The KEP is well-scoped at ~584 lines. However, the Test Plan section lists specific file paths and coverage targets that are implementation detail. The PRR questionnaire is necessarily long per KEP template requirements. | Consider moving specific file paths and coverage percentages from the Test Plan to the implementation PR. Keep the test categories (unit, integration, e2e) and what they cover. See [Proposed Text: Item 13](#item-13-trim-implementation-detail-from-test-plan). |

### Summary

| Applies | Count | Items |
|---------|-------|-------|
| Yes | 3 | #1 (RS controller alternative), #5 (examples), #7 (WhenEmpty vs WhenUnderutilized) |
| Partial | 3 | #4 (benchmark claims), #6 (motivation split), #13 (appendix length) |
| No | 7 | #2, #3, #8, #9, #10, #11, #12 |

Items 9-12 do not apply because the KEP changes the RS controller directly rather than using an annotation-writing sidecar controller. The annotation-related feedback is specific to the RFC's architecture.

---

## Three-Tier Drift Ranking in the KEP Context

### Concept

The Karpenter RFC's three-tier drift ranking proposal partitions nodes into:
1. **Group A (Drifted):** Nodes with `ConditionTypeDrifted=True` — lowest deletion cost
2. **Group B (Normal):** Non-drifted, non-protected nodes — middle deletion cost
3. **Group C (Do-not-disrupt):** Protected nodes — highest deletion cost

The KEP's `ConsolidatingScaleDown` heuristic currently has a two-tier model:
1. Pods on non-protected nodes (preferred for deletion)
2. Pods on do-not-disrupt nodes (deprioritized)

### Feasibility Assessment

**Can the KEP incorporate drift-aware ranking?**

Yes, with caveats:

1. **Data availability:** The KEP operates inside the RS controller, which has access to the node informer (already initialized when the feature gate is enabled). Node conditions including `ConditionTypeDrifted` are available via the node informer cache. The RS controller can check `node.Status.Conditions` for a condition with type matching a drifted signal.

2. **Annotation vs condition:** Karpenter uses `ConditionTypeDrifted` as a node condition (not an annotation). The KEP would need to define which signal indicates "drifted." Options:
   - Check for Karpenter's specific `Drifted` condition type (couples to Karpenter)
   - Define a generic `controller.kubernetes.io/node-lifecycle-phase` annotation (too broad for alpha)
   - Use a configurable condition type (adds complexity)

3. **Scope concern:** The KEP already introduces one new concept (consolidation-aware scale-down). Adding drift awareness in the same KEP increases scope and review surface. SIG-apps reviewers may push back on coupling to node lifecycle concepts in the RS controller.

### Recommendation

**Defer drift-tier ranking to a follow-up KEP or beta enhancement.** The reasoning:

- The KEP's two-tier model (normal vs do-not-disrupt) is already a significant behavioral change for the RS controller. Adding a third tier based on node lifecycle state increases the coupling between the RS controller and node autoscaler internals.
- The Karpenter RFC's annotation-based approach can implement three-tier ranking independently (it controls the annotation values). The KEP doesn't need to duplicate this — the two approaches are complementary.
- If the KEP ships in alpha with two tiers, and the RFC ships with three-tier annotations, PodDeletionCost (step 4 in the sort order) takes precedence over the consolidation heuristic (step 6). The RFC's three-tier ranking via annotations would override the KEP's two-tier in-memory ranking, giving the best of both worlds when both are active.

**However, the KEP should acknowledge drift as a future enhancement:**

> **Future: Drift-aware consolidation (post-alpha).** When a node is marked for replacement (e.g., Karpenter's `Drifted` condition, or a future generic node lifecycle signal), scale-down should prefer deleting pods from that node. This extends the two-tier model (normal vs do-not-disrupt) to three tiers (drifted → normal → do-not-disrupt). This is deferred to beta to limit alpha scope and to allow the generic annotation design (`controller.kubernetes.io/do-not-disrupt`) to inform the design of a generic drift signal. In the interim, the Karpenter Pod Deletion Cost Controller RFC provides three-tier drift ranking via annotations, which takes precedence over the in-tree heuristic via PodDeletionCost (step 4 in the sort order).

### Interaction Model: KEP + RFC Coexistence

When both the KEP and the Karpenter RFC are active:

| Scenario | Behavior |
|----------|----------|
| KEP only (no RFC) | Two-tier: normal vs do-not-disrupt. No drift awareness. |
| RFC only (no KEP) | Three-tier via annotations: drifted → normal → do-not-disrupt. RS controller uses PodDeletionCost sort step. |
| Both active | RFC annotations set PodDeletionCost (step 4), which takes precedence over KEP's consolidation heuristic (step 6). Three-tier ranking is effective. The KEP's do-not-disrupt deprioritization (step 5) adds defense-in-depth. |
| Neither active | Default RS spreading heuristic. |

This coexistence model means the KEP does not need to implement drift ranking itself — the RFC handles it via the annotation layer, and the sort order ensures correct precedence.

---

## Proposed Text Additions for the KEP

### Item 1: Reference Karpenter RFC as Complementary Approach

**Location:** Alternatives section, add after "Extend PodDeletionCost (KEP-2255)"

> ### Karpenter Pod Deletion Cost Controller (Annotation-Based Approach)
>
> The Karpenter project has an RFC for a Pod Deletion Cost Controller that
> achieves similar goals via a different mechanism: a sidecar controller that
> ranks nodes by consolidation preference and writes `pod-deletion-cost`
> annotations. This approach works with the existing RS controller sort order
> (step 4: PodDeletionCost) without requiring changes to Kubernetes core.
>
> **Relationship to this KEP:** The two approaches are complementary, not
> competing. The annotation-based approach can ship independently of Kubernetes
> release cycles and provides additional capabilities (three-tier drift ranking,
> configurable strategies). This KEP provides a zero-dependency, in-tree solution
> that requires no external controller. When both are active, the annotation-based
> PodDeletionCost (sort step 4) takes precedence over the in-tree consolidation
> heuristic (sort step 6), allowing the external controller to override or refine
> the in-tree behavior.
>
> **Deprecation path:** If this KEP reaches GA and the community adopts it widely,
> the annotation-based controller becomes optional — useful for advanced ranking
> strategies (drift-aware, resource-weighted) but not required for basic
> consolidation. The annotation-based approach remains the recommended path for
> users who need capabilities beyond the in-tree heuristic.

### Item 4: Qualify Improvement Expectations

**Location:** End of Motivation section, before Goals

> The magnitude of consolidation improvement depends on workload shape (uniform
> vs skewed replica counts), cluster topology (number of nodes, pods per node),
> and the node autoscaler's consolidation policy (empty-node-only vs
> underutilization-based). The consolidation heuristic is most effective when
> scale-down events remove enough replicas to empty or nearly empty at least one
> node. For small scale-down events (e.g., removing 1-2 replicas from a large
> deployment), the improvement over the spreading heuristic may be minimal.

### Item 5: Add Worked Examples

**Location:** New subsection in Design Details, after "Interaction with Existing Scale-Down Logic"

> ### Worked Examples
>
> #### Clean drain: Scale from 9 to 6 replicas
>
> A Deployment with 9 replicas across 3 nodes:
>
> ```
> Node A: 3 pods (5 total active pods on node)
> Node B: 3 pods (8 total active pods on node)
> Node C: 3 pods (7 total active pods on node)
> ```
>
> With `ConsolidatingScaleDown` enabled, the RS controller ranks pods by total
> active pods on their node (ascending). Node A has the fewest total active pods
> (5), so its 3 replica pods are deleted first. Node A is now empty of this
> Deployment's pods, and if the other 2 pods on Node A are also scaled down or
> belong to other shrinking workloads, the node becomes reclaimable.
>
> **Without the feature:** The spreading heuristic distributes 3 deletions across
> all 3 nodes (1 per node). Every node retains 2 replicas. No node moves closer
> to empty.
>
> #### Partial drain: Scale from 9 to 7 replicas
>
> Same cluster. The RS controller removes 2 pods, both from Node A (fewest total
> active pods). Node A still has 1 replica — not empty yet. But Node A is now the
> least-occupied node for this Deployment, so on the next scale-down event, its
> remaining pod is removed first. Convergence takes multiple events, but each
> event moves the system toward consolidation.
>
> **Without the feature:** The 2 deletions spread across 2 nodes. No node is
> closer to empty than before.

### Item 6: Split Motivation Concerns

**Location:** Replace the first two paragraphs of Motivation with subheadings

> ### The Spreading Heuristic Works Against Consolidation
>
> The current ReplicaSet scale-down algorithm prefers deleting pods on nodes with
> *more* colocated replicas of the same ReplicaSet (a spreading heuristic). While
> this promotes even distribution, it actively works against node consolidation.
>
> ### No Coordination Mechanism Exists
>
> Node autoscalers such as Karpenter and cluster-autoscaler can reclaim empty or
> underutilized nodes, but only when workloads consolidate during scale-down. When
> an HPA scales a Deployment from 20 replicas to 10, the current spreading
> heuristic distributes deletions evenly across nodes, leaving every node partially
> occupied and preventing any node from being reclaimed.
>
> KEP-2255 (PodDeletionCost) provides a mechanism for influencing pod deletion
> order via annotations, but it requires an external controller to continuously
> update annotations before scale-down events occur. This is operationally complex,
> does not integrate with HPA-driven scale-down, and continuously updating
> annotations places unnecessary load on the API server.

### Item 7: Value by Consolidation Policy

**Location:** End of Motivation, after the improvement-expectations paragraph from Item 4

> **Value by consolidation policy:** For node autoscalers configured with an
> empty-node-only consolidation policy (e.g., Karpenter's `ConsolidateWhenEmpty`),
> the benefit is direct — concentrating deletions creates empty nodes that qualify
> for removal. For underutilization-based policies (e.g., Karpenter's
> `WhenUnderutilized`, cluster-autoscaler's default), the benefit is indirect but
> still meaningful — concentrating deletions on already-underutilized nodes pushes
> them closer to the utilization threshold faster, reducing the number of
> consolidation moves (and therefore disruptions) needed to reach optimal state.
> The magnitude of improvement is larger for empty-node-only policies.

### Item 13: Trim Implementation Detail from Test Plan

**Location:** Replace specific file paths and coverage targets in Test Plan

Current:
> - `pkg/controller/controller_utils_test.go`: Tests for `ActivePodsWithRanks.Less()` ...
> - Coverage targets: `pkg/controller/controller_utils.go`: target 85%+ ...

Proposed:
> #### Unit Tests
>
> - Pod deletion ranking with `ConsolidatingScaleDown` enabled and disabled:
>   consolidation rank ordering, do-not-disrupt deprioritization, interaction
>   with PodDeletionCost, swap correctness
> - Pod-per-node counting: correct indexer usage, do-not-disrupt annotation
>   detection, unassigned pods, feature gate toggle
>
> Specific file paths and coverage targets will be documented in the
> implementation PR.

---

## Drift-Tier Ranking: Proposed Future Enhancement Text

**Location:** Add to Design Details, after "Interaction with Existing Scale-Down Logic" (or in a new "Future Work" section before Graduation Criteria)

> ### Future: Drift-Aware Consolidation (Post-Alpha)
>
> Node autoscalers mark nodes for replacement when they drift from their desired
> state (e.g., outdated AMI, changed configuration). Karpenter uses a `Drifted`
> node condition; other autoscalers may use different signals. Scale-down should
> prefer deleting pods from drifted nodes, since those nodes need replacement
> regardless — draining them via scale-down avoids additional disruption from a
> separate drain operation.
>
> This extends the current two-tier model to three tiers:
>
> 1. **Drifted nodes** (highest deletion priority) — pods here are deleted first
> 2. **Normal nodes** (middle priority) — standard consolidation targets
> 3. **Do-not-disrupt nodes** (lowest deletion priority) — protected
>
> This enhancement is deferred to beta for two reasons: (1) it limits alpha scope
> to the core consolidation heuristic, and (2) the design of a generic drift
> signal should be informed by the generic `controller.kubernetes.io/do-not-disrupt`
> annotation work planned for beta. In the interim, the Karpenter Pod Deletion
> Cost Controller provides three-tier drift ranking via PodDeletionCost
> annotations, which takes precedence over the in-tree heuristic in the sort order.
