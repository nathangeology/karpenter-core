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
	opmetrics "github.com/awslabs/operatorpkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// IPVSResourceAdjustmentTotal counts the number of times a pod's effective
	// resource usage was adjusted due to IPVS-aware computation (AllocatedResources
	// or peak annotation override).
	IPVSResourceAdjustmentTotal = opmetrics.NewPrometheusCounter(
		crmetrics.Registry,
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "ipvs_resource_adjustment_total",
			Help:      "Number of times a pod's effective resource usage was adjusted due to IPVS-aware computation.",
		},
		[]string{ResourceTypeLabel},
	)

	// IPVSConsolidationDeferredTotal counts the number of times consolidation
	// was deferred due to active pod resizes or grace period.
	IPVSConsolidationDeferredTotal = opmetrics.NewPrometheusCounter(
		crmetrics.Registry,
		prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "ipvs_consolidation_deferred_total",
			Help:      "Number of times consolidation was deferred due to active pod resizes or grace period.",
		},
		[]string{ReasonLabel},
	)
)
