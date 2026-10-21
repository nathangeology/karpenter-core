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

package metrics_test

import (
	"fmt"
	"strings"
	"testing"

	opmetrics "github.com/awslabs/operatorpkg/metrics"
	"github.com/awslabs/operatorpkg/status"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/metrics"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var testGauge1, testGauge2 opmetrics.GaugeMetric
var nodeClaimStatusController *status.Controller[*v1.NodeClaim]
var nodeStatusController *status.GenericObjectController[*corev1.Node]

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Store")
}

var _ = BeforeSuite(func() {
	testGauge1 = opmetrics.NewPrometheusGauge(crmetrics.Registry, prometheus.GaugeOpts{Name: "test_gauge_1"}, []opmetrics.Label{{Name: "label_1"}, {Name: "label_2"}}, opmetrics.Alpha)
	testGauge2 = opmetrics.NewPrometheusGauge(crmetrics.Registry, prometheus.GaugeOpts{Name: "test_gauge_2"}, []opmetrics.Label{{Name: "label_1"}, {Name: "label_2"}}, opmetrics.Alpha)
	nodeClaimStatusController = metrics.NodePoolScopedStatusMetrics(v1.NodePoolLabelKey, status.NewController[*v1.NodeClaim](
		nil,
		record.NewFakeRecorder(10),
		status.WithLabels(v1.NodePoolLabelKey),
	))
	nodeStatusController = metrics.NodePoolScopedGenericStatusMetrics(v1.NodePoolLabelKey, status.NewGenericObjectController[*corev1.Node](
		nil,
		record.NewFakeRecorder(10),
		status.WithLabels(v1.NodePoolLabelKey),
	))
})

