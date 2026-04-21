# De-LLM Review: problem-statement-v2.md

Reviewer: obsidian (polecat)
Date: 2026-04-21
Source: `enhancements/mayor/rig/keps/sig-apps/NNNN-replicaset-consolidation-scale-in/problem-statement-v2.md`

---

## Overall Assessment

This is a strong v2. The voice is mostly natural — it reads like an engineer explaining a problem they've been thinking about. The concrete example is good, the option analysis is honest about tradeoffs, and the document doesn't over-hedge. That said, there are residual LLM patterns worth cleaning up before this goes to the community.

Severity scale: **HIGH** = immediately recognizable as AI-generated, **MED** = pattern that accumulates, **LOW** = minor polish.

---

## Findings

### 1. Colon-heavy section openers (Tell #6, #9)

**Severity:** MED

**Location:** "The Problem" section, paragraph 3

> This is a coordination gap between workload controllers and infrastructure controllers.

And later in "Why this is hard to fix after the fact":

> The timing matters.

These short declarative topic sentences followed by explanation are fine individually, but the document uses this pattern repeatedly. It's the "The core issue:" / "The key insight:" formula without the literal colon — same cadence though.

**Also:** "The core case described above." (Use Case 1), "This is the key design question." (Where Should the Intelligence Live?)

**Suggested rewrite for "Where Should the Intelligence Live?" opener:**

Before:
> This is the key design question. There are several places where scale-in coordination logic could run, each with different tradeoffs.

After:
> Scale-in coordination logic could run in several places, each with different tradeoffs.

Drop the throat-clearing sentence. The heading already tells the reader this is the key question.

---

### 2. Bolded lead-ins on every list item (Tell #4)

**Severity:** HIGH

**Location:** Options A, B, C — every Strengths/Limitations bullet

Every single bullet under Strengths and Limitations starts with a bolded clause or em-dash summary. Example from Option A:

> - **No external dependencies** — uses information already in the informer cache
> - **Simple to understand** and reason about

This is the most recognizable LLM pattern in the document. Real KEPs don't bold-lead every bullet. Some bullets are just sentences.

**Suggested rewrite (Option A Strengths):**

Before:
> - No external dependencies — uses information already in the informer cache
> - Simple to understand and reason about
> - Works for all ReplicaSet-managed workloads immediately
> - Feature-gated, opt-in

After:
> - Uses information already in the informer cache, no external dependencies
> - Simple to understand and reason about
> - Works for all ReplicaSet-managed workloads immediately
> - Feature-gated, opt-in

Just remove the bold formatting and let the bullets be plain sentences. Some can keep emphasis where it's genuinely useful — just not all of them.

---

### 3. Em dash overuse (Tell #1)

**Severity:** MED

**Location:** Throughout

Counted 16 em dashes in the document. Some are fine (the concrete example uses them well). But several are the LLM's favorite "clause — elaboration" pattern:

- "prefer pods on nodes with fewer total pods" (consolidation) — fine, parenthetical already does the job
- "Karpenter already reasons about node utilization, drift, and consolidation. It's well-positioned to reconcile multiple inputs (node cost, workload sensitivity, drift status) and express the result as `pod-deletion-cost` annotations that the RS controller consumes." — no em dash here, but the pattern shows up nearby
- "No external dependencies — uses information already in the informer cache"
- "Longer timeline — depends on the Karpenter upstreaming effort"
- "Natural integration point as Karpenter upstreams into the scheduler" — this one doesn't have one, showing the pattern is inconsistent

**Suggested fix:** Cut about half the em dashes. Replace with periods (two sentences), commas, or parentheticals. No single fix — just reduce density. Target: 8 or fewer.

---

### 4. Symmetrical option structure (Tell #7)

**Severity:** MED

**Location:** Options A, B, C

All three options follow the exact same template: one-line summary, paragraph of explanation, **Strengths:** bullet list, **Limitations:** bullet list. The symmetry is too perfect. Real design docs have options where one gets more discussion than others, or where the tradeoffs are discussed in prose rather than mirrored bullet lists.

**Suggested fix:** Break the symmetry. Option A is simpler — it could be shorter, maybe just a paragraph with inline tradeoffs. Option C is speculative — it could be more discursive, acknowledging uncertainty in prose rather than neat bullets. Keep the full Strengths/Limitations format for Option B (the meatiest one).

---

### 5. "We'd like community input" hedging (Tell #5)

**Severity:** LOW

**Location:** End of "Where Should the Intelligence Live?" section

> We'd like community input on this sequencing. Does this progression make sense? Should we prioritize differently?

And the final paragraph:

> We want to get this right, and getting it right means hearing from the people who run these systems in production. Your input shapes the design.

The second one is the worse offender — it's a generic "your voice matters" closer that could appear on any community document. It doesn't say anything specific.

**Suggested rewrite (final paragraph):**

Before:
> We want to get this right, and getting it right means hearing from the people who run these systems in production. Your input shapes the design.

After:
> If you're running Karpenter or cluster-autoscaler at scale and hitting this, we want to hear what you've tried and what broke.

Specific, direct, no flattery.

---

### 6. Formulaic transitions (Tell #6)

**Severity:** LOW

