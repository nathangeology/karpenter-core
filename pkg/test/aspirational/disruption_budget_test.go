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
	"testing"
	"time"

	"github.com/onsi/gomega"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coreapis "sigs.k8s.io/karpenter/pkg/apis"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/fake"
	"sigs.k8s.io/karpenter/pkg/controllers/disruption"
	"sigs.k8s.io/karpenter/pkg/controllers/provisioning"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/controllers/state/informer"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/state/cost"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

// TestDriftDoesNotStarveConsolidationBudget documents the scenario from
// https://github.com/kubernetes-sigs/karpenter/pull/2930
//
// When nodes are drifted, drift disruption can consume the entire disruption
// budget, leaving consolidation unable to make progress indefinitely. The
// desired behavior is fair sharing of the disruption budget between drift and
// consolidation so that cost optimization can still proceed during rolling
// drift events.
//
// The disruption controller iterates methods in fixed order:
//
//	Emptiness → StaticDrift → Drift → MultiNodeConsolidation → SingleNodeConsolidation
//
// Once drift succeeds (returns a command), the loop breaks and requeues. If
// drifted nodes always exist, consolidation never gets a turn. This test
// simulates multiple reconciliation cycles and asserts that consolidation
// receives at least some budget allocation.
//
// This test will pass once disruption budget fairness is implemented.
func TestDriftDoesNotStarveConsolidationBudget(t *testing.T) {
	gomega.RegisterTestingT(t)
	ctx := TestContextWithLogger(t)
	ctx = options.ToContext(ctx, test.Options())

	env := test.NewEnvironment(test.WithCRDs(coreapis.CRDs...))
	defer func() {
		if err := env.Stop(); err != nil {
			t.Fatalf("stopping environment: %v", err)
		}
	}()

	cloudProvider := fake.NewCloudProvider()
	fakeClock := clock.NewFakeClock(time.Now())
	clusterCost := cost.NewClusterCost(ctx, cloudProvider, env.Client)
	cluster := state.NewCluster(fakeClock, env.Client, cloudProvider)
	nodeStateController := informer.NewNodeController(env.Client, cluster)
	nodeClaimStateController := informer.NewNodeClaimController(env.Client, cloudProvider, cluster, clusterCost)
	recorder := test.NewEventRecorder()
	prov := provisioning.NewProvisioner(env.Client, recorder, cloudProvider, cluster, fakeClock)
	queue := disruption.NewQueue(env.Client, recorder, cluster, fakeClock, prov)

	// Use instance types with clear pricing: expensive (current) and cheap (replacement)
	expensiveInstance := fake.NewInstanceType(fake.InstanceTypeOptions{
		Name: "expensive-on-demand",
		Resources: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("8Gi"),
			corev1.ResourcePods:   resource.MustParse("58"),
		},
		Offerings: []*cloudprovider.Offering{{
			Available: true,
			Requirements: scheduling.NewLabelRequirements(map[string]string{
				v1.CapacityTypeLabelKey:  v1.CapacityTypeOnDemand,
				corev1.LabelTopologyZone: "test-zone-1",
			}),
			Price: 1.0,
		}},
	})
	cheapInstance := fake.NewInstanceType(fake.InstanceTypeOptions{
		Name: "cheap-on-demand",
		Resources: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("8Gi"),
			corev1.ResourcePods:   resource.MustParse("58"),
		},
		Offerings: []*cloudprovider.Offering{{
			Available: true,
			Requirements: scheduling.NewLabelRequirements(map[string]string{
				v1.CapacityTypeLabelKey:  v1.CapacityTypeOnDemand,
				corev1.LabelTopologyZone: "test-zone-1",
			}),
			Price: 0.5,
		}},
	})
	cloudProvider.InstanceTypes = []*cloudprovider.InstanceType{expensiveInstance, cheapInstance}
	pricingController := informer.NewPricingController(env.Client, cloudProvider, clusterCost)
	ExpectSingletonReconciled(ctx, pricingController)

	const totalNodes = 10
	const reconcileCycles = 10

	// NodePool with a budget of 10% (= 1 node per cycle)
	nodePool := test.NodePool(v1.NodePool{
		Spec: v1.NodePoolSpec{
			Disruption: v1.Disruption{
				ConsolidationPolicy: v1.ConsolidationPolicyWhenEmptyOrUnderutilized,
				ConsolidateAfter:    v1.MustParseNillableDuration("0s"),
				Budgets:             []v1.Budget{{Nodes: "10%"}},
			},
		},
	})

	// Create nodes: half drifted (candidates for drift), half consolidatable (candidates for consolidation)
	rs := test.ReplicaSet()
	ExpectApplied(ctx, env.Client, rs, nodePool)
	if err := env.Client.Get(ctx, client.ObjectKeyFromObject(rs), rs); err != nil {
		t.Fatalf("getting replicaset: %v", err)
	}

	var nodeClaims []*v1.NodeClaim
	var nodes []*corev1.Node
	for i := 0; i < totalNodes; i++ {
		nc, n := test.NodeClaimAndNode(v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					v1.NodePoolLabelKey:            nodePool.Name,
					corev1.LabelInstanceTypeStable: "expensive-on-demand",
					v1.CapacityTypeLabelKey:        v1.CapacityTypeOnDemand,
					corev1.LabelTopologyZone:       "test-zone-1",
				},
			},
			Status: v1.NodeClaimStatus{
				Allocatable: map[corev1.ResourceName]resource.Quantity{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
					corev1.ResourcePods:   resource.MustParse("58"),
				},
			},
		})
		// Mark first half as drifted, second half as consolidatable
		if i < totalNodes/2 {
			nc.StatusConditions().SetTrue(v1.ConditionTypeDrifted)
		}
		nc.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)
		nodeClaims = append(nodeClaims, nc)
		nodes = append(nodes, n)
	}

	// Apply all nodes and bind a pod to each (so they're not empty)
	for i := 0; i < totalNodes; i++ {
		pod := test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"app": "test"},
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
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
			},
		})
		ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i], pod)
		ExpectManualBinding(ctx, env.Client, pod, nodes[i])
	}

	ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

	// Run multiple reconciliation cycles and track which disruption reasons fire
	driftActions := 0
	consolidationActions := 0

	for cycle := 0; cycle < reconcileCycles; cycle++ {
		disruptionController := disruption.NewController(fakeClock, env.Client, prov, cloudProvider, recorder, cluster, queue)
		ExpectSingletonReconciled(ctx, disruptionController)

		cmds := queue.GetCommands()
		for _, cmd := range cmds {
			switch cmd.Method.Reason() {
			case v1.DisruptionReasonDrifted:
				driftActions++
			case v1.DisruptionReasonUnderutilized:
				consolidationActions++
			}
		}

		// Process commands to free budget for next cycle
		for _, cmd := range cmds {
			for _, candidate := range cmd.Candidates {
				ExpectObjectReconciled(ctx, env.Client, queue, candidate.NodeClaim)
			}
		}

		// Reset queue and cluster state for next cycle
		*queue = *disruption.NewQueue(env.Client, recorder, cluster, fakeClock, prov)
		cluster.MarkUnconsolidated()

		// Re-mark remaining drifted nodes as drifted (simulating continuous drift)
		for i := 0; i < totalNodes/2; i++ {
			nc := &v1.NodeClaim{}
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(nodeClaims[i]), nc); err == nil {
				nc.StatusConditions().SetTrue(v1.ConditionTypeDrifted)
				ExpectApplied(ctx, env.Client, nc)
				ExpectReconcileSucceeded(ctx, nodeClaimStateController, client.ObjectKeyFromObject(nc))
			}
		}
	}

	// DESIRED BEHAVIOR: consolidation should get at least some budget across
	// multiple cycles, even when drift candidates always exist.
	if consolidationActions == 0 {
		t.Fatalf("consolidation was starved: got 0 actions across %d cycles "+
			"(drift got %d). Drift consumed the entire disruption budget every cycle, "+
			"preventing consolidation from ever making progress. "+
			"See https://github.com/kubernetes-sigs/karpenter/pull/2930",
			reconcileCycles, driftActions)
	}

	// Verify the budget fairness gives consolidation a meaningful share
	minExpected := reconcileCycles / 5
	if consolidationActions < minExpected {
		t.Errorf("consolidation got insufficient budget: %d/%d cycles (minimum expected: %d). "+
			"Budget fairness should ensure consolidation gets a meaningful share.",
			consolidationActions, reconcileCycles, minExpected)
	}

	_ = context.Background() // suppress unused import
}