var _ = Describe("Store", func() {
	var ms *metrics.Store
	var key client.ObjectKey

	BeforeEach(func() {
		ms = metrics.NewStore()
		key = client.ObjectKey{Namespace: "default", Name: "test"}
	})
	Context("Update", func() {
		It("should create metrics when calling update", func() {
			storeMetrics := []*metrics.StoreMetric{
				{
					GaugeMetric: testGauge1,
					Value:       3.65,
					Labels: map[string]string{
						"label_1": "test",
						"label_2": "test",
					},
				},
				{
					GaugeMetric: testGauge2,
					Value:       5.3,
					Labels: map[string]string{
						"label_1": "test",
						"label_2": "test",
					},
				},
			}
			ms.Update(key.String(), storeMetrics)

			// Expect to find the metrics with the correct values
			m, ok := FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeTrue())
			Expect(lo.FromPtr(m.Gauge.Value)).To(BeNumerically("==", storeMetrics[0].Value))

			m, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeTrue())
			Expect(lo.FromPtr(m.Gauge.Value)).To(BeNumerically("==", storeMetrics[1].Value))

		})
		It("should delete metrics when calling update", func() {
			storeMetrics := []*metrics.StoreMetric{
				{
					GaugeMetric: testGauge1,
					Value:       3.65,
					Labels: map[string]string{
						"label_1": "test",
						"label_2": "test",
					},
				},
				{
					GaugeMetric: testGauge2,
					Value:       5.3,
					Labels: map[string]string{
						"label_1": "test",
						"label_2": "test",
					},
				},
			}
			ms.Update(key.String(), storeMetrics)

			newStoreMetrics := []*metrics.StoreMetric{
				{
					GaugeMetric: testGauge1,
					Value:       3.65,
					Labels: map[string]string{
						"label_1": "test_2",
						"label_2": "test_2",
					},
				},
				{
					GaugeMetric: testGauge2,
					Value:       5.3,
					Labels: map[string]string{
						"label_1": "test_2",
						"label_2": "test_2",
					},
				},
			}
			ms.Update(key.String(), newStoreMetrics)

			// Expect to not find the old metrics
			_, ok := FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeFalse())
			_, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeFalse())

			// Expect to find the new metrics with the correct values
			m, ok := FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test_2", "label_2": "test_2"})
			Expect(ok).To(BeTrue())
			Expect(lo.FromPtr(m.Gauge.Value)).To(BeNumerically("==", newStoreMetrics[0].Value))
			m, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test_2", "label_2": "test_2"})
			Expect(ok).To(BeTrue())
			Expect(lo.FromPtr(m.Gauge.Value)).To(BeNumerically("==", newStoreMetrics[1].Value))
		})
		It("should consider metrics equal with the same labels", func() {
			storeMetrics := []*metrics.StoreMetric{
				{
					GaugeMetric: testGauge1,
					Value:       3.65,
					Labels: map[string]string{
						"label_1": "test",
						"label_2": "test",
					},
				},
				{
					GaugeMetric: testGauge2,
					Value:       5.3,
					Labels: map[string]string{
						"label_1": "test",
						"label_2": "test",
					},
				},
			}
			ms.Update(key.String(), storeMetrics)

			// Expect to find the metrics with the correct values
			m, ok := FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeTrue())
			Expect(lo.FromPtr(m.Gauge.Value)).To(BeNumerically("==", storeMetrics[0].Value))

			m, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeTrue())
			Expect(lo.FromPtr(m.Gauge.Value)).To(BeNumerically("==", storeMetrics[1].Value))

			// Flip around the labels in the map
			newStoreMetrics := []*metrics.StoreMetric{
				{
					GaugeMetric: testGauge1,
					Value:       4.5,
					Labels: map[string]string{
						"label_2": "test",
						"label_1": "test",
					},
				},
				{
					GaugeMetric: testGauge2,
					Value:       6.9,
					Labels: map[string]string{
						"label_2": "test",
						"label_1": "test",
					},
				},
			}
			ms.Update(key.String(), newStoreMetrics)

			// Expect to find the metrics with the new values
			m, ok = FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeTrue())
			Expect(lo.FromPtr(m.Gauge.Value)).To(BeNumerically("==", newStoreMetrics[0].Value))

			m, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeTrue())
			Expect(lo.FromPtr(m.Gauge.Value)).To(BeNumerically("==", newStoreMetrics[1].Value))
		})
	})
	Context("Delete", func() {
		It("should delete metrics by key", func() {
			storeMetrics := []*metrics.StoreMetric{
				{
					GaugeMetric: testGauge1,
					Value:       3.65,
					Labels: map[string]string{
						"label_1": "test",
						"label_2": "test",
					},
				},
				{
					GaugeMetric: testGauge2,
					Value:       5.3,
					Labels: map[string]string{
						"label_1": "test",
						"label_2": "test",
					},
				},
			}
			ms.Update(key.String(), storeMetrics)

			// Expect to find the metrics
			_, ok := FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeTrue())
			_, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeTrue())

			ms.Delete(key.String())

			// Expect the metrics to be gone
			_, ok = FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeFalse())
			_, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeFalse())
		})
		It("should delete the metrics if key didn't previously exist", func() {
			ms.Delete(key.String())
		})
	})
	Context("ReplaceAll", func() {
		It("should replace all metrics", func() {
			storeMetrics := []*metrics.StoreMetric{
				{
					GaugeMetric: testGauge1,
					Value:       3.65,
					Labels: map[string]string{
						"label_1": "test",
						"label_2": "test",
					},
				},
				{
					GaugeMetric: testGauge2,
					Value:       5.3,
					Labels: map[string]string{
						"label_1": "test",
						"label_2": "test",
					},
				},
			}
			ms.Update(key.String(), storeMetrics)

			// Expect to find the metrics
			_, ok := FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeTrue())
			_, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeTrue())

			key2 := client.ObjectKey{Namespace: "default", Name: "test2"}
			key3 := client.ObjectKey{Namespace: "default", Name: "test3"}

			newStore := map[string][]*metrics.StoreMetric{
				key2.String(): {
					{
						GaugeMetric: testGauge1,
						Value:       3.65,
						Labels: map[string]string{
							"label_1": "test2",
							"label_2": "test2",
						},
					},
					{
						GaugeMetric: testGauge2,
						Value:       4.3,
						Labels: map[string]string{
							"label_1": "test2",
							"label_2": "test2",
						},
					},
				},
				key3.String(): {
					{
						GaugeMetric: testGauge1,
						Value:       2.1,
						Labels: map[string]string{
							"label_1": "test3",
							"label_2": "test3",
						},
					},
					{
						GaugeMetric: testGauge2,
						Value:       8.9,
						Labels: map[string]string{
							"label_1": "test3",
							"label_2": "test3",
						},
					},
				},
			}
			ms.ReplaceAll(newStore)

			// Expect to not find the old metrics
			_, ok = FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeFalse())
			_, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeFalse())

			// Expect to find the new metrics for test2
			_, ok = FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test2", "label_2": "test2"})
			Expect(ok).To(BeTrue())
			_, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test2", "label_2": "test2"})
			Expect(ok).To(BeTrue())

			// Expect to find the new metrics for test3
			_, ok = FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test3", "label_2": "test3"})
			Expect(ok).To(BeTrue())
			_, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test3", "label_2": "test3"})
			Expect(ok).To(BeTrue())
		})
		It("should replace with an empty store", func() {
			storeMetrics := []*metrics.StoreMetric{
				{
					GaugeMetric: testGauge1,
					Value:       3.65,
					Labels: map[string]string{
						"label_1": "test",
						"label_2": "test",
					},
				},
				{
					GaugeMetric: testGauge2,
					Value:       5.3,
					Labels: map[string]string{
						"label_1": "test",
						"label_2": "test",
					},
				},
			}
			ms.Update(key.String(), storeMetrics)

			// Expect to find the metrics
			_, ok := FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeTrue())
			_, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeTrue())

			ms.ReplaceAll(nil)

			// Expect to not find the metrics now
			_, ok = FindMetricWithLabelValues("test_gauge_1", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeFalse())
			_, ok = FindMetricWithLabelValues("test_gauge_2", map[string]string{"label_1": "test", "label_2": "test"})
			Expect(ok).To(BeFalse())
		})
	})
})

