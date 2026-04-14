# KEP Style Recommendations — Batch 1: sig-apps KEPs

**Source KEPs studied:**
1. KEP-2255: ReplicaSet Pod Deletion Cost (GA)
2. KEP-3017: Unhealthy Pod Eviction Policy for PDBs (GA)
3. KEP-3329: Retriable and Non-Retriable Pod Failures for Jobs (GA)

**Our doc:** `problem-statement.md` — Coordinating Workload Scale-In with Node Autoscaling

---

## Pattern Observations from Successful KEPs

### 1. Opening: Problem-first, not context-first

All three KEPs open their Motivation section by naming the broken behavior immediately, then layering in context only as needed.

- **KEP-2255** opens with: "Currently ReplicaSets are scaled down based on a criteria that on the limit prioritizes deleting pods with a more recent creation/readiness timestamp. This is not ideal for some applications..." — two sentences, and you already know what's wrong.
- **KEP-3017** opens with: "Pod Disruption Budgets currently don't provide a way for users to specify how to handle pods that are Running, but not Healthy (Ready)." — one sentence, gap identified.
- **KEP-3329** opens with: "Running a large computational workload... requires usage of pod restart policies in order to account for infrastructure failures. Currently, kubernetes Job API offers a way to account for infrastructure failures by setting `.backoffLimit > 0`. However, this mechanism instructs the job controller to restart all failed pods — regardless of the root cause of the failures." — three sentences: context, current mechanism, what's broken.

**Pattern:** The first 1–3 sentences of the Motivation section name the specific gap or broken behavior. Context comes after, not before.

### 2. Motivation length and density

The successful KEPs keep their motivation sections focused and proportional:

- **KEP-2255:** ~150 words. Extremely concise. States the problem, gives two user stories, done.
- **KEP-3017:** ~250 words. Two paragraphs: one for the problem, one for why the current behavior exists (two competing use cases). Then Goals/Non-Goals.
- **KEP-3329:** ~300 words of core motivation, followed by links to community issues and third-party frameworks as evidence of demand. The motivation itself is tight; the evidence is additive, not structural.

**Pattern:** Motivation sections are 150–300 words of core argument. Evidence (issues, community discussions, third-party solutions) is cited as supporting material, not woven into the narrative.

### 3. Use of concrete examples

- **KEP-2255:** User stories serve as the examples. Story 1: "pods with lower utilization should be removed first." Story 2: "remove pods on expensive nodes first." Short, specific, no YAML in the motivation.
- **KEP-3017:** The motivation itself is the example — two competing use cases (availability vs. data-loss prevention) are described in plain language. The concrete API examples come later in the Proposal section.
- **KEP-3329:** Uses a detailed "Current state review" section with reproduced scenarios (Preemption, Taint-based eviction, Node drain, etc.) showing exact pod status fields. This is the most example-heavy of the three, but the examples are in a dedicated subsection, not in the motivation.

**Pattern:** Motivation uses plain-language scenarios. Detailed technical examples (YAML, status fields, reproduction steps) go in Proposal or Design Details, not in the problem statement.

### 4. Tone

- **KEP-2255:** Neutral, understated. "This is not ideal for some applications." No urgency language.
- **KEP-3017:** Neutral, acknowledges both sides. "Both use-cases have rough edges with the current implementation." Frames the problem as a design gap, not a failure.
- **KEP-3329:** Slightly more assertive but still measured. "This leads to unnecessary restarts of many pods, resulting in a waste of time and computational resources." Uses "waste" but backs it up immediately with the mechanism.

**Pattern:** Neutral to mildly assertive. The KEPs let the problem speak for itself. They don't use urgency language ("critical," "severe," "broken") — they describe the gap and let the reader conclude it matters. They acknowledge existing behavior as reasonable-for-its-time rather than criticizing it.

### 5. Framing: what's broken vs. what they want

- **KEP-2255:** "Currently X happens. This is not ideal because Y. We propose Z." Three-beat structure.
- **KEP-3017:** "PDBs are used for two purposes. The current implementation has rough edges for both. We add a field to let users choose." Frames as a design refinement, not a fix.
- **KEP-3329:** "The current mechanism does X. However, it doesn't distinguish Y from Z. This leads to waste." Frames as a missing capability in an otherwise working system.

**Pattern:** All three frame the problem as a missing capability or a design gap in an otherwise reasonable system. None of them frame the current behavior as a bug or a mistake. This is collaborative framing — it respects the original design decisions while making the case for evolution.

### 6. Prior art and references

