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

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/pod/deletioncost"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/test"
)

func TestRanking(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "PodDeletionCost/Ranking")
}

var _ = Describe("Node Ranking Engine", func() {
	var ctx context.Context
	var rankingEngine *deletioncost.RankingEngine

	BeforeEach(func() {
		ctx = context.Background()
	})

	Context("Random Strategy", func() {
		BeforeEach(func() {
			rankingEngine = deletioncost.NewRankingEngine(deletioncost.RankingStrategyRandom)
		})

		It("should assign sequential ranks to all nodes", func() {
			nodes := createTestNodes(5, false)
			ranks, err := rankingEngine.RankNodes(ctx, nodes)

			Expect(err).ToNot(HaveOccurred())
			Expect(ranks).To(HaveLen(5))

			// Verify all ranks are unique
			rankSet := make(map[int]bool)
			for _, rank := range ranks {
				Expect(rankSet[rank.Rank]).To(BeFalse(), "Ranks should be unique")
				rankSet[rank.Rank] = true
			}
		})

		It("should handle empty node list", func() {
			nodes := []*state.StateNode{}
			ranks, err := rankingEngine.RankNodes(ctx, nodes)

			Expect(err).ToNot(HaveOccurred())
			Expect(ranks).To(BeEmpty())
		})

		It("should handle single node", func() {
			nodes := createTestNodes(1, false)
			ranks, err := rankingEngine.RankNodes(ctx, nodes)

			Expect(err).ToNot(HaveOccurred())
			Expect(ranks).To(HaveLen(1))
		})
	})

	Context("LargestToSmallest Strategy", func() {
		BeforeEach(func() {
			rankingEngine = deletioncost.NewRankingEngine(deletioncost.RankingStrategyLargestToSmallest)
		})

		It("should rank larger nodes with lower ranks (deleted first)", func() {
			nodes := []*state.StateNode{
				createNodeWithCapacity("small", "2", "4Gi"),
				createNodeWithCapacity("large", "16", "64Gi"),
				createNodeWithCapacity("medium", "8", "32Gi"),
			}

			ranks, err := rankingEngine.RankNodes(ctx, nodes)
			Expect(err).ToNot(HaveOccurred())
			Expect(ranks).To(HaveLen(3))

			// Find ranks by node name
			rankMap := make(map[string]int)
			for _, rank := range ranks {
				rankMap[rank.Node.Node.Name] = rank.Rank
			}

			// Large node should have lowest rank (deleted first)
			Expect(rankMap["large"]).To(BeNumerically("<", rankMap["medium"]))
			Expect(rankMap["medium"]).To(BeNumerically("<", rankMap["small"]))
		})

		It("should handle nodes with identical capacity deterministically", func() {
			nodes := []*state.StateNode{
				createNodeWithCapacity("node-a", "4", "16Gi"),
				createNodeWithCapacity("node-b", "4", "16Gi"),
				createNodeWithCapacity("node-c", "4", "16Gi"),
			}

			ranks, err := rankingEngine.RankNodes(ctx, nodes)
			Expect(err).ToNot(HaveOccurred())
			Expect(ranks).To(HaveLen(3))

			// All ranks should be unique even with identical capacity
			rankSet := make(map[int]bool)
			for _, rank := range ranks {
				Expect(rankSet[rank.Rank]).To(BeFalse())
				rankSet[rank.Rank] = true
			}
		})
	})

	Context("SmallestToLargest Strategy", func() {
		BeforeEach(func() {
			rankingEngine = deletioncost.NewRankingEngine(deletioncost.RankingStrategySmallestToLargest)
		})

		It("should rank smaller nodes with lower ranks (deleted first)", func() {
			nodes := []*state.StateNode{
				createNodeWithCapacity("small", "2", "4Gi"),
				createNodeWithCapacity("large", "16", "64Gi"),
				createNodeWithCapacity("medium", "8", "32Gi"),
			}

			ranks, err := rankingEngine.RankNodes(ctx, nodes)
			Expect(err).ToNot(HaveOccurred())
			Expect(ranks).To(HaveLen(3))

			rankMap := make(map[string]int)
			for _, rank := range ranks {
				rankMap[rank.Node.Node.Name] = rank.Rank
			}

			// Small node should have lowest rank (deleted first)
			Expect(rankMap["small"]).To(BeNumerically("<", rankMap["medium"]))
			Expect(rankMap["medium"]).To(BeNumerically("<", rankMap["large"]))
		})
	})

	Context("UnallocatedVCPUPerPodCost Strategy", func() {
		BeforeEach(func() {
			rankingEngine = deletioncost.NewRankingEngine(deletioncost.RankingStrategyUnallocatedVCPUPerPodCost)
		})

		It("should rank nodes with higher unallocated vCPU per pod ratio lower (deleted first)", func() {
			// Node with high unallocated vCPU per pod (8 vCPU, 2 pods = 4.0 ratio)
			nodeHighRatio := createNodeWithCapacityAndPods("high-ratio", "8", "32Gi", 2)
			// Node with medium unallocated vCPU per pod (8 vCPU, 4 pods = 2.0 ratio)
			nodeMediumRatio := createNodeWithCapacityAndPods("medium-ratio", "8", "32Gi", 4)
			// Node with low unallocated vCPU per pod (8 vCPU, 8 pods = 1.0 ratio)
			nodeLowRatio := createNodeWithCapacityAndPods("low-ratio", "8", "32Gi", 8)

			nodes := []*state.StateNode{nodeLowRatio, nodeHighRatio, nodeMediumRatio}

			ranks, err := rankingEngine.RankNodes(ctx, nodes)
			Expect(err).ToNot(HaveOccurred())
			Expect(ranks).To(HaveLen(3))

			rankMap := make(map[string]int)
			for _, rank := range ranks {
				rankMap[rank.Node.Node.Name] = rank.Rank
			}

			// High ratio should have lowest rank (deleted first)
			Expect(rankMap["high-ratio"]).To(BeNumerically("<", rankMap["medium-ratio"]))
			Expect(rankMap["medium-ratio"]).To(BeNumerically("<", rankMap["low-ratio"]))
		})

		It("should handle nodes with zero pods", func() {
			nodes := []*state.StateNode{
				createNodeWithCapacityAndPods("no-pods", "8", "32Gi", 0),
				createNodeWithCapacityAndPods("with-pods", "8", "32Gi", 4),
			}

			ranks, err := rankingEngine.RankNodes(ctx, nodes)
			Expect(err).ToNot(HaveOccurred())
			Expect(ranks).To(HaveLen(2))

			// Should not panic and should assign ranks
			rankSet := make(map[int]bool)
			for _, rank := range ranks {
				Expect(rankSet[rank.Rank]).To(BeFalse())
				rankSet[rank.Rank] = true
			}
		})
	})

	Context("Do-Not-Disrupt Partitioning", func() {
		BeforeEach(func() {
			rankingEngine = deletioncost.NewRankingEngine(deletioncost.RankingStrategyRandom)
		})

		It("should rank nodes without do-not-disrupt pods first (lower ranks)", func() {
			normalNodes := createTestNodes(3, false)
			doNotDisruptNodes := createTestNodes(2, true)
			allNodes := append(normalNodes, doNotDisruptNodes...)

			ranks, err := rankingEngine.RankNodes(ctx, allNodes)
			Expect(err).ToNot(HaveOccurred())
			Expect(ranks).To(HaveLen(5))

			// Find max rank of normal nodes and min rank of do-not-disrupt nodes
			maxNormalRank := -10000
			minDoNotDisruptRank := 10000

			for _, rank := range ranks {
				if rank.HasDoNotDisrupt {
					if rank.Rank < minDoNotDisruptRank {
						minDoNotDisruptRank = rank.Rank
					}
				} else {
					if rank.Rank > maxNormalRank {
						maxNormalRank = rank.Rank
					}
				}
			}

			// All normal nodes should have lower ranks than do-not-disrupt nodes
			Expect(maxNormalRank).To(BeNumerically("<", minDoNotDisruptRank))
		})

		It("should handle all nodes having do-not-disrupt pods", func() {
			nodes := createTestNodes(5, true)
			ranks, err := rankingEngine.RankNodes(ctx, nodes)

			Expect(err).ToNot(HaveOccurred())
			Expect(ranks).To(HaveLen(5))

			// All should be marked as having do-not-disrupt
			for _, rank := range ranks {
				Expect(rank.HasDoNotDisrupt).To(BeTrue())
			}
		})

		It("should handle no nodes having do-not-disrupt pods", func() {
			nodes := createTestNodes(5, false)
			ranks, err := rankingEngine.RankNodes(ctx, nodes)

			Expect(err).ToNot(HaveOccurred())
			Expect(ranks).To(HaveLen(5))

			// None should be marked as having do-not-disrupt
			for _, rank := range ranks {
				Expect(rank.HasDoNotDisrupt).To(BeFalse())
			}
		})

		It("should apply ranking strategy within each partition", func() {
			rankingEngine = deletioncost.NewRankingEngine(deletioncost.RankingStrategyLargestToSmallest)

			// Create nodes with different sizes in each partition
			normalNodes := []*state.StateNode{
				createNodeWithCapacity("normal-large", "16", "64Gi"),
				createNodeWithCapacity("normal-small", "4", "16Gi"),
			}
			doNotDisruptNodes := []*state.StateNode{
				createNodeWithCapacityAndDoNotDisrupt("dnd-large", "16", "64Gi"),
				createNodeWithCapacityAndDoNotDisrupt("dnd-small", "4", "16Gi"),
			}
			allNodes := append(normalNodes, doNotDisruptNodes...)

			ranks, err := rankingEngine.RankNodes(ctx, allNodes)
			Expect(err).ToNot(HaveOccurred())

			rankMap := make(map[string]int)
			for _, rank := range ranks {
				rankMap[rank.Node.Node.Name] = rank.Rank
			}

			// Within normal partition: large should be ranked lower than small
			Expect(rankMap["normal-large"]).To(BeNumerically("<", rankMap["normal-small"]))

			// Within do-not-disrupt partition: large should be ranked lower than small
			Expect(rankMap["dnd-large"]).To(BeNumerically("<", rankMap["dnd-small"]))

			// All normal nodes should be ranked lower than all do-not-disrupt nodes
			Expect(rankMap["normal-small"]).To(BeNumerically("<", rankMap["dnd-large"]))
		})
	})

	Context("Edge Cases", func() {
		BeforeEach(func() {
			rankingEngine = deletioncost.NewRankingEngine(deletioncost.RankingStrategyRandom)
		})

		It("should handle very large number of nodes", func() {
			nodes := createTestNodes(1000, false)
			ranks, err := rankingEngine.RankNodes(ctx, nodes)

			Expect(err).ToNot(HaveOccurred())
			Expect(ranks).To(HaveLen(1000))

			// Verify all ranks are unique
			rankSet := make(map[int]bool)
			for _, rank := range ranks {
				Expect(rankSet[rank.Rank]).To(BeFalse())
				rankSet[rank.Rank] = true
			}
		})

		It("should handle invalid strategy gracefully", func() {
			rankingEngine = deletioncost.NewRankingEngine("InvalidStrategy")
			nodes := createTestNodes(3, false)

			_, err := rankingEngine.RankNodes(ctx, nodes)
			Expect(err).To(HaveOccurred())
		})
	})
})

