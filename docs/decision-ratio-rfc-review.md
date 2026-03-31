# Decision Ratio RFC Review: jamesmt-aws/karpenter#4

Review of the "Consolidation Cost Threshold" RFC (PR #4, now superseded by PR #8
renamed to "Balanced Consolidation Policy").

## RFC Design Intent

The RFC proposes a new `consolidationPolicy: Balanced` that scores each consolidation
move by comparing the fraction of NodePool cost saved to the fraction of NodePool
disruption incurred:

```
score = savings_fraction / disruption_fraction
```

Where:
- `savings_fraction = (deleted_node_cost - created_node_cost) / nodepool_total_cost`
- `disruption_fraction = move_disruption_cost / nodepool_total_disruption_cost`
- Per-pod disruption cost comes from existing `EvictionCost` (pod-deletion-cost annotation + priority), default 1.0 per pod
- A move is approved when `score >= 1/k`, where k = `disruptionTolerance` (default 2, threshold 0.5)

The three policies form a spectrum: `WhenEmpty` (k=0), `Balanced` (k=2), `WhenEmptyOrUnderutilized` (k=∞).

## Key Discussion Points from PR Comments

### 1. Kube-scheduler placement gap (shreyas-badiger, jamesmt-aws)
The most substantive concern: Karpenter assumes pods land on intended destinations, but
kube-scheduler may scatter them. Example: Karpenter provisions node K to consolidate B+C,
but scheduler distributes pods across D-J instead. K ends up empty, triggering another
consolidation cycle. James acknowledges the threshold reduces but doesn't eliminate this.
MostAllocated scheduling strategy is the real fix.

### 2. Reserved/ODCR opportunity cost (bwagner5, mmanders-amzn, jamesmt-aws)
Zero-cost nodes produce zero savings → never consolidated by scoring. The emptiness
controller still handles empty zero-cost nodes. Real opportunity cost modeling deferred —
would require billing API integration. `WhenEmptyOrUnderutilized` remains the answer for
operators who want to consolidate zero-cost capacity.

### 3. Disruption cost granularity (bwagner5, jamesmt-aws)
Discussion on whether `terminationGracePeriod` or historical metrics should feed into
disruption cost. James argues the existing `pod-deletion-cost` annotation is the right
escape hatch — no inference can capture application-specific restart pain. Consensus
landed on Low/Medium/High as practical values (mapping to 0, 1, 10).

### 4. Uniform cluster behavior (mmanders-amzn, jamesmt-aws)
In a uniformly inefficient cluster, every REPLACE may score identically and below
threshold. James added FAQ: DELETEs still work (score exactly 1.0 for identical nodes),
REPLACEs that save < 50% of source node cost are correctly rejected. This is by design.

### 5. Per-NodePool vs per-cluster normalization (mmanders-amzn, jamesmt-aws)
Per-NodePool chosen to match existing architecture. Prevents large pools from diluting
small pool scores. Cross-NodePool moves use source pool's policy and totals.

### 6. Score visibility (bwagner5, jamesmt-aws)
Score surfaced three ways: DEBUG logs, Prometheus histogram (`karpenter_consolidation_score`),
and events on NodeClaim.

### 7. Superseded by PR #8
PR #4 was closed in favor of PR #8 which renamed to "Balanced", added `disruptionTolerance`
parameter, threshold verification section, and file renames.

## Comparison to Observed KWOK Simulator Behavior

The bead references three observations from our KWOK simulation work:

### Uniform ratio of 1.0 for identical nodes in a uniform fleet
**Confirmed by RFC design.** In a uniform pool where all nodes are the same instance type
with equal pod counts, every DELETE scores exactly 1.0:
```
savings_fraction = node_cost / (N * node_cost) = 1/N
disruption_fraction = node_disruption / (N * node_disruption) = 1/N
score = (1/N) / (1/N) = 1.0
```
This is algebraically guaranteed — fleet size cancels out (Property 7 in the RFC).
For REPLACEs in a uniform pool, the score simplifies to `1 - replacement_price / node_price`,
which is always < 1.0 (you can't replace with a free node).

### Binary gate behavior (threshold ≤1.0 = pass all, >1.0 = block all)
**Confirmed for uniform fleets.** When all nodes produce the same score (1.0 for DELETEs),
the threshold acts as a binary gate: any threshold ≤ 1.0 passes every DELETE, any
threshold > 1.0 blocks every DELETE. There is no gradient to exploit. This is why k=1
(threshold 1.0) is a design hole — it's the exact boundary where uniform DELETEs barely
pass and no REPLACE ever passes.

### No gradient at scale with homogeneous nodes
**Confirmed as intentional.** The RFC's scale invariance property (identical scores
regardless of pool size) means homogeneous fleets produce no differentiation between
nodes. The gradient emerges only from heterogeneity:
- Different instance types → different cost fractions
- Different pod counts or disruption costs → different disruption fractions
- Mixed workloads (pod-deletion-cost annotations) → per-node disruption differentiation

The RFC's "Heterogeneous Disruption Cost" example demonstrates this: two nodes with
identical cost and pod count score 2.24 vs 0.29 based solely on pod disruption cost
differences.

## Implications for Simulator Calibration

1. **Homogeneous fleets won't exercise the scoring gradient.** KWOK simulations must use
   mixed instance types and varied pod disruption costs to see meaningful score
   differentiation. A uniform m7i.xlarge fleet will always produce score=1.0 for DELETEs.

2. **k=2 is the minimum useful value.** At k=1, no REPLACE passes in uniform pools. Our
   simulator should default to k=2 and test k=3 for cross-family replacement scenarios.

3. **The kube-scheduler gap is the biggest real-world risk.** The RFC acknowledges this
   but doesn't solve it. Our KWOK simulator should model scheduler placement divergence
   to quantify churn from scattered pod placement.

4. **Pod disruption cost is the primary differentiation lever.** Without `pod-deletion-cost`
   annotations, the score reduces to pod-count-vs-savings. Simulations should include
   varied disruption costs to test the full scoring range.

5. **Cross-NodePool moves use source pool normalization.** Multi-pool simulations need to
   verify that source-pool-centric scoring produces correct decisions when pools have
   very different cost structures (e.g., on-demand vs spot).

## Suggested Improvements from Discussion

1. **MostAllocated scheduling** — Repeatedly raised as the real fix for the scheduler
   placement gap. Should be a prerequisite or co-launch with Balanced consolidation.

2. **Automated pod-deletion-cost** — PR #2894 proposes a controller to set this
   automatically. Without it, most clusters will have uniform disruption cost=1.0,
   reducing the score to a simple pod-count ratio.

3. **Per-node baseline disruption cost** — Open question in the RFC. Adding a fixed
   overhead per node (cordon/drain/delete cost) would prevent empty-node DELETEs from
   having zero disruption cost. Not yet validated.

4. **Move quality tracking** — Annotate moved pods with the move's score, track
   premature re-disruption rates. This would directly measure whether the threshold
   is calibrated correctly.

## References

- RFC PR #4 (closed): https://github.com/jamesmt-aws/karpenter/pull/4
- Superseding PR #8 (open): https://github.com/jamesmt-aws/karpenter/pull/8
- Automated pod-deletion-cost: https://github.com/kubernetes-sigs/karpenter/pull/2894
- Decision ratio threshold PR: https://github.com/kubernetes-sigs/karpenter/pull/2893
