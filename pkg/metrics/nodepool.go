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

package metrics

import (
	"fmt"
	"strings"
	"sync"

	opmetrics "github.com/awslabs/operatorpkg/metrics"
	"github.com/awslabs/operatorpkg/status"
	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// nodePoolScopedMetric is the subset of the opmetrics metric interfaces needed to drop a
// NodePool's children. CounterMetric, GaugeMetric and ObservationMetric all satisfy it.
type nodePoolScopedMetric interface {
	DeletePartialMatch(labels map[string]string)
	Labels() []opmetrics.Label
}

// labelKeyToDimension converts a Kubernetes label key to the metric dimension name that
// operatorpkg's status controllers derive from it, since that conversion is unexported there. A
// drift between the two surfaces as the nodePoolScopedBy panic, not as a silent no-op.
var labelKeyToDimension = strings.NewReplacer("/", "_", ".", "_", "-", "_")

// nodePoolScopedEntry pairs a metric with the dimension carrying its NodePool name. Metrics
// declared in this repository spell it NodePoolLabel; metrics whose dimensions are derived from an
// object's label keys spell it with the Prometheus-sanitized form of that key.
type nodePoolScopedEntry struct {
	metric nodePoolScopedMetric
	label  string
}

// nodePoolScoped holds the metrics whose children DeleteNodePool drops. Declarations in this
// package append to it before main runs; operator-owned metrics are appended when their controller
// is constructed, so the mutex guards a startup write against a reconciling reader.
var (
	nodePoolScopedMu sync.RWMutex
	nodePoolScoped   []nodePoolScopedEntry
)

// NodePoolScoped records a metric whose children DeleteNodePool should drop, and returns it so
// that a declaration can wrap its constructor in place. Panics if the metric does not carry the
// NodePool dimension.
func NodePoolScoped[M nodePoolScopedMetric](metric M) M {
	return nodePoolScopedBy(NodePoolLabel, metric)
}

// NodePoolScopedStatusMetrics records the status condition metrics that operatorpkg emits for a
// kind but does not reap, so that a deleted NodePool's children in them go with the rest. Its own
// cleanupNotFound drops only its three gauges and keys them on the object's namespace and name;
// the counter and the two histograms carry neither dimension, so the label keys given to
// status.WithLabels are the only object-derived dimensions they have. nodePoolLabelKey must be one
// of those keys. Returns the controller so that a registration can wrap it in place.
func NodePoolScopedStatusMetrics[T status.Object](nodePoolLabelKey string, controller *status.Controller[T]) *status.Controller[T] {
	label := labelKeyToDimension.Replace(nodePoolLabelKey)
	nodePoolScopedBy(label, controller.ConditionDuration)
	nodePoolScopedBy(label, controller.ConditionTransitionsTotal)
	nodePoolScopedBy(label, controller.TerminationDuration)
	return controller
}

// NodePoolScopedGenericStatusMetrics is NodePoolScopedStatusMetrics for a GenericObjectController,
// whose own Register is the one that must reach the manager.
func NodePoolScopedGenericStatusMetrics[T client.Object](nodePoolLabelKey string, controller *status.GenericObjectController[T]) *status.GenericObjectController[T] {
	NodePoolScopedStatusMetrics(nodePoolLabelKey, controller.Controller)
	return controller
}

func nodePoolScopedBy[M nodePoolScopedMetric](label string, metric M) M {
	if !lo.ContainsBy(metric.Labels(), func(l opmetrics.Label) bool { return l.Name == label }) {
		panic(fmt.Sprintf("metric declared NodePool scoped without a %q dimension", label))
	}
	nodePoolScopedMu.Lock()
	defer nodePoolScopedMu.Unlock()
	entry := nodePoolScopedEntry{metric: metric, label: label}
	if !lo.Contains(nodePoolScoped, entry) {
		nodePoolScoped = append(nodePoolScoped, entry)
	}
	return metric
}

// DeleteNodePool drops every NodePool scoped metric's children for the named NodePool.
func DeleteNodePool(name string) {
	nodePoolScopedMu.RLock()
	defer nodePoolScopedMu.RUnlock()
	for _, entry := range nodePoolScoped {
		entry.metric.DeletePartialMatch(map[string]string{entry.label: name})
	}
}
