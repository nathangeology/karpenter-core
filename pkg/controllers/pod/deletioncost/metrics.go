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

package deletioncost

import (
	opmetrics "github.com/awslabs/operatorpkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"sigs.k8s.io/karpenter/pkg/metrics"
)

const (
	podDeletionCostSubsystem = "pod_deletion_cost"
	resultLabel              = "result"
	reasonLabel              = "reason"

	// reasonPodListFailed is the only node-scoped fault UpdatePodDeletionCosts can hit: the
	// node's pod list could not be read, so every pod on it went un-annotated.
	reasonPodListFailed = "pod_list_failed"
)

var (
	NodesRankedTotal = opmetrics.NewPrometheusCounter(
		crmetrics.Registry,
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Subsystem: podDeletionCostSubsystem,
			Name:      "nodes_ranked_total",
			Help:      "Number of nodes ranked in total by the pod deletion cost controller.",
		},
		[]string{},
	)
	NodesFailedTotal = opmetrics.NewPrometheusCounter(
		crmetrics.Registry,
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Subsystem: podDeletionCostSubsystem,
			Name:      "nodes_failed_total",
			Help:      "Number of nodes the pod deletion cost controller could not annotate in total, labeled by reason (pod_list_failed). Node-scoped: one increment stands for every pod on that node, which is why it is not folded into pods_updated_total.",
		},
		[]string{reasonLabel},
	)
	PodsUpdatedTotal = opmetrics.NewPrometheusCounter(
		crmetrics.Registry,
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Subsystem: podDeletionCostSubsystem,
			Name:      "pods_updated_total",
			Help:      "Number of pods the deletion cost controller processed in total, one increment per pod, labeled by result (success, skipped_customer_managed, skipped_third_party_conflict, skipped_pod_deleted, error).",
		},
		[]string{resultLabel},
	)
	RankingDurationSeconds = opmetrics.NewPrometheusHistogram(
		crmetrics.Registry,
		prometheus.HistogramOpts{
			Namespace: metrics.Namespace,
			Subsystem: podDeletionCostSubsystem,
			Name:      "ranking_duration_seconds",
			Help:      "Duration of node ranking computation in seconds.",
			Buckets:   metrics.DurationBuckets(),
		},
		[]string{},
	)
	AnnotationDurationSeconds = opmetrics.NewPrometheusHistogram(
		crmetrics.Registry,
		prometheus.HistogramOpts{
			Namespace: metrics.Namespace,
			Subsystem: podDeletionCostSubsystem,
			Name:      "annotation_duration_seconds",
			Help:      "Duration of pod annotation update operations in seconds.",
			Buckets:   metrics.DurationBuckets(),
		},
		[]string{},
	)
	SkippedNoChangesTotal = opmetrics.NewPrometheusCounter(
		crmetrics.Registry,
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Subsystem: podDeletionCostSubsystem,
			Name:      "skipped_no_changes_total",
			Help:      "Number of reconcile loops skipped due to no changes detected in cluster state.",
		},
		[]string{},
	)
)
