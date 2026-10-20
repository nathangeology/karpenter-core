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

	opmetrics "github.com/awslabs/operatorpkg/metrics"
	"github.com/samber/lo"
)

// nodePoolScopedMetric is the subset of the opmetrics metric interfaces needed to drop a
// NodePool's children. CounterMetric, GaugeMetric and ObservationMetric all satisfy it.
type nodePoolScopedMetric interface {
	DeletePartialMatch(labels map[string]string)
	Labels() []opmetrics.Label
}

// nodePoolScoped holds the metrics whose children DeleteNodePool drops. It is appended to
// from package variable initializers only, so it is fully built before main runs.
var nodePoolScoped []nodePoolScopedMetric

// NodePoolScoped records a metric whose children DeleteNodePool should drop, and returns it
// so that a declaration can wrap its constructor in place. Panics if the metric does not
// carry the NodePool dimension.
func NodePoolScoped[M nodePoolScopedMetric](metric M) M {
	if !lo.ContainsBy(metric.Labels(), func(label opmetrics.Label) bool { return label.Name == NodePoolLabel }) {
		panic(fmt.Sprintf("metric declared NodePool scoped without a %q dimension", NodePoolLabel))
	}
	nodePoolScoped = append(nodePoolScoped, metric)
	return metric
}

// DeleteNodePool drops every NodePool scoped metric's children for the named NodePool.
func DeleteNodePool(name string) {
	for _, metric := range nodePoolScoped {
		metric.DeletePartialMatch(map[string]string{NodePoolLabel: name})
	}
}
