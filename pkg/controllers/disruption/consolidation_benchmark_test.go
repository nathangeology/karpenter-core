//go:build test_performance

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
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	clocktesting "k8s.io/utils/clock/testing"
	stdtime "time"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakecr "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/fake"
	"sigs.k8s.io/karpenter/pkg/controllers/disruption"
	"sigs.k8s.io/karpenter/pkg/controllers/dynamicresources/deviceallocation"
	"sigs.k8s.io/karpenter/pkg/controllers/provisioning"
	pstate "sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	"sigs.k8s.io/karpenter/pkg/operator/logging"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/state/cost"
	"sigs.k8s.io/karpenter/pkg/state/virtualpods"
	"sigs.k8s.io/karpenter/pkg/test"
	_ "sigs.k8s.io/karpenter/pkg/test/v1alpha1"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

// Benchmarks target the N-candidate iteration inside
// SingleNodeConsolidation.ComputeCommands and the binary search inside
// MultiNodeConsolidation.ComputeCommands. These are the pathological loops
// identified in kubernetes-sigs/karpenter#2972.
//
// This file uses controller-runtime's in-memory fake client with an index
// on spec.nodeName so that cluster.populateResourceRequests scans only pods
// for the target node instead of the full pod list. That keeps setup at 1000
// nodes in the sub-minute range instead of the tens of minutes an envtest
// apiserver + etcd round-trip per object costs.
//
// Benches are gated behind the test_performance build tag so this file is
// invisible to normal `go test ./...` invocations.
//
// The maximum cluster size across all benches is populated once and all sub-
// sizes are prefix-slices of the same candidate list, amortizing setup cost.
//
// To run locally:
//   go test -tags=test_performance -run='^$' \
//       -bench='BenchmarkSingleNodeConsolidation|BenchmarkMultiNodeConsolidation' \
//       -benchtime=1x -count=1 ./pkg/controllers/disruption/...

const benchMaxNodes = 1000

// Package-scoped bench context distinct from the ginkgo suite variables in
// suite_test.go so a `go test -tags=test_performance -bench=. -run=1` invocation
// runs only the benches, does not fire Ginkgo's BeforeSuite, and does not depend
// on envtest.
var (
	benchOnce       sync.Once
	benchCtx        context.Context
	benchCancel     context.CancelFunc
	benchClient     client.Client
	benchClock      *clocktesting.FakeClock
	benchCP         *fake.CloudProvider
	benchClusterCst *cost.ClusterCost
	benchCluster    *pstate.Cluster
	benchProv       *provisioning.Provisioner
	benchRecorder   *test.EventRecorder
	benchQueue      *disruption.Queue
	benchNodePools  []*v1.NodePool
	benchNodeClaims []*v1.NodeClaim
	benchNodes      []*corev1.Node
	benchInstType   *cloudprovider.InstanceType
)

