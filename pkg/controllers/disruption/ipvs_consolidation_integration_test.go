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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/disruption"
	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

// Requirements: 3.1, 3.3, 3.4
var _ = Describe("IPVS Consolidation Integration", func() {
	var nodePool *v1.NodePool
	var nodeClaim *v1.NodeClaim
	var node *corev1.Node

	BeforeEach(func() {
		nodePool = test.NodePool(v1.NodePool{
			Spec: v1.NodePoolSpec{
				Disruption: v1.Disruption{
					ConsolidationPolicy: v1.ConsolidationPolicyWhenEmptyOrUnderutilized,
					ConsolidateAfter:    v1.MustParseNillableDuration("0s"),
					Budgets: []v1.Budget{{
						Nodes: "100%",
					}},
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

		// Enable the IPVS feature gate for all tests in this block
		ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
			FeatureGates: test.FeatureGates{
				InPlacePodVerticalScaling: lo.ToPtr(true),
				SpotToSpotConsolidation:   lo.ToPtr(true),
			},
		}))
	})

	It("should block consolidation when a pod has an active InProgress resize", func() {
		// Create a pod with an active InProgress resize status
		rs := test.ReplicaSet()
		ExpectApplied(ctx, env.Client, rs)

		pod := test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion:         "apps/v1",
						Kind:               "ReplicaSet",
						Name:               rs.Name,
						UID:                rs.UID,
						Controller:         lo.ToPtr(true),
						BlockOwnerDeletion: lo.ToPtr(true),
					},
				},
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
			},
		})

		ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node, rs, pod)
		ExpectManualBinding(ctx, env.Client, pod, node)

		// Set the pod's resize status to InProgress
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Resize = corev1.PodResizeStatusInProgress
		ExpectApplied(ctx, env.Client, pod)

		// Inform cluster state about nodes and nodeclaims
		ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, []*corev1.Node{node}, []*v1.NodeClaim{nodeClaim})

		ExpectSingletonReconciled(ctx, disruptionController)

		// Consolidation should be blocked — no commands in the queue
		cmds := queue.GetCommands()
		Expect(cmds).To(HaveLen(0))

		// Verify the Unconsolidatable event was emitted with the active resize message
		Expect(recorder.Calls(events.Unconsolidatable)).To(BeNumerically(">", 0))
	})

	It("should block consolidation when a pod has a Proposed resize", func() {
		rs := test.ReplicaSet()
		ExpectApplied(ctx, env.Client, rs)

		pod := test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion:         "apps/v1",
						Kind:               "ReplicaSet",
						Name:               rs.Name,
						UID:                rs.UID,
						Controller:         lo.ToPtr(true),
						BlockOwnerDeletion: lo.ToPtr(true),
					},
				},
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
			},
		})

		ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node, rs, pod)
		ExpectManualBinding(ctx, env.Client, pod, node)

		// Set the pod's resize status to Proposed
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Resize = corev1.PodResizeStatus("Proposed")
		ExpectApplied(ctx, env.Client, pod)

		ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, []*corev1.Node{node}, []*v1.NodeClaim{nodeClaim})

		ExpectSingletonReconciled(ctx, disruptionController)

		// Consolidation should be blocked
		cmds := queue.GetCommands()
		Expect(cmds).To(HaveLen(0))
		Expect(recorder.Calls(events.Unconsolidatable)).To(BeNumerically(">", 0))
	})

	It("should block consolidation during the grace period after resize completes", func() {
		rs := test.ReplicaSet()
		ExpectApplied(ctx, env.Client, rs)

		pod := test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion:         "apps/v1",
						Kind:               "ReplicaSet",
						Name:               rs.Name,
						UID:                rs.UID,
						Controller:         lo.ToPtr(true),
						BlockOwnerDeletion: lo.ToPtr(true),
					},
				},
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
			},
		})

		// Pod has no active resize (resize already completed)
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Resize = corev1.PodResizeStatus("")

		ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node, rs, pod)
		ExpectManualBinding(ctx, env.Client, pod, node)

		// Inform cluster state about nodes and nodeclaims
		ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, []*corev1.Node{node}, []*v1.NodeClaim{nodeClaim})

		// Simulate that a resize recently completed by setting the lastResizeCompletionTime
		// to 2 minutes ago (within the default 5-minute grace period) on the actual cluster state node
		for n := range cluster.Nodes() {
			if n.Node.Name == node.Name {
				n.SetLastResizeCompletionTime(fakeClock.Now().Add(-2 * time.Minute))
			}
		}

		cluster.MarkUnconsolidated()
		*queue = *disruption.NewQueue(env.Client, recorder, cluster, fakeClock, prov)
		disruptionController = disruption.NewController(fakeClock, env.Client, prov, cloudProvider, recorder, cluster, queue, disruption.WithMethods(NewMethodsWithNopValidator()...))

		ExpectSingletonReconciled(ctx, disruptionController)

		// Consolidation should be blocked during grace period
		cmds := queue.GetCommands()
		Expect(cmds).To(HaveLen(0))
		Expect(recorder.Calls(events.Unconsolidatable)).To(BeNumerically(">", 0))
	})

	It("should allow consolidation after the grace period expires", func() {
		rs := test.ReplicaSet()
		ExpectApplied(ctx, env.Client, rs)

		pod := test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion:         "apps/v1",
						Kind:               "ReplicaSet",
						Name:               rs.Name,
						UID:                rs.UID,
						Controller:         lo.ToPtr(true),
						BlockOwnerDeletion: lo.ToPtr(true),
					},
				},
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
			},
		})

		// Pod has no active resize
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Resize = corev1.PodResizeStatus("")

		ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node, rs, pod)
		ExpectManualBinding(ctx, env.Client, pod, node)

		ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, []*corev1.Node{node}, []*v1.NodeClaim{nodeClaim})

		// Simulate that a resize completed 6 minutes ago (past the default 5-minute grace period)
		for n := range cluster.Nodes() {
			if n.Node.Name == node.Name {
				n.SetLastResizeCompletionTime(fakeClock.Now().Add(-6 * time.Minute))
			}
		}

		// Reset disruption state for a fresh evaluation
		cluster.MarkUnconsolidated()
		*queue = *disruption.NewQueue(env.Client, recorder, cluster, fakeClock, prov)
		disruptionController = disruption.NewController(fakeClock, env.Client, prov, cloudProvider, recorder, cluster, queue, disruption.WithMethods(NewMethodsWithNopValidator()...))

		ExpectSingletonReconciled(ctx, disruptionController)

		// Consolidation should now proceed — the node is underutilized and grace period has expired
		cmds := queue.GetCommands()
		Expect(cmds).To(HaveLen(1))
	})

	It("should block consolidation within default grace period and proceed after it expires", func() {
		rs := test.ReplicaSet()
		ExpectApplied(ctx, env.Client, rs)

		pod := test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion:         "apps/v1",
						Kind:               "ReplicaSet",
						Name:               rs.Name,
						UID:                rs.UID,
						Controller:         lo.ToPtr(true),
						BlockOwnerDeletion: lo.ToPtr(true),
					},
				},
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
			},
		})

		pod.Status.Phase = corev1.PodRunning
		pod.Status.Resize = corev1.PodResizeStatus("")

		ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node, rs, pod)
		ExpectManualBinding(ctx, env.Client, pod, node)

		ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, []*corev1.Node{node}, []*v1.NodeClaim{nodeClaim})

		// Set resize completion to 3 minutes ago — within the default 5-minute grace period
		for n := range cluster.Nodes() {
			if n.Node.Name == node.Name {
				n.SetLastResizeCompletionTime(fakeClock.Now().Add(-3 * time.Minute))
			}
		}

		cluster.MarkUnconsolidated()
		recorder.Reset()
		*queue = *disruption.NewQueue(env.Client, recorder, cluster, fakeClock, prov)
		disruptionController = disruption.NewController(fakeClock, env.Client, prov, cloudProvider, recorder, cluster, queue, disruption.WithMethods(NewMethodsWithNopValidator()...))

		ExpectSingletonReconciled(ctx, disruptionController)

		// Should be blocked — still within the 5-minute grace period
		cmds := queue.GetCommands()
		Expect(cmds).To(HaveLen(0))
		Expect(recorder.Calls(events.Unconsolidatable)).To(BeNumerically(">", 0))

		// Now advance the clock past the grace period (3 more minutes = 6 total)
		fakeClock.Step(3 * time.Minute)

		cluster.MarkUnconsolidated()
		recorder.Reset()
		*queue = *disruption.NewQueue(env.Client, recorder, cluster, fakeClock, prov)
		disruptionController = disruption.NewController(fakeClock, env.Client, prov, cloudProvider, recorder, cluster, queue, disruption.WithMethods(NewMethodsWithNopValidator()...))

		ExpectSingletonReconciled(ctx, disruptionController)

		// Should now proceed — grace period has expired
		cmds = queue.GetCommands()
		Expect(cmds).To(HaveLen(1))
	})
})