**Location:** Multiple

- "The result:" (The Problem, paragraph 2) — colon-introduced consequence
- "The result: after a scale-down event, every node is still partially occupied."
- "A likely path forward combines these approaches:" — colon-introduced list

These are minor individually but contribute to a cadence that feels templated.

**Suggested fix for "The result:":**

Before:
> The result: after a scale-down event, every node is still partially occupied.

After:
> After a scale-down event, every node is still partially occupied.

Just delete "The result:" — the reader can figure out causality from context.

---

### 7. Numbered list as outline (Tell #12)

**Severity:** MED

**Location:** "Why this is hard to fix after the fact" — the 5-step sequence

> 1. HPA decides replicas should decrease
> 2. RS controller immediately selects pods to delete using its built-in heuristic
> 3. Pods are terminated
> 4. Autoscaler notices underutilized nodes
> 5. Autoscaler consolidates — evicting more pods to empty nodes it can remove

This reads like an outline, not an explanation. Each step is a single clause. It would read more naturally as a short paragraph describing the sequence, or at minimum the steps should have more flesh on them.

**Suggested rewrite:**

> The HPA decides replicas should decrease, and the RS controller immediately picks pods to delete using its built-in heuristic. Those pods are terminated. Then the autoscaler notices underutilized nodes and starts consolidating — evicting *more* pods to empty out nodes it can remove.

Same information, reads like someone explaining it rather than bullet-pointing a flowchart.

---

### 8. "Both X and Y" / parallel structure (Tell #7)

**Severity:** LOW

**Location:** "The Problem" section, paragraph 3

> The workload controller doesn't know which nodes the autoscaler wants to drain. The autoscaler can't influence which pods the workload controller deletes. Both are doing reasonable things independently, but the combined result is worse than either would produce alone.

The first two sentences are a nice parallel. The third sentence ("Both are doing reasonable things independently, but the combined result is worse than either would produce alone") is the LLM's symmetry reflex — it's a generic observation that adds nothing the reader hasn't already inferred.

**Suggested rewrite:**

Before:
> Both are doing reasonable things independently, but the combined result is worse than either would produce alone.

After:
> They're not coordinating, and it shows.

Or just delete the sentence. The two preceding sentences already make the point.

---

### 9. "These aren't mutually exclusive" (Tell #6, #8)

**Severity:** LOW

**Location:** Subheading under the options

> ### These aren't mutually exclusive

This is a signpost that LLMs love. It's the "let me reassure you that I'm being balanced" move. The content under it is good (the short/medium/long term progression), but the heading is unnecessary — the reader can see the options aren't exclusive from the content.

**Suggested fix:** Rename to something like "Sequencing" or "A possible progression" — or just make it a paragraph under the options without its own heading.

---

### 10. Repetitive bullet-list openings in "Call for Use Cases" (Tell #3)

**Severity:** MED

**Location:** "Call for Use Cases" section

> - Do you run workloads where HPA-driven scale-down causes unnecessary pod disruption?
> - Do you use `pod-deletion-cost` annotations today? If so, what sets them and why?
> - Do you have workloads where certain replicas are clearly better candidates for removal...?
> - Do you run mixed workloads on shared nodes where some pods are much more expensive to disrupt...?
> - Are there scale-in scenarios specific to your domain...?

Every bullet starts with "Do you" or "Are there." This is the LLM question-list pattern — it reads like a survey form, not a conversation.

**Suggested rewrite:**

> Some things we're curious about: how you handle HPA-driven scale-down today, whether you use `pod-deletion-cost` annotations (and what sets them), whether you have replicas that are obviously better deletion candidates than others, and whether your domain (ML training, CI/CD, gaming, edge) has scale-in patterns we haven't considered.

One paragraph, conversational, same content.

---

### 11. Generic filler (Tell #11)

**Severity:** LOW

**Location:** Use Case 6 title and body

> ### 6. Condensing multiple signals into a single deletion priority

> Several of these use cases point to the same idea: there are multiple reasons to prefer deleting a pod on one node over another.

"Several of these use cases point to the same idea" is throat-clearing. The reader just read the use cases.

**Suggested rewrite:**

Before:
> Several of these use cases point to the same idea: there are multiple reasons to prefer deleting a pod on one node over another.

After:
> There are multiple reasons to prefer deleting a pod on one node over another.

---

## Summary

| Severity | Count | Key patterns |
|----------|-------|-------------|
| HIGH | 1 | Bolded lead-ins on every bullet (#2) |
| MED | 4 | Colon openers (#1), em dash density (#3), symmetrical options (#4), repetitive list openings (#10) |
| LOW | 6 | Hedging (#5), formulaic transitions (#6), outline-as-list (#7), parallel symmetry (#8), signpost heading (#9), generic filler (#11) |

The highest-impact fix is #2 (bolded lead-ins) — it's the single most recognizable LLM pattern and it's easy to fix. After that, breaking the option symmetry (#4) and converting the "Call for Use Cases" questions to prose (#10) would do the most to make this read like a human wrote it.

The document's actual *content* is solid. The problem statement is clear, the example is concrete, and the option analysis is honest. This is a de-styling pass, not a rewrite.