func setupBench(b *testing.B) {
	b.Helper()
	benchOnce.Do(func() {
		log.SetLogger(logging.NopLogger)
		benchClock = clocktesting.NewFakeClock(stdtime.Now())
		benchCtx, benchCancel = context.WithCancel(TestContextWithLogger(b))
		benchCtx = injection.WithControllerName(benchCtx, "disruption-bench")
		benchCtx = options.ToContext(benchCtx, test.Options())

		// karpenter/v1 and test/v1alpha1 register themselves with
		// scheme.Scheme in their init() functions; blank-import above.
		benchClient = fakecr.NewClientBuilder().
			WithScheme(scheme.Scheme).
			WithStatusSubresource(&v1.NodeClaim{}, &v1.NodePool{}).
			WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
				return []string{obj.(*corev1.Pod).Spec.NodeName}
			}).
			Build()

		benchCP = fake.NewCloudProvider()
		benchCP.InstanceTypes = fake.InstanceTypesAssorted()
		benchClusterCst = cost.NewClusterCost(benchCtx, benchCP, benchClient)
		benchCluster = pstate.NewCluster(benchClock, benchClient, benchCP)
		benchRecorder = test.NewEventRecorder()
		draCtl := deviceallocation.NewController(benchClient)
		benchProv = provisioning.NewProvisioner(benchClient, benchRecorder, benchCP, benchCluster, benchClock, draCtl, virtualpods.NewVirtualPodCache(benchClient))
		benchQueue = disruption.NewQueue(benchClient, benchRecorder, benchCluster, benchClock, benchProv)

		// Pick the most expensive on-demand instance type as the cluster shape.
		// Consolidation attempts to find a cheaper replacement; every other
		// type is cheaper, so filterByPrice always has candidates.
		its := lo.Filter(benchCP.InstanceTypes, func(it *cloudprovider.InstanceType, _ int) bool {
			for _, o := range it.Offerings.Available() {
				if o.Requirements.Get(v1.CapacityTypeLabelKey).Any() == v1.CapacityTypeOnDemand {
					return true
				}
			}
			return false
		})
		sort.Slice(its, func(i, j int) bool { return its[i].Offerings.Cheapest().Price < its[j].Offerings.Cheapest().Price })
		benchInstType = its[len(its)-1]
		off := benchInstType.Offerings.Available()[0]
		zone := off.Requirements.Get(corev1.LabelTopologyZone).Any()
		capType := off.Requirements.Get(v1.CapacityTypeLabelKey).Any()

		benchNodePools = test.NodePools(3, v1.NodePool{
			Spec: v1.NodePoolSpec{
				Disruption: v1.Disruption{
					ConsolidationPolicy: v1.ConsolidationPolicyWhenEmptyOrUnderutilized,
					ConsolidateAfter:    v1.MustParseNillableDuration("0s"),
					Budgets:             []v1.Budget{{Nodes: "100%"}},
				},
			},
		})
		for _, np := range benchNodePools {
			if err := benchClient.Create(benchCtx, np); err != nil {
				b.Fatalf("create nodepool: %v", err)
			}
		}

		rs := test.ReplicaSet()
		if err := benchClient.Create(benchCtx, rs); err != nil {
			b.Fatalf("create replicaset: %v", err)
		}

		benchNodeClaims = make([]*v1.NodeClaim, 0, benchMaxNodes)
		benchNodes = make([]*corev1.Node, 0, benchMaxNodes)
		antiSelector := metav1.LabelSelector{MatchLabels: map[string]string{"bench": "stress"}}
		for i := 0; i < benchMaxNodes; i++ {
			np := benchNodePools[i%len(benchNodePools)]
			nc, nd := test.NodeClaimAndNode(v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1.NodePoolLabelKey:            np.Name,
						corev1.LabelInstanceTypeStable: benchInstType.Name,
						v1.CapacityTypeLabelKey:        capType,
						corev1.LabelTopologyZone:       zone,
					},
				},
				Status: v1.NodeClaimStatus{
					Allocatable: map[corev1.ResourceName]resource.Quantity{
						corev1.ResourceCPU:    resource.MustParse("32"),
						corev1.ResourceMemory: resource.MustParse("128Gi"),
						corev1.ResourcePods:   resource.MustParse("100"),
					},
				},
			})
			// Mark as Launched/Registered/Initialized so the state controller
			// treats the node as usable, and Consolidatable so it is a valid
			// disruption candidate.
			nc.StatusConditions().SetTrue(v1.ConditionTypeLaunched)
			nc.StatusConditions().SetTrue(v1.ConditionTypeRegistered)
			nc.StatusConditions().SetTrue(v1.ConditionTypeInitialized)
			nc.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)
			// Match ExpectMakeNodesReady/Initialized: taint-free, Ready=True,
			// registered+initialized labels set.
			nd.Spec.Taints = nil
			if nd.Labels == nil {
				nd.Labels = map[string]string{}
			}
			nd.Labels[v1.NodeRegisteredLabelKey] = "true"
			nd.Labels[v1.NodeInitializedLabelKey] = "true"
			nd.Status.Phase = corev1.NodeRunning
			nd.Status.Conditions = []corev1.NodeCondition{{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionTrue,
				LastHeartbeatTime:  metav1.NewTime(benchClock.Now()),
				LastTransitionTime: metav1.NewTime(benchClock.Now()),
				Reason:             "KubeletReady",
			}}
			if err := benchClient.Create(benchCtx, nc); err != nil {
				b.Fatalf("create nodeclaim: %v", err)
			}
			if err := benchClient.Status().Update(benchCtx, nc); err != nil {
				b.Fatalf("status update nodeclaim: %v", err)
			}
			if err := benchClient.Create(benchCtx, nd); err != nil {
				b.Fatalf("create node: %v", err)
			}
			if err := benchClient.Status().Update(benchCtx, nd); err != nil {
				b.Fatalf("status update node: %v", err)
			}

			pod := test.Pod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"bench": "stress",
						"app":   fmt.Sprintf("bench-%d", i),
					},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion:         "apps/v1",
						Kind:               "ReplicaSet",
						Name:               rs.Name,
						UID:                rs.UID,
						Controller:         lo.ToPtr(true),
						BlockOwnerDeletion: lo.ToPtr(true),
					}},
				},
				PodAntiRequirements: []corev1.PodAffinityTerm{{
					LabelSelector: &antiSelector,
					TopologyKey:   corev1.LabelHostname,
				}},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
				},
			})
			pod.Spec.NodeName = nd.Name
			if err := benchClient.Create(benchCtx, pod); err != nil {
				b.Fatalf("create pod: %v", err)
			}

			benchCluster.UpdateNodeClaim(nc)
			if err := benchCluster.UpdateNode(benchCtx, nd); err != nil {
				b.Fatalf("cluster.UpdateNode: %v", err)
			}
			benchNodeClaims = append(benchNodeClaims, nc)
			benchNodes = append(benchNodes, nd)
		}
	})
}