// Helper functions

func createTestNodes(count int, hasDoNotDisrupt bool) []*state.StateNode {
	nodes := make([]*state.StateNode, count)
	for i := 0; i < count; i++ {
		nodes[i] = createTestNode(i, hasDoNotDisrupt)
	}
	return nodes
}

func createTestNode(index int, hasDoNotDisrupt bool) *state.StateNode {
	node := test.Node(test.NodeOptions{
		ObjectMeta: metav1.ObjectMeta{
			Name: test.RandomName(),
		},
		Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("16Gi"),
		},
	})

	stateNode := state.NewNode(node)

	if hasDoNotDisrupt {
		pod := test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					v1.DoNotDisruptAnnotationKey: "true",
				},
			},
		})
		stateNode.Pods[string(pod.UID)] = pod
	}

	return stateNode
}

func createNodeWithCapacity(name, cpu, memory string) *state.StateNode {
	node := test.Node(test.NodeOptions{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
		},
	})

	return state.NewNode(node)
}

func createNodeWithCapacityAndPods(name, cpu, memory string, podCount int) *state.StateNode {
	node := test.Node(test.NodeOptions{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
		},
	})

	stateNode := state.NewNode(node)

	// Add pods to the node
	for i := 0; i < podCount; i++ {
		pod := test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				Name: test.RandomName(),
			},
		})
		stateNode.Pods[string(pod.UID)] = pod
	}

	return stateNode
}

func createNodeWithCapacityAndDoNotDisrupt(name, cpu, memory string) *state.StateNode {
	node := test.Node(test.NodeOptions{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
		},
	})

	stateNode := state.NewNode(node)

	// Add a do-not-disrupt pod
	pod := test.Pod(test.PodOptions{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				v1.DoNotDisruptAnnotationKey: "true",
			},
		},
	})
	stateNode.Pods[string(pod.UID)] = pod

	return stateNode
}
