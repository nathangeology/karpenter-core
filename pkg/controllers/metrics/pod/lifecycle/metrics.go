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

package lifecycle

import (
	opmetrics "github.com/awslabs/operatorpkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"sigs.k8s.io/karpenter/pkg/metrics"
)

const (
	namespaceLabel  = "namespace"
	deploymentLabel = "deployment"
)

var lifecycleLabels = []string{namespaceLabel, deploymentLabel}

func newHistogram(name, help string) opmetrics.ObservationMetric {
	return opmetrics.NewPrometheusHistogram(
		crmetrics.Registry,
		prometheus.HistogramOpts{
			Namespace: metrics.Namespace,
			Subsystem: metrics.PodSubsystem,
			Name:      name,
			Help:      help,
			Buckets:   metrics.DurationBuckets(),
		},
		lifecycleLabels,
	)
}

var (
	LifecycleSchedulingDecisionDurationSeconds = newHistogram(
		"lifecycle_scheduling_decision_duration_seconds",
		"Duration from pod creation to Karpenter scheduling decision.",
	)
	LifecycleNodeProvisioningDurationSeconds = newHistogram(
		"lifecycle_node_provisioning_duration_seconds",
		"Duration of node provisioning phase for the pod's node.",
	)
	LifecycleInstanceLaunchDurationSeconds = newHistogram(
		"lifecycle_instance_launch_duration_seconds",
		"Duration from NodeClaim creation to instance launch.",
	)
	LifecycleNodeRegistrationDurationSeconds = newHistogram(
		"lifecycle_node_registration_duration_seconds",
		"Duration from instance launch to node registration.",
	)
	LifecycleImagePullDurationSeconds = newHistogram(
		"lifecycle_image_pull_duration_seconds",
		"Duration of image pull phase estimated from node ready to init container start.",
	)
	LifecycleInitContainersDurationSeconds = newHistogram(
		"lifecycle_init_containers_duration_seconds",
		"Duration of init containers execution.",
	)
	LifecycleContainerStartDurationSeconds = newHistogram(
		"lifecycle_container_start_duration_seconds",
		"Duration from init containers complete to containers started.",
	)
	LifecycleReadinessDurationSeconds = newHistogram(
		"lifecycle_readiness_duration_seconds",
		"Duration from containers started to pod ready.",
	)
	LifecycleStartupTotalDurationSeconds = newHistogram(
		"lifecycle_startup_total_duration_seconds",
		"Total duration from pod creation to pod ready.",
	)
	LifecycleGracefulTerminationDurationSeconds = newHistogram(
		"lifecycle_graceful_termination_duration_seconds",
		"Duration of graceful termination phase.",
	)
	LifecycleContainerStopDurationSeconds = newHistogram(
		"lifecycle_container_stop_duration_seconds",
		"Duration from deletion timestamp to all containers stopped.",
	)
	LifecycleVolumeDetachDurationSeconds = newHistogram(
		"lifecycle_volume_detach_duration_seconds",
		"Duration of volume detach phase from NodeClaim conditions.",
	)
	LifecycleShutdownTotalDurationSeconds = newHistogram(
		"lifecycle_shutdown_total_duration_seconds",
		"Total duration from pod deletion to final termination.",
	)
)