// candidatesForBench builds fresh disruption.Candidate values against the
// pre-populated cluster and returns the first numNodes of them.
func candidatesForBench(b *testing.B, m disruption.Method, numNodes int) (map[string]int, []*disruption.Candidate) {
	b.Helper()
	benchCluster.MarkUnconsolidated()
	budgets, err := disruption.BuildDisruptionBudgetMapping(benchCtx, benchCluster, benchClock, benchClient, benchCP, benchRecorder, m.Reason())
	if err != nil {
		b.Fatalf("build disruption budgets: %v", err)
	}
	cands, err := disruption.GetCandidates(benchCtx, benchCluster, benchClient, benchRecorder, benchClock, benchCP, m.ShouldDisrupt, m.Class(), benchQueue)
	if err != nil {
		b.Fatalf("get candidates: %v", err)
	}
	if len(cands) < numNodes {
		b.Fatalf("expected at least %d candidates, got %d", numNodes, len(cands))
	}
	return budgets, cands[:numNodes]
}

func BenchmarkSingleNodeConsolidation_ComputeCommands_100(b *testing.B) {
	benchmarkSingleNodeConsolidation(b, 100)
}
func BenchmarkSingleNodeConsolidation_ComputeCommands_400(b *testing.B) {
	benchmarkSingleNodeConsolidation(b, 400)
}
func BenchmarkSingleNodeConsolidation_ComputeCommands_1000(b *testing.B) {
	benchmarkSingleNodeConsolidation(b, 1000)
}

func benchmarkSingleNodeConsolidation(b *testing.B, numNodes int) {
	setupBench(b)
	c := disruption.MakeConsolidation(benchClock, benchCluster, benchClient, benchProv, benchCP, benchRecorder, benchQueue)
	singleNode := disruption.NewSingleNodeConsolidation(c, disruption.WithValidator(NopValidator{}))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		budgets, cands := candidatesForBench(b, singleNode, numNodes)
		b.StartTimer()
		if _, err := singleNode.ComputeCommands(benchCtx, budgets, cands...); err != nil {
			b.Fatalf("compute commands: %v", err)
		}
	}
}

// BenchmarkMultiNodeConsolidation_ComputeCommands is table-driven to surface
// the log2(N) shape of firstNConsolidationOption's binary search across a
// range of candidate counts. The MultiNode ComputeCommands path caps its
// batch at 100 (maxParallel), so counts above 100 exercise the same window.
func BenchmarkMultiNodeConsolidation_ComputeCommands(b *testing.B) {
	for _, numNodes := range []int{10, 50, 100} {
		b.Run(fmt.Sprintf("%d", numNodes), func(sub *testing.B) {
			benchmarkMultiNodeConsolidation(sub, numNodes)
		})
	}
}

func benchmarkMultiNodeConsolidation(b *testing.B, numNodes int) {
	setupBench(b)
	c := disruption.MakeConsolidation(benchClock, benchCluster, benchClient, benchProv, benchCP, benchRecorder, benchQueue)
	multi := disruption.NewMultiNodeConsolidation(c, disruption.WithValidator(NopValidator{}))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		budgets, cands := candidatesForBench(b, multi, numNodes)
		b.StartTimer()
		if _, err := multi.ComputeCommands(benchCtx, budgets, cands...); err != nil {
			b.Fatalf("compute commands: %v", err)
		}
	}
}
