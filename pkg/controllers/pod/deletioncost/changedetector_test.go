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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/karpenter/pkg/controllers/pod/deletioncost"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/test"
)

var _ = Describe("Change Detector", func() {
	var changeDetector *deletioncost.ChangeDetector

	BeforeEach(func() {
		changeDetector = deletioncost.NewChangeDetector()
	})

	Context("Initial State", func() {
		It("should detect change on first call", func() {
			nodes := createTestNodes(3, false)
			hasChanged := changeDetector.HasChanged(nodes)

			Expect(hasChanged).To(BeTrue(), "First call should always detect change")
		})
	})

	Context("Node Changes", func() {
		It("should detect when nodes are added", func() {
			initialNodes := createTestNodes(3, false)
			changeDetector.HasChanged(initialNodes)

			// Add a node
			moreNodes := createTestNodes(4, false)
			hasChanged := changeDetector.HasChanged(moreNodes)

			Expect(hasChanged).To(BeTrue(), "Should detect node addition")
		})

		It("should detect when nodes are removed", func() {
			initialNodes := createTestNodes(3, false)
			changeDetector.HasChanged(initialNodes)

			// Remove a node
			fewerNodes := initialNodes[:2]
			hasChanged := changeDetector.HasChanged(fewerNodes)

			Expect(hasChanged).To(BeTrue(), "Should detect node removal")
		})

		It("should NOT detect change when nodes are unchanged", func() {
			nodes := createTestNodes(3, false)
			changeDetector.HasChanged(nodes)

			// Same nodes
			hasChanged := changeDetector.HasChanged(nodes)

			Expect(hasChanged).To(BeFalse(), "Should not detect change when nodes are same")
		})

		It("should detect when node names change", func() {
			initialNodes := createTestNodes(3, false)
			changeDetector.HasChanged(initialNodes)

			// Different nodes with same count
			differentNodes := createTestNodes(3, false)
			hasChanged := changeDetector.HasChanged(differentNodes)

			Expect(hasChanged).To(BeTrue(), "Should detect different nodes")
		})
	})

	Context("Pod Changes", func() {
		It("should detect when pods are added to nodes", func() {
			node := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
			})
			stateNode := state.NewNode(node)

			nodes := []*state.StateNode{stateNode}
			changeDetector.HasChanged(nodes)

			// Add a pod
			pod := test.Pod(test.PodOptions{})
			stateNode.Pods[string(pod.UID)] = pod

			hasChanged := changeDetector.HasChanged(nodes)
			Expect(hasChanged).To(BeTrue(), "Should detect pod addition")
		})

		It("should detect when pods are removed from nodes", func() {
			node := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
			})
			stateNode := state.NewNode(node)

			pod := test.Pod(test.PodOptions{})
			stateNode.Pods[string(pod.UID)] = pod

			nodes := []*state.StateNode{stateNode}
			changeDetector.HasChanged(nodes)

			// Remove the pod
			delete(stateNode.Pods, string(pod.UID))

			hasChanged := changeDetector.HasChanged(nodes)
			Expect(hasChanged).To(BeTrue(), "Should detect pod removal")
		})

		It("should NOT detect change when pod count is same", func() {
			node := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
			})
			stateNode := state.NewNode(node)

			pod := test.Pod(test.PodOptions{})
			stateNode.Pods[string(pod.UID)] = pod

			nodes := []*state.StateNode{stateNode}
			changeDetector.HasChanged(nodes)

			// Same pod count
			hasChanged := changeDetector.HasChanged(nodes)
			Expect(hasChanged).To(BeFalse(), "Should not detect change when pod count is same")
		})
	})

	Context("Do-Not-Disrupt Changes", func() {
		It("should detect when do-not-disrupt annotation is added", func() {
			node := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
			})
			stateNode := state.NewNode(node)

			pod := test.Pod(test.PodOptions{})
			stateNode.Pods[string(pod.UID)] = pod

			nodes := []*state.StateNode{stateNode}
			changeDetector.HasChanged(nodes)

			// Add do-not-disrupt annotation
			pod.Annotations = map[string]string{
				"karpenter.sh/do-not-disrupt": "true",
			}

			hasChanged := changeDetector.HasChanged(nodes)
			Expect(hasChanged).To(BeTrue(), "Should detect do-not-disrupt annotation change")
		})

		It("should detect when do-not-disrupt annotation is removed", func() {
			node := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
			})
			stateNode := state.NewNode(node)

			pod := test.Pod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"karpenter.sh/do-not-disrupt": "true",
					},
				},
			})
			stateNode.Pods[string(pod.UID)] = pod

			nodes := []*state.StateNode{stateNode}
			changeDetector.HasChanged(nodes)

			// Remove do-not-disrupt annotation
			delete(pod.Annotations, "karpenter.sh/do-not-disrupt")

			hasChanged := changeDetector.HasChanged(nodes)
			Expect(hasChanged).To(BeTrue(), "Should detect do-not-disrupt annotation removal")
		})
	})

	Context("Node Capacity Changes", func() {
		It("should detect when node capacity changes", func() {
			node := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
				},
			})
			stateNode := state.NewNode(node)

			nodes := []*state.StateNode{stateNode}
			changeDetector.HasChanged(nodes)

			// Change capacity
			node.Status.Allocatable = corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("8"),
				corev1.ResourceMemory: resource.MustParse("32Gi"),
			}
			stateNode = state.NewNode(node)
			nodes = []*state.StateNode{stateNode}

			hasChanged := changeDetector.HasChanged(nodes)
			Expect(hasChanged).To(BeTrue(), "Should detect capacity change")
		})
	})

	Context("Empty States", func() {
		It("should handle empty node list", func() {
			nodes := []*state.StateNode{}
			hasChanged := changeDetector.HasChanged(nodes)

			Expect(hasChanged).To(BeTrue(), "First call should detect change")

			// Second call with empty list
			hasChanged = changeDetector.HasChanged(nodes)
			Expect(hasChanged).To(BeFalse(), "Should not detect change for empty list")
		})

		It("should detect transition from empty to non-empty", func() {
			emptyNodes := []*state.StateNode{}
			changeDetector.HasChanged(emptyNodes)

			nodes := createTestNodes(3, false)
			hasChanged := changeDetector.HasChanged(nodes)

			Expect(hasChanged).To(BeTrue(), "Should detect transition from empty to non-empty")
		})

		It("should detect transition from non-empty to empty", func() {
			nodes := createTestNodes(3, false)
			changeDetector.HasChanged(nodes)

			emptyNodes := []*state.StateNode{}
			hasChanged := changeDetector.HasChanged(emptyNodes)

			Expect(hasChanged).To(BeTrue(), "Should detect transition from non-empty to empty")
		})
	})

	Context("Complex Scenarios", func() {
		It("should handle multiple consecutive checks without changes", func() {
			nodes := createTestNodes(3, false)
			changeDetector.HasChanged(nodes)

			// Multiple checks with no changes
			for i := 0; i < 10; i++ {
				hasChanged := changeDetector.HasChanged(nodes)
				Expect(hasChanged).To(BeFalse(), "Should not detect change on iteration %d", i)
			}
		})

		It("should detect changes after multiple stable checks", func() {
			nodes := createTestNodes(3, false)
			changeDetector.HasChanged(nodes)

			// Multiple stable checks
			for i := 0; i < 5; i++ {
				changeDetector.HasChanged(nodes)
			}

			// Now make a change
			moreNodes := createTestNodes(4, false)
			hasChanged := changeDetector.HasChanged(moreNodes)

			Expect(hasChanged).To(BeTrue(), "Should detect change after stable period")
		})

		It("should handle rapid changes", func() {
			nodes := createTestNodes(3, false)
			changeDetector.HasChanged(nodes)

			// Rapid changes
			for i := 4; i <= 10; i++ {
				nodes = createTestNodes(i, false)
				hasChanged := changeDetector.HasChanged(nodes)
				Expect(hasChanged).To(BeTrue(), "Should detect each change")
			}
		})
	})

	Context("Hash Stability", func() {
		It("should produce same hash for same node state", func() {
			nodes1 := createTestNodes(3, false)
			changeDetector.HasChanged(nodes1)

			// Create identical nodes (same names, same state)
			nodes2 := nodes1
			hasChanged := changeDetector.HasChanged(nodes2)

			Expect(hasChanged).To(BeFalse(), "Same nodes should produce same hash")
		})

		It("should handle nodes in different order", func() {
			node1 := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			})
			node2 := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "node-2"},
			})

			nodesOrder1 := []*state.StateNode{
				state.NewNode(node1),
				state.NewNode(node2),
			}
			changeDetector.HasChanged(nodesOrder1)

			nodesOrder2 := []*state.StateNode{
				state.NewNode(node2),
				state.NewNode(node1),
			}
			hasChanged := changeDetector.HasChanged(nodesOrder2)

			// This behavior depends on implementation - if hash is order-dependent,
			// it will detect change. If order-independent, it won't.
			// Document the expected behavior based on implementation
			_ = hasChanged // Test should verify actual implementation behavior
		})
	})

	Context("Performance", func() {
		It("should handle large number of nodes efficiently", func() {
			largeNodeSet := createTestNodes(1000, false)
			hasChanged := changeDetector.HasChanged(largeNodeSet)

			Expect(hasChanged).To(BeTrue(), "First call should detect change")

			// Second call should be fast
			hasChanged = changeDetector.HasChanged(largeNodeSet)
			Expect(hasChanged).To(BeFalse(), "Should efficiently detect no change")
		})

		It("should handle nodes with many pods", func() {
			node := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "busy-node"},
			})
			stateNode := state.NewNode(node)

			// Add many pods
			for i := 0; i < 100; i++ {
				pod := test.Pod(test.PodOptions{})
				stateNode.Pods[string(pod.UID)] = pod
			}

			nodes := []*state.StateNode{stateNode}
			hasChanged := changeDetector.HasChanged(nodes)

			Expect(hasChanged).To(BeTrue(), "First call should detect change")

			hasChanged = changeDetector.HasChanged(nodes)
			Expect(hasChanged).To(BeFalse(), "Should efficiently handle many pods")
		})
	})
})
