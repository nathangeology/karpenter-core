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

package disruption_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

// Aspirational tests for Balanced Consolidation scoring (kp-mp5d.4, PR #2962).
//
// The "Balanced" consolidation policy uses a scoring model:
//   savings_fraction / disruption_fraction >= 1/threshold
//
// These tests validate boundary conditions that the scoring logic must handle:
// - Score exactly at threshold boundary (should approve)
// - Score epsilon below threshold (should reject)
// - Pods with negative deletion cost (should not block consolidation)
// - NodePool total changes mid-evaluation (should not use stale scores)
//
// All tests are expected to FAIL against current code because the "Balanced"
// consolidation policy has not yet been implemented.

var _ = Describe("Balanced Consolidation", func() {
	var nodePool *v1.NodePool

	BeforeEach(func() {
		nodePool = test.NodePool(v1.NodePool{
			Spec: v1.NodePoolSpec{
				Disruption: v1.Disruption{
					// This is the aspirational policy — currently not a valid enum value
					ConsolidationPolicy: v1.ConsolidationPolicy("Balanced"),
					ConsolidateAfter:    v1.MustParseNillableDuration("0s"),
					Budgets: []v1.Budget{{
						Nodes: "100%",
					}},
				},
			},
		})
	})

	Context("Threshold Boundary Scoring", func() {
		It("should approve consolidation when score is exactly at threshold boundary", func() {
			// Setup: A node using 50% of its capacity with savings_fraction/disruption_fraction = 1/threshold exactly.
			// The consolidation should be approved at the boundary.
			nodeClaim, node := test.NodeClaimAndNode(v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1.NodePoolLabelKey:            nodePool.Name,
						corev1.LabelInstanceTypeStable: mostExpensiveInstance.Name,
						v1.CapacityTypeLabelKey:        mostExpensiveOffering.Requirements.Get(v1.CapacityTypeLabelKey).Any(),
						corev1.LabelTopologyZone:       mostExpensiveOffering.Requirements.Get(corev1.LabelTopologyZone).Any(),
					},
				},
				Status: v1.NodeClaimStatus{
					Allocatable: map[corev1.ResourceName]resource.Quantity{
						corev1.ResourceCPU: resource.MustParse("32"),
					},
				},
			})
			nodeClaim.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)

			// Place a pod using exactly 50% of capacity — at the scoring threshold
			pod := test.Pod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("16"),
					},
				},
			})

			ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node, pod)
			ExpectManualBinding(ctx, env.Client, pod, node)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, []*corev1.Node{node}, []*v1.NodeClaim{nodeClaim})

			// The Balanced policy should exist and approve consolidation at the boundary
			ExpectSingletonReconciled(ctx, disruptionController)

			// Assert the node is approved for consolidation (disruption decision made)
			Expect(recorder.Calls("ConsolidationCandidate")).To(BeNumerically(">=", 1),
				"Node at exact threshold boundary should be approved for balanced consolidation")
		})

		It("should reject consolidation when score is epsilon below threshold", func() {
			// Setup: A node using just above the threshold — disruption cost too high relative to savings.
			// With a nearly-full node, the savings fraction is tiny but disruption fraction is high.
			nodeClaim, node := test.NodeClaimAndNode(v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1.NodePoolLabelKey:            nodePool.Name,
						corev1.LabelInstanceTypeStable: leastExpensiveInstance.Name,
						v1.CapacityTypeLabelKey:        leastExpensiveOffering.Requirements.Get(v1.CapacityTypeLabelKey).Any(),
						corev1.LabelTopologyZone:       leastExpensiveOffering.Requirements.Get(corev1.LabelTopologyZone).Any(),
					},
				},
				Status: v1.NodeClaimStatus{
					Allocatable: map[corev1.ResourceName]resource.Quantity{
						corev1.ResourceCPU: resource.MustParse("32"),
					},
				},
			})
			nodeClaim.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)

			// Pod using 31/32 CPUs — barely any savings possible, high disruption cost
			pod := test.Pod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("31"),
					},
				},
			})

			ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node, pod)
			ExpectManualBinding(ctx, env.Client, pod, node)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, []*corev1.Node{node}, []*v1.NodeClaim{nodeClaim})

			ExpectSingletonReconciled(ctx, disruptionController)

			// With Balanced policy, a nearly-full cheapest node should NOT be consolidated
			// because the savings/disruption ratio is below threshold
			Expect(recorder.Calls("ConsolidationCandidate")).To(Equal(0),
				"Node below scoring threshold should be rejected for balanced consolidation")
		})

		It("should not block consolidation when all pods have negative deletion cost", func() {
			// Pods with the controller.kubernetes.io/pod-deletion-cost annotation set negative
			// should make a node MORE attractive for consolidation, not less.
			nodeClaim, node := test.NodeClaimAndNode(v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1.NodePoolLabelKey:            nodePool.Name,
						corev1.LabelInstanceTypeStable: mostExpensiveInstance.Name,
						v1.CapacityTypeLabelKey:        mostExpensiveOffering.Requirements.Get(v1.CapacityTypeLabelKey).Any(),
						corev1.LabelTopologyZone:       mostExpensiveOffering.Requirements.Get(corev1.LabelTopologyZone).Any(),
					},
				},
				Status: v1.NodeClaimStatus{
					Allocatable: map[corev1.ResourceName]resource.Quantity{
						corev1.ResourceCPU: resource.MustParse("32"),
					},
				},
			})
			nodeClaim.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)

			// Pod with negative deletion cost — signals it's cheap to remove
			pod := test.Pod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"controller.kubernetes.io/pod-deletion-cost": "-100",
					},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("4"),
					},
				},
			})

			ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node, pod)
			ExpectManualBinding(ctx, env.Client, pod, node)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, []*corev1.Node{node}, []*v1.NodeClaim{nodeClaim})

			ExpectSingletonReconciled(ctx, disruptionController)

			// Negative deletion cost should make the score MORE favorable, not block consolidation
			Expect(recorder.Calls("ConsolidationCandidate")).To(BeNumerically(">=", 1),
				"Pods with negative deletion cost should not block balanced consolidation")
		})
	})

	Context("Mid-Cycle State Changes", func() {
		It("should not use stale scores when NodePool total changes mid-evaluation", func() {
			// Two nodes in the same pool. If the pool shrinks (node removed) during
			// scoring evaluation, the consolidation should use fresh state, not cached scores.
			nodeClaim1, node1 := test.NodeClaimAndNode(v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1.NodePoolLabelKey:            nodePool.Name,
						corev1.LabelInstanceTypeStable: mostExpensiveInstance.Name,
						v1.CapacityTypeLabelKey:        mostExpensiveOffering.Requirements.Get(v1.CapacityTypeLabelKey).Any(),
						corev1.LabelTopologyZone:       mostExpensiveOffering.Requirements.Get(corev1.LabelTopologyZone).Any(),
					},
				},
				Status: v1.NodeClaimStatus{
					Allocatable: map[corev1.ResourceName]resource.Quantity{
						corev1.ResourceCPU: resource.MustParse("32"),
					},
				},
			})
			nodeClaim2, node2 := test.NodeClaimAndNode(v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1.NodePoolLabelKey:            nodePool.Name,
						corev1.LabelInstanceTypeStable: mostExpensiveInstance.Name,
						v1.CapacityTypeLabelKey:        mostExpensiveOffering.Requirements.Get(v1.CapacityTypeLabelKey).Any(),
						corev1.LabelTopologyZone:       mostExpensiveOffering.Requirements.Get(corev1.LabelTopologyZone).Any(),
					},
				},
				Status: v1.NodeClaimStatus{
					Allocatable: map[corev1.ResourceName]resource.Quantity{
						corev1.ResourceCPU: resource.MustParse("32"),
					},
				},
			})
			nodeClaim1.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)
			nodeClaim2.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)

			// Light workload on both nodes
			pod1 := test.Pod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
				},
			})
			pod2 := test.Pod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
				},
			})

			ExpectApplied(ctx, env.Client, nodePool, nodeClaim1, node1, nodeClaim2, node2, pod1, pod2)
			ExpectManualBinding(ctx, env.Client, pod1, node1)
			ExpectManualBinding(ctx, env.Client, pod2, node2)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController,
				[]*corev1.Node{node1, node2}, []*v1.NodeClaim{nodeClaim1, nodeClaim2})

			// First reconcile computes scores for both nodes
			ExpectSingletonReconciled(ctx, disruptionController)

			// Simulate mid-cycle change: delete one node
			ExpectDeleted(ctx, env.Client, nodeClaim2, node2)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController,
				[]*corev1.Node{node1}, []*v1.NodeClaim{nodeClaim1})

			// Mark cluster as unconsolidated to force re-evaluation
			cluster.MarkUnconsolidated()

			// Second reconcile should compute fresh scores, not reuse stale state
			ExpectSingletonReconciled(ctx, disruptionController)

			// The controller should not have made a stale decision based on pre-deletion state.
			// With only one node remaining and its workload needing to go somewhere,
			// consolidation should be reconsidered with fresh data.
		})
	})
})
