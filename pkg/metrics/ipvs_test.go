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
	"testing"

	opmetrics "github.com/awslabs/operatorpkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"sigs.k8s.io/karpenter/pkg/metrics"
)

// Requirements: 7.1, 7.2, 7.3, 7.4

// --- IPVSResourceAdjustmentTotal tests ---

func TestIPVSResourceAdjustmentTotal_IncrementCPU(t *testing.T) {
	metrics.IPVSResourceAdjustmentTotal.Reset()

	counter := metrics.IPVSResourceAdjustmentTotal.(*opmetrics.PrometheusCounter)
	before := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "cpu"}))

	metrics.IPVSResourceAdjustmentTotal.Inc(map[string]string{metrics.ResourceTypeLabel: "cpu"})

	after := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "cpu"}))
	if after-before != 1 {
		t.Fatalf("expected cpu counter to increment by 1, got delta %v", after-before)
	}
}

func TestIPVSResourceAdjustmentTotal_IncrementMemory(t *testing.T) {
	metrics.IPVSResourceAdjustmentTotal.Reset()

	counter := metrics.IPVSResourceAdjustmentTotal.(*opmetrics.PrometheusCounter)
	before := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "memory"}))

	metrics.IPVSResourceAdjustmentTotal.Inc(map[string]string{metrics.ResourceTypeLabel: "memory"})

	after := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "memory"}))
	if after-before != 1 {
		t.Fatalf("expected memory counter to increment by 1, got delta %v", after-before)
	}
}

func TestIPVSResourceAdjustmentTotal_ResetZerosCounters(t *testing.T) {
	// Increment both labels first
	metrics.IPVSResourceAdjustmentTotal.Inc(map[string]string{metrics.ResourceTypeLabel: "cpu"})
	metrics.IPVSResourceAdjustmentTotal.Inc(map[string]string{metrics.ResourceTypeLabel: "memory"})

	metrics.IPVSResourceAdjustmentTotal.Reset()

	counter := metrics.IPVSResourceAdjustmentTotal.(*opmetrics.PrometheusCounter)
	cpuVal := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "cpu"}))
	memVal := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "memory"}))

	if cpuVal != 0 {
		t.Fatalf("expected cpu counter to be 0 after reset, got %v", cpuVal)
	}
	if memVal != 0 {
		t.Fatalf("expected memory counter to be 0 after reset, got %v", memVal)
	}
}

// --- IPVSConsolidationDeferredTotal tests ---

func TestIPVSConsolidationDeferredTotal_IncrementActiveResize(t *testing.T) {
	metrics.IPVSConsolidationDeferredTotal.Reset()

	counter := metrics.IPVSConsolidationDeferredTotal.(*opmetrics.PrometheusCounter)
	before := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ReasonLabel: "active_resize"}))

	metrics.IPVSConsolidationDeferredTotal.Inc(map[string]string{metrics.ReasonLabel: "active_resize"})

	after := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ReasonLabel: "active_resize"}))
	if after-before != 1 {
		t.Fatalf("expected active_resize counter to increment by 1, got delta %v", after-before)
	}
}

func TestIPVSConsolidationDeferredTotal_IncrementGracePeriod(t *testing.T) {
	metrics.IPVSConsolidationDeferredTotal.Reset()

	counter := metrics.IPVSConsolidationDeferredTotal.(*opmetrics.PrometheusCounter)
	before := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ReasonLabel: "grace_period"}))

	metrics.IPVSConsolidationDeferredTotal.Inc(map[string]string{metrics.ReasonLabel: "grace_period"})

	after := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ReasonLabel: "grace_period"}))
	if after-before != 1 {
		t.Fatalf("expected grace_period counter to increment by 1, got delta %v", after-before)
	}
}

func TestIPVSConsolidationDeferredTotal_ResetZerosCounters(t *testing.T) {
	// Increment both labels first
	metrics.IPVSConsolidationDeferredTotal.Inc(map[string]string{metrics.ReasonLabel: "active_resize"})
	metrics.IPVSConsolidationDeferredTotal.Inc(map[string]string{metrics.ReasonLabel: "grace_period"})

	metrics.IPVSConsolidationDeferredTotal.Reset()

	counter := metrics.IPVSConsolidationDeferredTotal.(*opmetrics.PrometheusCounter)
	activeVal := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ReasonLabel: "active_resize"}))
	graceVal := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ReasonLabel: "grace_period"}))

	if activeVal != 0 {
		t.Fatalf("expected active_resize counter to be 0 after reset, got %v", activeVal)
	}
	if graceVal != 0 {
		t.Fatalf("expected grace_period counter to be 0 after reset, got %v", graceVal)
	}
}

func TestIPVSConsolidationDeferredTotal_MultipleIncrements(t *testing.T) {
	metrics.IPVSConsolidationDeferredTotal.Reset()

	counter := metrics.IPVSConsolidationDeferredTotal.(*opmetrics.PrometheusCounter)

	// Increment active_resize 3 times
	for i := 0; i < 3; i++ {
		metrics.IPVSConsolidationDeferredTotal.Inc(map[string]string{metrics.ReasonLabel: "active_resize"})
	}
	// Increment grace_period 2 times
	for i := 0; i < 2; i++ {
		metrics.IPVSConsolidationDeferredTotal.Inc(map[string]string{metrics.ReasonLabel: "grace_period"})
	}

	activeVal := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ReasonLabel: "active_resize"}))
	graceVal := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ReasonLabel: "grace_period"}))

	if activeVal != 3 {
		t.Fatalf("expected active_resize counter to be 3, got %v", activeVal)
	}
	if graceVal != 2 {
		t.Fatalf("expected grace_period counter to be 2, got %v", graceVal)
	}
}
