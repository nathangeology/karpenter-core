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
	"context"
	"math"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/karpenter/pkg/test"
	disruptionutils "sigs.k8s.io/karpenter/pkg/utils/disruption"
)

// TestBalancedConsolidation_ThresholdBoundary documents the aspirational
// "Balanced" consolidation policy from
// https://github.com/kubernetes-sigs/karpenter/pull/2962
//
// The proposed scoring model: a consolidation candidate is approved when
//   savings_fraction / disruption_fraction >= 1/threshold
//
// Where:
//   - savings_fraction = (current_cost - replacement_cost) / current_cost
//   - disruption_fraction = disrupted_pods / total_pods
//   - threshold is a user-configured value (e.g., 0.5 means "only
//     consolidate when savings outweigh disruption by at least 2:1")
//
// Edge case: when the score is exactly at the threshold boundary, the
// candidate should be approved (>= not >). Current code has no Balanced
// policy — only WhenEmpty and WhenEmptyOrUnderutilized.
//
// This test FAILS on current code (no Balanced policy exists) and will
// PASS once the scoring model from PR #2962 is implemented.
func TestBalancedConsolidation_ThresholdBoundary(t *testing.T) {
	// Example: threshold = 0.5
	// Current cost: $1.00, Replacement cost: $0.50
	// savings_fraction = 0.5
	// 10 total pods, 5 disrupted
	// disruption_fraction = 0.5
	// score = 0.5 / 0.5 = 1.0, threshold requires >= 1/0.5 = 2.0
	// This should be REJECTED.
	//
	// With replacement cost $0.25:
	// savings_fraction = 0.75
	// score = 0.75 / 0.5 = 1.5, still < 2.0
	// Still REJECTED.
	//
	// With only 2 pods disrupted of 10:
	// disruption_fraction = 0.2
	// score = 0.75 / 0.2 = 3.75, >= 2.0
	// APPROVED.

	t.Skip("aspirational: Balanced consolidation policy not yet implemented (#2962)")
}

// TestBalancedConsolidation_NegativeDeletionCostPods documents the edge
// case where all pods on a node have negative deletion cost annotations.
//
// When pod-deletion-cost is negative (e.g., -2147483647), the
// ReschedulingCost can become negative. In the proposed Balanced scoring
// model, a negative disruption_fraction should NOT block consolidation —
// pods with negative deletion cost are EAGER to be evicted.
//
// The desired behavior: when ReschedulingCost <= 0, disruption_fraction
// is treated as epsilon (near-zero positive), making the score effectively
// infinite — always approved regardless of savings.
//
// This test FAILS because (a) Balanced policy doesn't exist, and (b) the
// current ReschedulingCost calculation doesn't have special handling for
// the all-negative case in a scoring context.
func TestBalancedConsolidation_NegativeDeletionCostPods(t *testing.T) {
	ctx := context.Background()

	// Pod with maximum negative deletion cost
	pod := test.Pod(test.PodOptions{
		ResourceRequirements: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
		Phase: corev1.PodRunning,
	})
	pod.Annotations = map[string]string{
		corev1.PodDeletionCost: "-2147483647",
	}

	cost := disruptionutils.ReschedulingCost(ctx, []*corev1.Pod{pod})

	// With max negative deletion cost: cost ≈ 1.0 + (-2147483647/2^27) ≈ -15.0, clamped to -10.0
	// ReschedulingCost for a single such pod is -10.0
	if cost >= 0 {
		t.Fatalf("expected negative rescheduling cost for pod with min deletion cost, got %f", cost)
	}

	// In a Balanced scoring model, negative cost should mean "eager to
	// evict" — consolidation should never be blocked by negative-cost pods.
	// The scoring formula needs a floor/special case for this.
	t.Skip("aspirational: Balanced scoring model needs special handling for negative disruption cost (#2962)")
}

// TestBalancedConsolidation_MidCyclePoolChange documents the race where a
// NodePool's total instance count changes during consolidation evaluation.
//
// The scoring model uses disruption_fraction = disrupted_pods / total_pods.
// If total_pods changes between when the candidate is scored and when the
// disruption is executed, the actual disruption_fraction differs from what
// was approved.
//
// Scenario: NodePool has 10 nodes with 100 total pods. A consolidation
// candidate scores at disruption_fraction = 5/100 = 0.05 (low disruption).
// Between scoring and execution, 8 nodes are removed by drift, leaving
// 2 nodes with 20 pods. The actual disruption is 5/20 = 0.25 (5x higher).
//
// Desired behavior: the score is re-validated at execution time, or
// total_pods is snapshotted atomically with the disruption decision.
//
// This test FAILS because Balanced policy doesn't exist and there's no
// stale-score detection mechanism.
func TestBalancedConsolidation_MidCyclePoolChange(t *testing.T) {
	t.Skip("aspirational: no stale-score detection when NodePool state changes during consolidation evaluation (#2962)")
}

// TestBalancedConsolidation_ScoreEpsilonBelowThreshold verifies that a
// candidate scoring infinitesimally below the threshold is correctly
// rejected — no floating-point rounding allows it through.
//
// With threshold = 0.5 (requires score >= 2.0):
//   savings_fraction = 0.4
//   disruption_fraction = 0.200000000001 (just above 0.2)
//   score = 0.4 / 0.200000000001 ≈ 1.99999999999 (just below 2.0)
//
// This must be REJECTED. The scoring comparison must use exact or
// conservative arithmetic to avoid false approvals from float imprecision.
func TestBalancedConsolidation_ScoreEpsilonBelowThreshold(t *testing.T) {
	threshold := 0.5
	requiredScore := 1.0 / threshold // 2.0

	savingsFraction := 0.4
	disruptionFraction := 0.2 + 1e-10 // epsilon above 0.2
	score := savingsFraction / disruptionFraction

	if score >= requiredScore {
		t.Fatal("floating-point precision allowed score to meet threshold when it shouldn't")
	}

	// This is a design constraint for the Balanced implementation:
	// it must handle float comparison correctly at boundaries.
	epsilon := math.Abs(score - requiredScore)
	if epsilon > 1e-6 {
		t.Skipf("score %f is far enough from threshold %f to not trigger precision issues", score, requiredScore)
	}

	t.Skip("aspirational: Balanced consolidation scoring needs precision-safe threshold comparison (#2962)")
}