- **KEP-2255:** No prior art section. The feature is simple enough that it doesn't need one.
- **KEP-3017:** References a specific GitHub issue (#72320) and a PR discussion (#105296) as evidence that the problem is real and has been discussed. Brief, inline.
- **KEP-3329:** References two GitHub issues (#17244, #31147) and two third-party frameworks (TFJob, Argo) as evidence of community demand. Also links to Kubernetes docs for context. The references are in the Motivation section, used as "the community has already identified this need."

**Pattern:** Prior art is cited as evidence of demand or prior discussion, not as a literature review. References are inline and brief — a link and a one-line description of relevance.

---

## Specific Recommendations for Our Problem Statement

### A. Restructure the opening to lead with the gap

**Current opening:** "Kubernetes clusters use node autoscalers like Karpenter and cluster-autoscaler to right-size infrastructure. These autoscalers remove nodes that are empty or underutilized..." — This is context, not the problem. The actual problem statement ("there's a coordination gap") doesn't arrive until the fourth sentence.

**Recommendation:** Open with the coordination gap directly. Something like: "When workloads scale down, the ReplicaSet controller selects pods for deletion without considering node utilization. This means scale-down events spread deletions evenly across nodes instead of concentrating them to free up nodes for removal." Then layer in the context about autoscalers.

The successful KEPs all name the broken behavior in sentence 1–2. Our doc buries it.

### B. Shorten the problem statement core

Our problem statement section is ~600 words before the Example. The successful KEPs keep their core motivation to 150–300 words. The "How the RS controller selects pods today" subsection is valuable but reads more like Design Details than Motivation.

**Recommendation:** Move the RS controller sort-order details into a "Background" or "Current Behavior" section (like KEP-3329's "Current state review"). Keep the Problem Statement section to ~200 words: what's broken, why it matters, one sentence on impact.

### C. Simplify the example

The 5-node example is good and concrete, but it's presented as a wall of text. KEP-3329 uses bullet-pointed reproduction steps with clear field-by-field status. KEP-2255 uses one-sentence user stories.

**Recommendation:** Keep the example but tighten it. Use a before/after format with numbers:
- Today: 10 deletions spread across 5 nodes → 0 empty nodes → autoscaler must evict more
- Proposed: 10 deletions concentrated on 2–3 nodes → 2 empty nodes → autoscaler removes them with zero additional disruption

This is essentially what the doc says, but a tighter format makes it scannable.

### D. Soften the tone in a few places

Our doc is well-written but occasionally more assertive than the successful KEPs:
- "the damage is done" (in the timing problem section) — the successful KEPs avoid dramatic framing
- "This is the worst case for node consolidation" — KEP-style would be "This is suboptimal for node consolidation" or "This works against node consolidation"

**Recommendation:** Replace a few phrases:
- "the damage is done" → "the opportunity is lost" or "the deletions have already been spread"
- "worst case" → "suboptimal" or "works against"
- The overall tone is already good — these are minor adjustments to match the collaborative, neutral tone of successful KEPs.

### E. Tighten the "Impact" section

Our Impact section mixes quantitative claims (BMW's 375+ clusters) with qualitative statements. The successful KEPs either cite specific issues/discussions as evidence or keep impact implicit in the motivation.

**Recommendation:** The BMW reference is strong — keep it. But trim the section. The wg-serving paragraph could move to the Design Questions section (where it already appears). The Impact section should be 2–3 sentences: who is affected, how much, and one concrete reference.

### F. Reframe "Related Work" as evidence of demand

Our Related Work section is thorough but reads like a literature review. The successful KEPs use prior art to say "this problem is real and people have tried to solve it" — not to survey the landscape.

**Recommendation:** Rename to "Prior Art" or keep "Related Work" but restructure each entry as: "X tried to solve this. Here's what worked and what didn't. Our approach builds on / differs from X because Y." Currently, some entries (like the Eviction Request API) read as neutral descriptions rather than positioned relative to our proposal.

### G. Add explicit Goals / Non-Goals early

All three successful KEPs have Goals and Non-Goals sections immediately after Motivation. Our doc has "Design Questions" but no explicit Goals/Non-Goals.

**Recommendation:** Add a Goals / Non-Goals section after the Problem Statement. This is a KEP convention that reviewers expect. Even for a problem statement doc (pre-KEP), stating what's in scope and what's not helps focus the discussion. For example:
- Goal: Reduce unnecessary pod disruption during workload scale-down by improving pod deletion selection in the RS controller.
- Non-Goal: Replacing the Eviction API, changing StatefulSet scale-down semantics, or building a general-purpose pod deletion framework.

### H. Consider factual precision on the RS controller sort order

Our doc describes the RS controller's sort order as a 5-step priority chain. This is accurate and useful, but the successful KEPs tend to describe current behavior at a higher level in the Motivation and save implementation details for Design Details.

**Recommendation:** Keep the sort-order description but move it to a "Background" section. In the Problem Statement, summarize it as: "The RS controller's pod deletion heuristic prioritizes even distribution of deletions across nodes (spreading). This is workload-local — it doesn't consider node utilization or autoscaler goals." The detailed 5-step breakdown is valuable for reviewers who want to understand the mechanism, but it shouldn't be the first thing they read.

---

## Summary of Key Patterns

| Pattern | Successful KEPs | Our Doc |
|---------|----------------|---------|
| Opening | Problem in sentence 1–2 | Context first, problem in sentence 4 |
| Motivation length | 150–300 words | ~600 words before example |
| Examples | Plain language in motivation, technical details later | Good example but could be tighter |
| Tone | Neutral, collaborative, "gap not bug" | Mostly good, a few assertive phrases |
| Prior art | Evidence of demand, briefly cited | Thorough survey, could be more positioned |
| Goals/Non-Goals | Present in all three | Missing |
| Technical detail placement | Background/Design Details sections | Mixed into Problem Statement |
