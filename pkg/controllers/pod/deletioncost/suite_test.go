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

package deletioncost_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/pod/deletioncost"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

func TestDeletionCost(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "PodDeletionCost")
}

var _ = Describe("Pod Deletion Cost Integration", func() {
	var ctx context.Context
	var env *Environment
	var rankingEngine *deletioncost.RankingEngine
	var annotationMgr *deletioncost.AnnotationManager

	BeforeEach(func() {
		ctx = TestContextWithLogger(GinkgoT())
		env = NewEnvironment(ctx, GinkgoT())
		annotationMgr = deletioncost.NewAnnotationManager(env.Client)
	})

	AfterEach(func() {
		env.Cleanup()
	})

	Context("End-to-End Workflow", func() {
		It("should rank nodes and update pod deletion costs", func() {
			rankingEngine = deletioncost.NewRankingEngine(deletioncost.RankingStrategyLargestToSmallest)

			// Create nodes with different sizes
			smallNode := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "small-node"},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
			})
			largeNode := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "large-node"},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("16"),
					corev1.ResourceMemory: resource.MustParse("64Gi"),
				},
			})
			env.ExpectCreated(smallNode, largeNode)

			// Create pods on each node
			smallPod := test.Pod(test.PodOptions{NodeName: smallNode.Name})
			largePod := test.Pod(test.PodOptions{NodeName: largeNode.Name})
			env.ExpectCreated(smallPod, largePod)

			// Rank nodes
			nodes := []*state.StateNode{
				state.NewNode(smallNode),
				state.NewNode(largeNode),
			}
			ranks, err := rankingEngine.RankNodes(ctx, nodes)
			Expect(err).ToNot(HaveOccurred())

			// Update pod annotations
			err = annotationMgr.UpdatePodDeletionCosts(ctx, ranks)
			Expect(err).ToNot(HaveOccurred())

			// Verify pods have correct deletion costs
			updatedSmallPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(smallPod), updatedSmallPod)).To(Succeed())

			updatedLargePod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(largePod), updatedLargePod)).To(Succeed())

			// Large node should have lower deletion cost (deleted first)
			smallCost := updatedSmallPod.Annotations[deletioncost.PodDeletionCostAnnotation]
			largeCost := updatedLargePod.Annotations[deletioncost.PodDeletionCostAnnotation]

			Expect(smallCost).ToNot(BeEmpty())
			Expect(largeCost).ToNot(BeEmpty())

			// Parse and compare (large should be numerically less)
			var smallCostInt, largeCostInt int
			_, err = fmt.Sscanf(smallCost, "%d", &smallCostInt)
			Expect(err).ToNot(HaveOccurred())
			_, err = fmt.Sscanf(largeCost, "%d", &largeCostInt)
			Expect(err).ToNot(HaveOccurred())

			Expect(largeCostInt).To(BeNumerically("<", smallCostInt))
		})

		It("should handle do-not-disrupt pods correctly in workflow", func() {
			rankingEngine = deletioncost.NewRankingEngine(deletioncost.RankingStrategyRandom)

			// Create normal node
			normalNode := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "normal-node"},
			})

			// Create node with do-not-disrupt pod
			dndNode := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "dnd-node"},
			})

			env.ExpectCreated(normalNode, dndNode)

			// Create normal pod
			normalPod := test.Pod(test.PodOptions{NodeName: normalNode.Name})

			// Create do-not-disrupt pod
			dndPod := test.Pod(test.PodOptions{
				NodeName: dndNode.Name,
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						v1.DoNotDisruptAnnotationKey: "true",
					},
				},
			})

			env.ExpectCreated(normalPod, dndPod)

			// Build state nodes with pods
			normalStateNode := state.NewNode(normalNode)
			normalStateNode.Pods[string(normalPod.UID)] = normalPod

			dndStateNode := state.NewNode(dndNode)
			dndStateNode.Pods[string(dndPod.UID)] = dndPod

			nodes := []*state.StateNode{normalStateNode, dndStateNode}

			// Rank nodes
			ranks, err := rankingEngine.RankNodes(ctx, nodes)
			Expect(err).ToNot(HaveOccurred())

			// Update pod annotations
			err = annotationMgr.UpdatePodDeletionCosts(ctx, ranks)
			Expect(err).ToNot(HaveOccurred())

			// Verify normal pod has lower deletion cost than do-not-disrupt pod
			updatedNormalPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(normalPod), updatedNormalPod)).To(Succeed())

			updatedDndPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(dndPod), updatedDndPod)).To(Succeed())

			normalCost := updatedNormalPod.Annotations[deletioncost.PodDeletionCostAnnotation]
			dndCost := updatedDndPod.Annotations[deletioncost.PodDeletionCostAnnotation]

			var normalCostInt, dndCostInt int
			_, err = fmt.Sscanf(normalCost, "%d", &normalCostInt)
			Expect(err).ToNot(HaveOccurred())
			_, err = fmt.Sscanf(dndCost, "%d", &dndCostInt)
			Expect(err).ToNot(HaveOccurred())

			// Normal pod should have lower cost (deleted first)
			Expect(normalCostInt).To(BeNumerically("<", dndCostInt))
		})

		It("should respect customer-managed annotations in workflow", func() {
			rankingEngine = deletioncost.NewRankingEngine(deletioncost.RankingStrategyRandom)

			node := test.Node()
			env.ExpectCreated(node)

			// Create customer-managed pod
			customerPod := test.Pod(test.PodOptions{
				NodeName: node.Name,
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						deletioncost.PodDeletionCostAnnotation: "999",
					},
				},
			})

			// Create Karpenter-managed pod
			karpenterPod := test.Pod(test.PodOptions{NodeName: node.Name})

			env.ExpectCreated(customerPod, karpenterPod)

			// Build state node
			stateNode := state.NewNode(node)
			stateNode.Pods[string(customerPod.UID)] = customerPod
			stateNode.Pods[string(karpenterPod.UID)] = karpenterPod

			// Rank and update
			ranks, err := rankingEngine.RankNodes(ctx, []*state.StateNode{stateNode})
			Expect(err).ToNot(HaveOccurred())

			err = annotationMgr.UpdatePodDeletionCosts(ctx, ranks)
			Expect(err).ToNot(HaveOccurred())

			// Verify customer pod unchanged
			updatedCustomerPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(customerPod), updatedCustomerPod)).To(Succeed())
			Expect(updatedCustomerPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("999"))
			Expect(updatedCustomerPod.Annotations).ToNot(HaveKey(deletioncost.KarpenterManagedDeletionCostKey))

			// Verify Karpenter pod updated
			updatedKarpenterPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(karpenterPod), updatedKarpenterPod)).To(Succeed())
			Expect(updatedKarpenterPod.Annotations).To(HaveKey(deletioncost.PodDeletionCostAnnotation))
			Expect(updatedKarpenterPod.Annotations).To(HaveKey(deletioncost.KarpenterManagedDeletionCostKey))
		})
	})

	Context("Different Ranking Strategies", func() {
		It("should produce different results for different strategies", func() {
			// Create nodes with different characteristics
			smallNode := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "small"},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
			})
			largeNode := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "large"},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("16"),
					corev1.ResourceMemory: resource.MustParse("64Gi"),
				},
			})

			nodes := []*state.StateNode{
				state.NewNode(smallNode),
				state.NewNode(largeNode),
			}

			// Test LargestToSmallest
			largestFirst := deletioncost.NewRankingEngine(deletioncost.RankingStrategyLargestToSmallest)
			ranksLargestFirst, err := largestFirst.RankNodes(ctx, nodes)
			Expect(err).ToNot(HaveOccurred())

			// Test SmallestToLargest
			smallestFirst := deletioncost.NewRankingEngine(deletioncost.RankingStrategySmallestToLargest)
			ranksSmallestFirst, err := smallestFirst.RankNodes(ctx, nodes)
			Expect(err).ToNot(HaveOccurred())

			// Find ranks for each node in each strategy
			largestFirstMap := make(map[string]int)
			smallestFirstMap := make(map[string]int)

			for _, rank := range ranksLargestFirst {
				largestFirstMap[rank.Node.Node.Name] = rank.Rank
			}
			for _, rank := range ranksSmallestFirst {
				smallestFirstMap[rank.Node.Node.Name] = rank.Rank
			}

			// Verify opposite ordering
			// LargestToSmallest: large < small
			Expect(largestFirstMap["large"]).To(BeNumerically("<", largestFirstMap["small"]))

			// SmallestToLargest: small < large
			Expect(smallestFirstMap["small"]).To(BeNumerically("<", smallestFirstMap["large"]))
		})
	})

	Context("Change Detection Integration", func() {
		It("should detect changes correctly in workflow", func() {
			changeDetector := deletioncost.NewChangeDetector()

			node := test.Node()
			stateNode := state.NewNode(node)

			nodes := []*state.StateNode{stateNode}

			// First check
			hasChanged := changeDetector.HasChanged(nodes)
			Expect(hasChanged).To(BeTrue())

			// No change
			hasChanged = changeDetector.HasChanged(nodes)
			Expect(hasChanged).To(BeFalse())

			// Add pod
			pod := test.Pod(test.PodOptions{})
			stateNode.Pods[string(pod.UID)] = pod

			hasChanged = changeDetector.HasChanged(nodes)
			Expect(hasChanged).To(BeTrue())
		})
	})
})
