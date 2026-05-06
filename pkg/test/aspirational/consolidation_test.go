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

// TestMultiNodeConsolidation_CheaperCombination documents the scenario from
// https://github.com/kubernetes-sigs/karpenter/issues/1962
//
// Two c5a.xlarge nodes (4 vCPU, 8 GiB each) at $0.154/hr run pods using ~50%
// CPU. Their combined cost is $0.308/hr. A single m6a.xlarge (4 vCPU, 16 GiB)
// at $0.173/hr can fit both pods and is cheaper than running two c5a.xlarge.
//
// Currently multi-node consolidation fails to find this replacement because
// filterOutSameInstanceType removes c5a.xlarge from the replacement options
// (it's the same type as the candidates), and the remaining cross-family
// option (m6a.xlarge at $0.173) is individually more expensive than a single
// c5a.xlarge ($0.154), so RemoveInstanceTypeOptionsByPriceAndMinValues filters
// it out too — even though it's cheaper than the *combined* cost of both nodes.
//
// This test will pass once multi-node consolidation properly compares
// replacement cost against the aggregate candidate cost.
func TestMultiNodeConsolidation_CheaperCombination(t *testing.T) {
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

	// Instance types simulating the issue:
	// c5a.xlarge: 4 vCPU, 8 GiB — $0.154/hr (two = $0.308/hr)
	// m6a.xlarge: 4 vCPU, 16 GiB — $0.173/hr (cheaper than two c5a.xlarge combined)
	c5aXlarge := fake.NewInstanceType(fake.InstanceTypeOptions{
		Name: "c5a.xlarge",
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
			Price: 0.154,
		}},
	})
	m6aXlarge := fake.NewInstanceType(fake.InstanceTypeOptions{
		Name: "m6a.xlarge",
		Resources: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("16Gi"),
			corev1.ResourcePods:   resource.MustParse("58"),
		},
		Offerings: []*cloudprovider.Offering{{
			Available: true,
			Requirements: scheduling.NewLabelRequirements(map[string]string{
				v1.CapacityTypeLabelKey:  v1.CapacityTypeOnDemand,
				corev1.LabelTopologyZone: "test-zone-1",
			}),
			Price: 0.173,
		}},
	})

	cloudProvider.InstanceTypes = []*cloudprovider.InstanceType{c5aXlarge, m6aXlarge}
	pricingController := informer.NewPricingController(env.Client, cloudProvider, clusterCost)
	ExpectSingletonReconciled(ctx, pricingController)

	nodePool := test.NodePool(v1.NodePool{
		Spec: v1.NodePoolSpec{
			Disruption: v1.Disruption{
				ConsolidationPolicy: v1.ConsolidationPolicyWhenEmptyOrUnderutilized,
				ConsolidateAfter:    v1.MustParseNillableDuration("0s"),
				Budgets:             []v1.Budget{{Nodes: "100%"}},
			},
		},
	})

	// Two c5a.xlarge nodes
	nodeClaim1, node1 := test.NodeClaimAndNode(v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				v1.NodePoolLabelKey:            nodePool.Name,
				corev1.LabelInstanceTypeStable: "c5a.xlarge",
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
	nodeClaim1.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)

	nodeClaim2, node2 := test.NodeClaimAndNode(v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				v1.NodePoolLabelKey:            nodePool.Name,
				corev1.LabelInstanceTypeStable: "c5a.xlarge",
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
	nodeClaim2.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)

	// Pods at ~50% CPU on each node — combined they fit on one m6a.xlarge
	rs := test.ReplicaSet()
	ExpectApplied(ctx, env.Client, rs)
	if err := env.Client.Get(ctx, client.ObjectKeyFromObject(rs), rs); err != nil {
		t.Fatalf("getting replicaset: %v", err)
	}

	pod1 := test.Pod(test.PodOptions{
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
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			},
		},
	})
	pod2 := test.Pod(test.PodOptions{
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
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			},
		},
	})

	ExpectApplied(ctx, env.Client, nodePool, nodeClaim1, node1, nodeClaim2, node2, pod1, pod2)
	ExpectManualBinding(ctx, env.Client, pod1, node1)
	ExpectManualBinding(ctx, env.Client, pod2, node2)
	ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, []*corev1.Node{node1, node2}, []*v1.NodeClaim{nodeClaim1, nodeClaim2})

	// Directly invoke multi-node consolidation (bypasses validation timer)
	c := disruption.MakeConsolidation(fakeClock, cluster, env.Client, prov, cloudProvider, recorder, queue)
	multiNodeConsolidation := disruption.NewMultiNodeConsolidation(c)
	budgets, err := disruption.BuildDisruptionBudgetMapping(ctx, cluster, fakeClock, env.Client, cloudProvider, recorder, multiNodeConsolidation.Reason())
	if err != nil {
		t.Fatalf("building disruption budgets: %v", err)
	}

	candidates, err := disruption.GetCandidates(ctx, cluster, env.Client, recorder, fakeClock, cloudProvider, multiNodeConsolidation.ShouldDisrupt, multiNodeConsolidation.Class(), queue)
	if err != nil {
		t.Fatalf("getting candidates: %v", err)
	}

	cmds, err := multiNodeConsolidation.ComputeCommands(ctx, budgets, candidates...)
	if err != nil {
		t.Fatalf("computing commands: %v", err)
	}

	// ASSERTION: Multi-node consolidation should find that replacing
	// 2x c5a.xlarge ($0.308/hr combined) with 1x m6a.xlarge ($0.173/hr) saves money.
	//
	// Currently this fails because:
	// 1. filterOutSameInstanceType removes c5a.xlarge from replacement options
	//    (it's the same type as the candidates being consolidated)
	// 2. The candidatePrice passed to RemoveInstanceTypeOptionsByPriceAndMinValues
	//    is the SUM of both nodes ($0.308), but the filtering compares each
	//    replacement option's individual price against... the cheapest existing
	//    node's price ($0.154), NOT the aggregate. So m6a.xlarge ($0.173) gets
	//    filtered out because $0.173 > $0.154.
	//
	// The fix would be to compare replacement cost against aggregate candidate cost
	// in the multi-node path.
	if len(cmds) == 0 {
		t.Fatalf("multi-node consolidation found no commands: "+
			"2x c5a.xlarge ($0.308/hr combined) should consolidate to 1x m6a.xlarge ($0.173/hr), "+
			"but the controller failed to identify this cross-family replacement as cheaper. "+
			"See https://github.com/kubernetes-sigs/karpenter/issues/1962")
	}

	// Verify the replacement includes m6a.xlarge
	cmd := cmds[0]
	if len(cmd.Replacements) == 0 {
		t.Fatalf("consolidation issued delete-only command but pods have nowhere to schedule")
	}

	found := false
	for _, it := range cmd.Replacements[0].InstanceTypeOptions {
		if it.Name == "m6a.xlarge" {
			found = true
			break
		}
	}
	if !found {
		names := lo.Map(cmd.Replacements[0].InstanceTypeOptions, func(it *cloudprovider.InstanceType, _ int) string { return it.Name })
		t.Errorf("expected m6a.xlarge in replacement options, got: %v", names)
	}
}
