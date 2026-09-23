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

package performance

import (
	"time"
)

// ConsolidationRound represents a single round of consolidation
type ConsolidationRound struct {
	RoundNumber   int           `json:"round_number"`
	StartTime     time.Time     `json:"start_time"`
	Duration      time.Duration `json:"duration"`
	NodesRemoved  int           `json:"nodes_removed"`
	StartingNodes int           `json:"starting_nodes"`
	EndingNodes   int           `json:"ending_nodes"`
}

// PerformanceReport represents the structured performance test results
type PerformanceReport struct {
	TestName                string        `json:"test_name"`
	TestType                string        `json:"test_type"`
	TotalPods               int           `json:"total_pods"`
	TotalNodes              int           `json:"total_nodes"`
	TotalTime               time.Duration `json:"total_time"`
	PodsNetChange           int           `json:"change_in_pod_count"`
	NodesNetChange          int           `json:"change_in_node_count"`
	TotalReservedCPUUtil    float64       `json:"total_reserved_cpu_utilization"`
	TotalReservedMemoryUtil float64       `json:"total_reserved_memory_utilization"`
	ResourceEfficiencyScore float64       `json:"resource_efficiency_score"`
	PodsPerNode             float64       `json:"pods_per_node"`
	Rounds                  int           `json:"rounds"`
	Timestamp               time.Time     `json:"timestamp"`

	// Karpenter pod resource usage from Kubernetes Metrics API (container-level)
	KarpenterP95MemoryMB float64 `json:"karpenter_p95_memory_mb"`
	KarpenterAvgMemoryMB float64 `json:"karpenter_avg_memory_mb"`
	KarpenterMaxMemoryMB float64 `json:"karpenter_max_memory_mb"`
	KarpenterP95CPUCores float64 `json:"karpenter_p95_cpu_cores"`
	KarpenterAvgCPUCores float64 `json:"karpenter_avg_cpu_cores"`
	KarpenterMaxCPUCores float64 `json:"karpenter_max_cpu_cores"`
	MetricsSampleCount   int     `json:"metrics_sample_count"`

	// KarpenterCPUCoreLimit is the container's CPU limit in cores, and
	// KarpenterCPUSaturatedPct the share of rate samples at or above 99% of it. A
	// workload that saturates the limit has a CPU column that cannot move, so a
	// real effect displaces into duration and the flat CPU reads as a pass.
	KarpenterCPUCoreLimit    float64 `json:"karpenter_cpu_core_limit"`
	KarpenterCPUSaturatedPct float64 `json:"karpenter_cpu_saturated_pct"`

	// Disruption-controller metrics, read from the same scrape as CPU and memory.
	// They replace Rounds, which counts poll iterations that caught a
	// karpenter.sh/disrupted taint and so aliases the poll schedule rather than the
	// controller's work. Rounds stays in the informational tier so the two can be
	// compared on the same run.
	DisruptionDecisions float64 `json:"disruption_decisions"`
	// Mean per decision, not per second of wall clock. Upstream's metrics.Measure
	// wraps the whole disrupt() call, so the span covers GetCandidatesWithTotals
	// and ComputeCommands.
	DecisionEvalMeanSeconds float64 `json:"decision_eval_mean_seconds"`
	DecisionEvalCount       float64 `json:"decision_eval_count"`
	// Non-zero when the multi-node or single-node search hit its own internal
	// timeout, which makes the phase a different experiment.
	ConsolidationTimeouts float64 `json:"consolidation_timeouts"`
	FailedValidations     float64 `json:"failed_validations"`

	// ConvergenceSeconds is how long the phase waited from its pod-count target
	// being met to the controller reporting no eligible nodes for the confirmation
	// window. It is the part of TotalTime that is a measurement rather than a
	// schedule, and it is reported separately so the two are not conflated.
	ConvergenceSeconds float64 `json:"convergence_seconds"`
	// Converged is false when the confirmation window never closed, which means
	// the phase ended on its timeout and ConvergenceSeconds is a censoring bound
	// rather than a measurement.
	Converged bool `json:"converged"`

	// pprof debug artifacts (not used for assertions, saved for offline analysis)
	MemoryProfileData []byte `json:"-"`
	CPUProfileData    []byte `json:"-"`
}
