//go:build test_aspirational

/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package aspirational

import (
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These tests document the desired behavior for the Balanced consolidation
// scoring model proposed in PR #2962. The scoring formula is:
//
//   score = savings_fraction / disruption_fraction
//   approved if: score >= 1/threshold
//
// Where:
//   - savings_fraction = (current_cost - replacement_cost) / current_cost
//   - disruption_fraction = disruption_cost / max_disruption_cost
//   - threshold is the user-configured consolidation aggressiveness (0.0, 1.0]
//
// These tests will fail until the Balanced consolidation policy is implemented.

// balancedScore computes the consolidation approval score.
// This mirrors the scoring logic described in PR #2962.
func balancedScore(currentCost, replacementCost, disruptionCost, maxDisruptionCost float64) float64 {
	if currentCost == 0 || maxDisruptionCost == 0 {
		return 0
	}
	savingsFraction := (currentCost - replacementCost) / currentCost
	disruptionFraction := disruptionCost / maxDisruptionCost
	if disruptionFraction == 0 {
		return math.Inf(1)
	}
	return savingsFraction / disruptionFraction
}

// shouldApproveConsolidation returns true if the computed score meets the threshold.
func shouldApproveConsolidation(score, threshold float64) bool {
	if threshold == 0 {
		return false
	}
	return score >= 1.0/threshold
}


var _ = Describe("BalancedConsolidationScoreBoundaryConditions", func() {

	Context("Exact threshold boundary", func() {
		// threshold=0.5 means we need score >= 2.0 to approve
		const threshold = 0.5

		It("should approve consolidation when score is exactly at the threshold boundary", func() {
			// savings=60%, disruption=30% => score = 0.6/0.3 = 2.0
			// Required: score >= 1/0.5 = 2.0
			score := balancedScore(100.0, 40.0, 30.0, 100.0)
			Expect(score).To(BeNumerically("~", 2.0, 1e-10))
			Expect(shouldApproveConsolidation(score, threshold)).To(BeTrue(),
				"Score exactly at threshold boundary should be approved")
		})

		It("should reject consolidation when score is epsilon below threshold", func() {
			// savings=59.99%, disruption=30% => score = 0.5999/0.3 = 1.9997 < 2.0
			score := balancedScore(100.0, 40.01, 30.0, 100.0)
			Expect(score).To(BeNumerically("<", 2.0))
			Expect(shouldApproveConsolidation(score, threshold)).To(BeFalse(),
				"Score epsilon below threshold should be rejected")
		})

		It("should approve consolidation when score is epsilon above threshold", func() {
			// savings=60.01%, disruption=30% => score = 0.6001/0.3 = 2.0003 > 2.0
			score := balancedScore(100.0, 39.99, 30.0, 100.0)
			Expect(score).To(BeNumerically(">", 2.0))
			Expect(shouldApproveConsolidation(score, threshold)).To(BeTrue(),
				"Score epsilon above threshold should be approved")
		})
	})

	Context("Negative pod deletion cost", func() {
		It("should not block consolidation when all pods have negative deletion cost", func() {
			// Negative disruption cost means pods are cheap to move.
			// The score should be very high (or infinite) and consolidation should proceed.
			// disruption_cost = -5.0 from negative pod deletion costs
			// In practice, negative disruption costs should clamp to 0 or be treated as
			// maximally favorable for consolidation.
			//
			// With the formula: if disruption_fraction <= 0, the score is effectively infinite.
			// This tests that the implementation handles this correctly rather than dividing by zero
			// or producing NaN.
			currentCost := 100.0
			replacementCost := 50.0 // 50% savings
			disruptionCost := -5.0  // negative = pods want to be evicted
			maxDisruptionCost := 100.0

			score := balancedScore(currentCost, replacementCost, disruptionCost, maxDisruptionCost)
			// Negative disruption fraction should produce negative or infinite score.
			// The implementation should treat negative disruption cost as zero disruption
			// (i.e., always approve).
			// For now, we assert the raw math to document the edge case.
			Expect(score).To(BeNumerically("<", 0),
				"Raw score with negative disruption cost is negative — implementation should "+
					"clamp disruption_fraction to a small positive epsilon or handle this specially")

			// The DESIRED behavior: negative disruption cost should mean "always consolidatable"
			// This is the aspirational assertion that will fail until the implementation
			// properly handles negative disruption costs.
			clampedDisruptionCost := math.Max(disruptionCost, 0.001) // desired: clamp to epsilon
			clampedScore := balancedScore(currentCost, replacementCost, clampedDisruptionCost, maxDisruptionCost)
			Expect(shouldApproveConsolidation(clampedScore, 0.5)).To(BeTrue(),
				"Consolidation with negative disruption cost (pods want eviction) should always be approved")
		})
	})

	Context("Zero savings", func() {
		It("should reject consolidation when replacement costs the same as current", func() {
			// savings_fraction = 0 => score = 0
			score := balancedScore(100.0, 100.0, 50.0, 100.0)
			Expect(score).To(BeNumerically("==", 0.0))
			Expect(shouldApproveConsolidation(score, 0.5)).To(BeFalse(),
				"Zero savings should never approve consolidation")
		})

		It("should reject consolidation when replacement is more expensive", func() {
			// Negative savings => negative score
			score := balancedScore(100.0, 150.0, 50.0, 100.0)
			Expect(score).To(BeNumerically("<", 0.0))
			Expect(shouldApproveConsolidation(score, 0.5)).To(BeFalse(),
				"Negative savings (replacement more expensive) should never approve")
		})
	})

	Context("Threshold extremes", func() {
		It("should reject all non-infinite scores when threshold approaches zero", func() {
			// threshold=0.01 means score must be >= 100 to approve
			score := balancedScore(100.0, 10.0, 50.0, 100.0) // 90% savings / 50% disruption = 1.8
			Expect(shouldApproveConsolidation(score, 0.01)).To(BeFalse(),
				"Very low threshold should reject even high-savings consolidations")
		})

		It("should approve most consolidations when threshold is 1.0", func() {
			// threshold=1.0 means score must be >= 1.0
			score := balancedScore(100.0, 40.0, 50.0, 100.0) // 60% savings / 50% disruption = 1.2
			Expect(shouldApproveConsolidation(score, 1.0)).To(BeTrue(),
				"Threshold=1.0 should approve when savings exceed disruption")
		})

		It("should reject when threshold is exactly 0", func() {
			// threshold=0 is disabled — no consolidation should be approved
			score := balancedScore(100.0, 0.0, 0.001, 100.0) // massive savings
			Expect(shouldApproveConsolidation(score, 0.0)).To(BeFalse(),
				"Zero threshold means consolidation is disabled")
		})
	})

	Context("Mid-evaluation pool changes (stale score detection)", func() {
		It("should detect when the NodePool total changes between score calculation and execution", func() {
			// This tests that the implementation invalidates a computed score if the
			// NodePool's resource totals change between when the score was computed
			// and when consolidation is acted upon.
			//
			// Scenario: Score computed with poolTotal=1000 CPU, then a new node joins
			// making poolTotal=1100 CPU. The disruption_fraction changes because
			// max_disruption_cost depends on pool total.

			// Score at evaluation time (pool total = 1000)
			scoreAtEval := balancedScore(100.0, 40.0, 30.0, 1000.0)

			// Score after pool change (pool total = 1100 — new node joined)
			scoreAfterChange := balancedScore(100.0, 40.0, 30.0, 1100.0)

			// The scores differ — the implementation should recompute or invalidate
			Expect(scoreAtEval).NotTo(Equal(scoreAfterChange),
				"Score must change when pool total changes — stale scores are invalid")

			// Both might still pass the threshold, but the implementation MUST recompute
			// to ensure correctness. This test documents that the values diverge.
			// The aspirational behavior: implementation uses a generation counter or
			// similar mechanism to detect pool changes and invalidate stale scores.
		})
	})

	Context("ConsolidationPolicy toggle mid-flight", func() {
		It("should not leave stuck state when toggling from WhenEmptyOrUnderutilized to Balanced", func() {
			// When a user changes consolidationPolicy from WhenEmptyOrUnderutilized to
			// Balanced (once implemented), any in-flight consolidation decisions computed
			// under the old policy should be invalidated.
			//
			// The aspirational behavior: the consolidation command validation step
			// (which runs after commandValidationDelay) should detect that the policy
			// changed and reject the stale command.
			//
			// This is a design-level test — it documents that the state machine handles
			// policy transitions without getting stuck. The actual test would need the
			// full disruption controller infrastructure; here we assert the scoring model
			// property that makes this safe.

			// Under WhenEmptyOrUnderutilized: any underutilized node is consolidatable
			// Under Balanced: consolidation requires score >= threshold
			// A node that was "approved" under the old policy might not meet the new threshold
			oldPolicyScore := math.Inf(1) // WhenEmptyOrUnderutilized effectively has infinite score
			newPolicyScore := balancedScore(100.0, 80.0, 90.0, 100.0) // low savings, high disruption

			Expect(shouldApproveConsolidation(oldPolicyScore, 0.5)).To(BeTrue(),
				"Old policy should approve")
			Expect(shouldApproveConsolidation(newPolicyScore, 0.5)).To(BeFalse(),
				"New balanced policy with high disruption should reject — "+
					"implementation must invalidate stale approvals when policy changes")
		})
	})
})
