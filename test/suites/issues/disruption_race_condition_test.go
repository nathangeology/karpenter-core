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

package issues_test

import (
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/disruption"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var _ = Describe("Issue #651 - Consolidation Race Condition", func() {
	Context("EBS Volume Race Condition", func() {
		var nodePool *v1.NodePool
		var nodeClaim *v1.NodeClaim
		var node *corev1.Node

		BeforeEach(func() {
			// Use real validator for actual timing behavior
			disruptionController = disruption.NewController(fakeClock, env.Client, prov, cloudProvider, recorder, cluster, queue, disruption.WithMethods(NewMethodsWithRealValidator()...))

			nodePool = test.NodePool(v1.NodePool{
				Spec: v1.NodePoolSpec{
					Disruption: v1.Disruption{
						ConsolidationPolicy: v1.ConsolidationPolicyWhenEmptyOrUnderutilized,
						ConsolidateAfter:    v1.MustParseNillableDuration("0s"),
						Budgets:             []v1.Budget{{Nodes: "100%"}},
					},
				},
			})

			nodeClaim, node = test.NodeClaimAndNode(v1.NodeClaim{
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
						corev1.ResourceCPU:  resource.MustParse("32"),
						corev1.ResourcePods: resource.MustParse("100"),
					},
				},
			})
			nodeClaim.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)
		})

		It("should not consolidate node when new pod with EBS volume is scheduled during consolidation of existing pods", func() {
			By("Setting up node with existing pods that can be consolidated")
			rs := test.ReplicaSet()
			ExpectApplied(ctx, env.Client, rs)

			// Create 2 pods that use only half the node capacity - this makes the node eligible for consolidation
			existingPods := test.Pods(2, test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "existing"},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion:         "apps/v1",
						Kind:               "ReplicaSet",
						Name:               rs.Name,
						UID:                rs.UID,
						Controller:         lo.ToPtr(true),
						BlockOwnerDeletion: lo.ToPtr(true),
					}},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("8"), // 8 + 8 = 16 < 32 (underutilized)
					},
				},
			})

			ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node, existingPods[0], existingPods[1])
			ExpectManualBinding(ctx, env.Client, existingPods[0], node)
			ExpectManualBinding(ctx, env.Client, existingPods[1], node)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, []*corev1.Node{node}, []*v1.NodeClaim{nodeClaim})

			By("Creating EBS storage resources")
			storageClass := &storagev1.StorageClass{
				ObjectMeta:        metav1.ObjectMeta{Name: "ebs-gp3"},
				Provisioner:       "ebs.csi.aws.com",
				VolumeBindingMode: &[]storagev1.VolumeBindingMode{storagev1.VolumeBindingWaitForFirstConsumer}[0],
				Parameters:        map[string]string{"type": "gp3"},
			}

			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ebs-pvc", Namespace: "default"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
					},
					StorageClassName: &storageClass.Name,
				},
			}

			ExpectApplied(ctx, env.Client, storageClass, pvc)

			By("Testing race condition: scheduling new pod with EBS volume during consolidation validation")
			var newPodScheduledDuringValidation atomic.Bool

			finished := atomic.Bool{}
			ExpectParallelized(
				func() {
					defer finished.Store(true)
					// This triggers consolidation of the underutilized node and waits for validation timeout
					ExpectSingletonReconciled(ctx, disruptionController)
				},
				func() {
					// Wait for controller to block on validation timeout
					Eventually(fakeClock.HasWaiters, time.Second*10).Should(BeTrue())

					// Controller should be blocking during timeout
					Expect(finished.Load()).To(BeFalse())

					By("Scheduling NEW pod with EBS volume during consolidation validation")
					// This is the race condition: a NEW pod with EBS volume gets scheduled
					// while Karpenter is consolidating the existing pods
					newPodWithEBS := test.Pod(test.PodOptions{
						ObjectMeta: metav1.ObjectMeta{
							Name: "new-pod-with-ebs-volume",
							Labels: map[string]string{
								"issue": "651",
								"test":  "race-condition",
								"app":   "new-workload", // Different from existing pods
							},
						},
						PersistentVolumeClaims: []string{pvc.Name},
						ResourceRequirements: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("4"), // This pod needs EBS volume
							},
						},
					})

					ExpectApplied(ctx, env.Client, newPodWithEBS)
					ExpectManualBinding(ctx, env.Client, newPodWithEBS, node)
					newPodScheduledDuringValidation.Store(true)

					// Update cluster state to reflect the new pod
					ExpectReconcileSucceeded(ctx, nodeStateController, client.ObjectKeyFromObject(node))

					GinkgoWriter.Printf("RACE CONDITION: NEW pod with EBS volume scheduled during consolidation validation\n")
					GinkgoWriter.Printf("  - Existing pods: %s, %s (being consolidated)\n", existingPods[0].Name, existingPods[1].Name)
					GinkgoWriter.Printf("  - NEW pod: %s (with EBS volume, scheduled during validation)\n", newPodWithEBS.Name)

					// Advance clock to complete validation
					fakeClock.Step(31 * time.Second)
					Eventually(finished.Load, 10*time.Second).Should(BeTrue())
				},
			)

			By("Verifying the race condition behavior")
			Expect(newPodScheduledDuringValidation.Load()).To(BeTrue(), "New pod should have been scheduled during validation")

			// Check if consolidation was blocked or proceeded
			cmds := queue.GetCommands()

			if len(cmds) > 0 {
				// Race condition exists: consolidation proceeded despite new pod with EBS volume
				GinkgoWriter.Printf("❌ RACE CONDITION DETECTED:\n")
				GinkgoWriter.Printf("  - Consolidation command created: %s\n", cmds[0].Decision())
				GinkgoWriter.Printf("  - Candidates: %d nodes\n", len(cmds[0].Candidates))
				GinkgoWriter.Printf("  - NEW pod with EBS volume would FAIL when node is terminated\n")
				GinkgoWriter.Printf("  - Karpenter missed the new pod during consolidation validation\n")

				// This should fail until the race condition is fixed
				Fail("RACE CONDITION REPRODUCED (Issue #651): " +
					"Consolidation proceeded even though a NEW pod with EBS volume was scheduled " +
					"during validation. This represents the exact customer scenario where pods fail " +
					"when nodes are terminated while EBS volume attachment is in progress. " +
					"This test will pass once proper synchronization prevents consolidation " +
					"when new pods with volumes are scheduled during validation.")
			} else {
				// Race condition fixed: consolidation was properly blocked
				GinkgoWriter.Printf("✅ RACE CONDITION FIXED:\n")
				GinkgoWriter.Printf("  - Consolidation properly blocked by NEW pod with EBS volume\n")
				GinkgoWriter.Printf("  - Proper synchronization detected the new pod during validation\n")
				GinkgoWriter.Printf("  - Pod will not fail due to node termination\n")
				GinkgoWriter.Printf("SUCCESS: The race condition has been resolved!\n")
			}
		})
	})
})