var _ = Describe("NodePoolScoped", func() {
	It("should panic on a metric that does not carry the nodepool dimension", func() {
		Expect(func() {
			metrics.NodePoolScoped(opmetrics.NewPrometheusGauge(
				prometheus.NewRegistry(),
				prometheus.GaugeOpts{Name: "test_gauge_unscoped"},
				[]opmetrics.Label{{Name: "label_1"}},
				opmetrics.Alpha,
			))
		}).To(PanicWith(ContainSubstring(metrics.NodePoolLabel)))
	})
})

// statusConditionMetrics are the metrics operatorpkg emits per kind that its own cleanupNotFound
// does not reap, so that the specs assert over all of them rather than over a sample. Both kinds
// Karpenter gives a NodePool dimension are covered, since each goes through a different
// registration entry point.
func statusConditionMetrics() map[string]any {
	return map[string]any{
		"operator_nodeclaim_status_condition_transition_seconds": nodeClaimStatusController.ConditionDuration,
		"operator_nodeclaim_status_condition_transitions_total":  nodeClaimStatusController.ConditionTransitionsTotal,
		"operator_nodeclaim_termination_duration_seconds":        nodeClaimStatusController.TerminationDuration,
		"operator_node_status_condition_transition_seconds":      nodeStatusController.ConditionDuration,
		"operator_node_status_condition_transitions_total":       nodeStatusController.ConditionTransitionsTotal,
		"operator_node_termination_duration_seconds":             nodeStatusController.TerminationDuration,
	}
}

var _ = Describe("NodePoolScopedStatusMetrics", func() {
	var nodePoolDimension string
	BeforeEach(func() {
		nodePoolDimension = strings.NewReplacer("/", "_", ".", "_", "-", "_").Replace(v1.NodePoolLabelKey)
	})
	It("should name the NodePool dimension exactly as operatorpkg derives it from the label key", func() {
		Expect(statusConditionMetrics()).To(HaveLen(6))
		for name, metric := range statusConditionMetrics() {
			By(name)
			labelNames := lo.Map(metric.(interface{ Labels() []opmetrics.Label }).Labels(), func(l opmetrics.Label, _ int) string { return l.Name })
			Expect(labelNames).To(ContainElement(nodePoolDimension))
		}
	})
	It("should panic when the given label key is not one of the controller's dimensions", func() {
		Expect(func() {
			metrics.NodePoolScopedStatusMetrics("karpenter.sh/not-a-declared-label", nodeClaimStatusController)
		}).To(PanicWith(ContainSubstring("karpenter_sh_not_a_declared_label")))
		Expect(func() {
			metrics.NodePoolScopedGenericStatusMetrics("karpenter.sh/not-a-declared-label", nodeStatusController)
		}).To(PanicWith(ContainSubstring("karpenter_sh_not_a_declared_label")))
	})
	It("should drop a deleted NodePool's children and leave another NodePool's in place", func() {
		Expect(statusConditionMetrics()).To(HaveLen(6))
		for name, metric := range statusConditionMetrics() {
			By(name)
			emitWithNodePool(metric, nodePoolDimension, "deleted-nodepool")
			emitWithNodePool(metric, nodePoolDimension, "surviving-nodepool")
			_, ok := FindMetricWithLabelValues(name, map[string]string{nodePoolDimension: "deleted-nodepool"})
			Expect(ok).To(BeTrue())
		}

		metrics.DeleteNodePool("deleted-nodepool")

		for name := range statusConditionMetrics() {
			By(name)
			_, ok := FindMetricWithLabelValues(name, map[string]string{nodePoolDimension: "deleted-nodepool"})
			Expect(ok).To(BeFalse())
			_, ok = FindMetricWithLabelValues(name, map[string]string{nodePoolDimension: "surviving-nodepool"})
			Expect(ok).To(BeTrue())
		}
	})
})

// emitWithNodePool creates one child of metric for nodePoolName, filling the metric's other
// dimensions with a placeholder.
func emitWithNodePool(metric any, nodePoolDimension, nodePoolName string) {
	GinkgoHelper()
	labels := map[string]string{}
	for _, label := range metric.(interface{ Labels() []opmetrics.Label }).Labels() {
		labels[label.Name] = "test"
	}
	Expect(labels).To(HaveKey(nodePoolDimension))
	labels[nodePoolDimension] = nodePoolName

	switch typed := metric.(type) {
	case opmetrics.CounterMetric:
		typed.Inc(labels)
	case opmetrics.ObservationMetric:
		typed.Observe(1, labels)
	default:
		Fail(fmt.Sprintf("metric %T is none of the opmetrics metric kinds", metric))
	}
}
