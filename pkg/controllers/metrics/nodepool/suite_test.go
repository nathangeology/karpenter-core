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

package nodepool_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	opmetrics "github.com/awslabs/operatorpkg/metrics"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	prometheusmodel "github.com/prometheus/client_model/go"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"sigs.k8s.io/karpenter/pkg/apis"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/fake"
	cloudprovidermetrics "sigs.k8s.io/karpenter/pkg/cloudprovider/metrics"
	"sigs.k8s.io/karpenter/pkg/controllers/disruption"
	"sigs.k8s.io/karpenter/pkg/controllers/metrics/nodepool"
	"sigs.k8s.io/karpenter/pkg/controllers/node/termination"
	"sigs.k8s.io/karpenter/pkg/controllers/nodeclaim/lifecycle"
	"sigs.k8s.io/karpenter/pkg/metrics"
	"sigs.k8s.io/karpenter/pkg/state/cost"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var nodePoolController *nodepool.Controller
var ctx context.Context
var env *test.Environment
var cp *fake.CloudProvider
var cc *cost.ClusterCost

func TestAPIs(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "NodePoolMetrics")
}

var _ = BeforeSuite(func() {
	env = test.NewEnvironment(test.WithCRDs(apis.CRDs...), test.WithCRDs(v1alpha1.CRDs...))
	cp = fake.NewCloudProvider()
	cc = cost.NewClusterCost(ctx, cp, env.Client)
	nodePoolController = nodepool.NewController(env.Client, cp, cc)
})

