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

package provisioning_test

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/samber/lo"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	clocktesting "k8s.io/utils/clock/testing"
	fakecr "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/fake"
	"sigs.k8s.io/karpenter/pkg/controllers/provisioning"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	"sigs.k8s.io/karpenter/pkg/operator/logging"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/test"
	_ "sigs.k8s.io/karpenter/pkg/test/v1alpha1"
)

func init() {
	log.SetLogger(logging.NopLogger)
}

//nolint:gosec
var benchRand = rand.New(rand.NewSource(42))

// BenchmarkNewSchedulerEndToEnd exercises the full Provisioner.NewScheduler() path
// which includes ListManaged (kube list), per-NodePool GetInstanceTypes, volume
// topology requirements lookup, NewTopology, and getDaemonSetPods. This benchmark
// measures the composite Phase-A (pre-Solve) setup cost and catches latent regressions
// in any component of that chain.
//
// To run:
//
//	go test -tags=test_performance -run=XXX -bench=BenchmarkNewSchedulerEndToEnd -count=10 -benchmem ./pkg/controllers/provisioning/
//
// To compare before/after with benchstat:
//
//	go test -tags=test_performance -run=XXX -bench=BenchmarkNewSchedulerEndToEnd -count=10 -benchmem | tee /tmp/old
//	# make changes
//	go test -tags=test_performance -run=XXX -bench=BenchmarkNewSchedulerEndToEnd -count=10 -benchmem | tee /tmp/new
//	benchstat /tmp/old /tmp/new
func BenchmarkNewSchedulerEndToEnd(b *testing.B) {
	type benchCase struct {
		nodePoolCount  int
		podCount       int
		daemonSetCount int
	}
	cases := []benchCase{
		{nodePoolCount: 1, podCount: 100, daemonSetCount: 3},
		{nodePoolCount: 5, podCount: 500, daemonSetCount: 5},
		{nodePoolCount: 10, podCount: 1000, daemonSetCount: 8},
		{nodePoolCount: 20, podCount: 1000, daemonSetCount: 10},
	}

	for _, c := range cases {
		name := fmt.Sprintf("%dNP_%dPods_%dDS", c.nodePoolCount, c.podCount, c.daemonSetCount)
		b.Run(name, func(b *testing.B) {
			benchmarkNewSchedulerEndToEnd(b, c.nodePoolCount, c.podCount, c.daemonSetCount)
		})
	}
}

func benchmarkNewSchedulerEndToEnd(b *testing.B, nodePoolCount, podCount, daemonSetCount int) {
	ctx := options.ToContext(injection.WithControllerName(context.Background(), "provisioner"), test.Options())

	cp := fake.NewCloudProvider()
	cp.InstanceTypes = fake.InstanceTypes(100)

	nodePools := make([]*v1.NodePool, nodePoolCount)
	for i := range nodePools {
		nodePools[i] = test.NodePool(v1.NodePool{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("bench-pool-%d", i)},
			Spec: v1.NodePoolSpec{
				Limits: v1.Limits{
					corev1.ResourceCPU:    resource.MustParse("10000000"),
					corev1.ResourceMemory: resource.MustParse("10000000Gi"),
				},
			},
		})
	}

	daemonSets := make([]appsv1.DaemonSet, daemonSetCount)
	for i := range daemonSets {
		daemonSets[i] = appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("bench-ds-%d", i),
				Namespace: "kube-system",
			},
			Spec: appsv1.DaemonSetSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": fmt.Sprintf("ds-%d", i)},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": fmt.Sprintf("ds-%d", i)},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  "agent",
							Image: "agent:latest",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
						}},
					},
				},
			},
		}
	}

	clientBuilder := fakecr.NewClientBuilder()
	for _, np := range nodePools {
		clientBuilder.WithObjects(np)
	}
	for i := range daemonSets {
		clientBuilder.WithObjects(&daemonSets[i])
	}
	kubeClient := clientBuilder.Build()

	clk := clocktesting.NewFakeClock(time.Now())
	cluster := state.NewCluster(clk, kubeClient, cp)
	prov := provisioning.NewProvisioner(kubeClient, events.NewRecorder(&record.FakeRecorder{}), cp, cluster, clk)

	pods := makeBenchPods(podCount)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sched, err := prov.NewScheduler(ctx, pods, nil)
		if err != nil {
			b.Fatalf("NewScheduler failed: %v", err)
		}
		if sched == nil {
			b.Fatal("NewScheduler returned nil scheduler")
		}
	}
}

func makeBenchPods(count int) []*corev1.Pod {
	pods := make([]*corev1.Pod, count)
	cpuValues := []int{100, 250, 500, 1000, 1500}
	memValues := []int{100, 256, 512, 1024, 2048}
	for i := range pods {
		labels := map[string]string{
			"app": fmt.Sprintf("bench-%d", benchRand.Intn(20)),
		}
		pods[i] = test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				Labels:    labels,
				Namespace: "default",
				UID:       uuid.NewUUID(),
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", cpuValues[benchRand.Intn(len(cpuValues))])),
					corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", memValues[benchRand.Intn(len(memValues))])),
				},
			},
			TopologySpreadConstraints: lo.Ternary(benchRand.Float32() < 0.3,
				[]corev1.TopologySpreadConstraint{{
					MaxSkew:           1,
					TopologyKey:       corev1.LabelTopologyZone,
					WhenUnsatisfiable: corev1.DoNotSchedule,
					LabelSelector:     &metav1.LabelSelector{MatchLabels: labels},
				}},
				nil,
			),
		})
	}
	return pods
}