var _ = AfterSuite(func() {
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = Describe("Metrics", func() {
	var nodePool *v1.NodePool
	BeforeEach(func() {
		nodePool = test.NodePool(v1.NodePool{
			Spec: v1.NodePoolSpec{
				Template: v1.NodeClaimTemplate{
					Spec: v1.NodeClaimTemplateSpec{
						NodeClassRef: &v1.NodeClassReference{
							Group: "karpenter.test.sh",
							Kind:  "TestNodeClass",
							Name:  "default",
						},
					},
				},
			},
		})
	})
	DescribeTable(
		"should update the nodepool limit metrics",
		func(isNodePoolManaged bool) {
			limits := v1.Limits{
				corev1.ResourceCPU:              resource.MustParse("10"),
				corev1.ResourceMemory:           resource.MustParse("10Mi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
			}
			nodePool.Spec.Limits = limits
			if !isNodePoolManaged {
				nodePool.Spec.Template.Spec.NodeClassRef = &v1.NodeClassReference{
					Group: "karpenter.test.sh",
					Kind:  "UnmanagedNodeClass",
					Name:  "default",
				}
			}
			ExpectApplied(ctx, env.Client, nodePool)
			ExpectReconcileSucceeded(ctx, nodePoolController, client.ObjectKeyFromObject(nodePool))

			for k, v := range limits {
				m, found := FindMetricWithLabelValues("karpenter_nodepools_limit", map[string]string{
					metrics.NodePoolLabel:     nodePool.GetName(),
					metrics.ResourceTypeLabel: strings.ReplaceAll(k.String(), "-", "_"),
				})
				Expect(found).To(Equal(isNodePoolManaged))
				if isNodePoolManaged {
					Expect(m.GetGauge().GetValue()).To(BeNumerically("~", v.AsApproximateFloat64()))
				}
			}
		},
		Entry("should update the nodepool limit metrics", true),
		Entry("should ignore nodepools not managed by this instance of Karpenter", false),
	)
	It("should update the nodepool usage metrics", func() {
		resources := corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse("10"),
			corev1.ResourceMemory:           resource.MustParse("10Mi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
		}
		nodePool.Status.Resources = resources

		ExpectApplied(ctx, env.Client, nodePool)
		ExpectReconcileSucceeded(ctx, nodePoolController, client.ObjectKeyFromObject(nodePool))

		for k, v := range resources {
			m, found := FindMetricWithLabelValues("karpenter_nodepools_usage", map[string]string{
				metrics.NodePoolLabel:     nodePool.GetName(),
				metrics.ResourceTypeLabel: strings.ReplaceAll(k.String(), "-", "_"),
			})
			Expect(found).To(BeTrue())
			Expect(m.GetGauge().GetValue()).To(BeNumerically("~", v.AsApproximateFloat64()))
		}
	})
	It("should delete the nodepool state metrics on nodepool delete", func() {
		expectedMetrics := []string{"karpenter_nodepools_limit", "karpenter_nodepools_usage"}
		nodePool.Spec.Limits = v1.Limits{
			corev1.ResourceCPU:              resource.MustParse("100"),
			corev1.ResourceMemory:           resource.MustParse("100Mi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("1000Gi"),
		}
		nodePool.Status.Resources = corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse("10"),
			corev1.ResourceMemory:           resource.MustParse("10Mi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
		}
		ExpectApplied(ctx, env.Client, nodePool)
		ExpectReconcileSucceeded(ctx, nodePoolController, client.ObjectKeyFromObject(nodePool))

		for _, name := range expectedMetrics {
			_, found := FindMetricWithLabelValues(name, map[string]string{
				metrics.NodePoolLabel: nodePool.GetName(),
			})
			Expect(found).To(BeTrue())
		}

		ExpectDeleted(ctx, env.Client, nodePool)
		ExpectReconcileSucceeded(ctx, nodePoolController, client.ObjectKeyFromObject(nodePool))

		for _, name := range expectedMetrics {
			_, found := FindMetricWithLabelValues(name, map[string]string{
				metrics.NodePoolLabel: nodePool.GetName(),
			})
			Expect(found).To(BeFalse())
		}
	})
	Context("NodePool scoped metrics owned by other controllers", func() {
		var nodeClaim *v1.NodeClaim
		BeforeEach(func() {
			for _, metric := range nodePoolScopedMetrics {
				emitNodePoolScopedMetric(metric, nodePool.Name)
			}
			Expect(familiesWithNodePool(nodePool.Name)).To(HaveLen(len(nodePoolScopedMetrics)))
			nodeClaim = test.NodeClaim(v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
				Spec:       v1.NodeClaimSpec{NodeClassRef: nodePool.Spec.Template.Spec.NodeClassRef},
			})
		})
		It("should delete every NodePool scoped metric's children on nodepool delete", func() {
			ExpectApplied(ctx, env.Client, nodePool)
			ExpectDeleted(ctx, env.Client, nodePool)

			Expect(ExpectReconcileSucceeded(ctx, nodePoolController, client.ObjectKeyFromObject(nodePool)).RequeueAfter).To(BeZero())
			Expect(familiesWithNodePool(nodePool.Name)).To(BeEmpty())
		})
		It("should retain them while the deleted NodePool still has a NodeClaim, since its termination still writes them", func() {
			ExpectApplied(ctx, env.Client, nodePool, nodeClaim)
			ExpectDeleted(ctx, env.Client, nodePool)

			Expect(ExpectReconcileSucceeded(ctx, nodePoolController, client.ObjectKeyFromObject(nodePool)).RequeueAfter).To(Equal(10 * time.Second))
			Expect(familiesWithNodePool(nodePool.Name)).To(HaveLen(len(nodePoolScopedMetrics)))
		})
		It("should delete them on the requeue that follows the last NodeClaim going away", func() {
			ExpectApplied(ctx, env.Client, nodePool, nodeClaim)
			ExpectDeleted(ctx, env.Client, nodePool)
			ExpectReconcileSucceeded(ctx, nodePoolController, client.ObjectKeyFromObject(nodePool))
			ExpectDeleted(ctx, env.Client, nodeClaim)

			Expect(ExpectReconcileSucceeded(ctx, nodePoolController, client.ObjectKeyFromObject(nodePool)).RequeueAfter).To(BeZero())
			Expect(familiesWithNodePool(nodePool.Name)).To(BeEmpty())
		})
		It("should leave another NodePool's children in place", func() {
			other := test.NodePool(v1.NodePool{Spec: nodePool.Spec})
			for _, metric := range nodePoolScopedMetrics {
				emitNodePoolScopedMetric(metric, other.Name)
			}
			ExpectApplied(ctx, env.Client, nodePool, other)
			ExpectDeleted(ctx, env.Client, nodePool)
			ExpectReconcileSucceeded(ctx, nodePoolController, client.ObjectKeyFromObject(nodePool))

			Expect(familiesWithNodePool(nodePool.Name)).To(BeEmpty())
			Expect(familiesWithNodePool(other.Name)).To(HaveLen(len(nodePoolScopedMetrics)))
		})
	})
})

// nodePoolScopedMetrics is the full set of metrics declared with metrics.NodePoolScoped, so that
// the specs above assert over all of them rather than over a sample.
var nodePoolScopedMetrics = []any{
	cloudprovidermetrics.ErrorsTotal,
	disruption.ConsolidationMovesTotal,
	disruption.ConsolidationScoreHistogram,
	disruption.NodeClaimsUnhealthyDisruptedTotal,
	disruption.NodePoolAllowedDisruptions,
	disruption.NodePoolNodesConsumingBudgets,
	disruption.NodepoolDecisionsPerformed,
	disruption.TerminateFirstDecisionsTotal,
	lifecycle.InstanceTerminationDurationSeconds,
	lifecycle.NodeClaimTerminationDurationSeconds,
	metrics.NodeClaimsCreatedTotal,
	metrics.NodeClaimsDisruptedTotal,
	metrics.NodeClaimsTerminatedTotal,
	metrics.NodesCreatedTotal,
	metrics.NodesTerminatedTotal,
	metrics.PodsDisruptionInitiatedTotal,
	cost.CostTrackingErrorsTotal,
	termination.DurationSeconds,
	termination.NodeLifetimeDurationSeconds,
	termination.NodesDrainedTotal,
}

// emitNodePoolScopedMetric creates one child of metric for nodePoolName, filling the metric's
// other dimensions with a placeholder.
func emitNodePoolScopedMetric(metric any, nodePoolName string) {
	GinkgoHelper()
	labels := map[string]string{}
	for _, label := range metric.(interface{ Labels() []opmetrics.Label }).Labels() {
		labels[label.Name] = "test"
	}
	Expect(labels).To(HaveKey(metrics.NodePoolLabel))
	labels[metrics.NodePoolLabel] = nodePoolName

	switch typed := metric.(type) {
	case opmetrics.GaugeMetric:
		typed.Set(1, labels)
	case opmetrics.CounterMetric:
		typed.Inc(labels)
	case opmetrics.ObservationMetric:
		typed.Observe(1, labels)
	default:
		Fail(fmt.Sprintf("metric %T is none of the opmetrics metric kinds", metric))
	}
}

// familiesWithNodePool returns the names of the metric families holding at least one series
// labeled with the given NodePool.
func familiesWithNodePool(nodePoolName string) []string {
	GinkgoHelper()
	families, err := crmetrics.Registry.Gather()
	Expect(err).ToNot(HaveOccurred())

	return lo.FilterMap(families, func(family *prometheusmodel.MetricFamily, _ int) (string, bool) {
		return family.GetName(), lo.ContainsBy(family.GetMetric(), func(m *prometheusmodel.Metric) bool {
			return lo.ContainsBy(m.GetLabel(), func(pair *prometheusmodel.LabelPair) bool {
				return pair.GetName() == metrics.NodePoolLabel && pair.GetValue() == nodePoolName
			})
		})
	})
}
