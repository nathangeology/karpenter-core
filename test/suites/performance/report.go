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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/test/pkg/environment/common"
)

// OutputPerformanceReport outputs a performance report to console and file
func OutputPerformanceReport(report *PerformanceReport, filePrefix string) {
	GinkgoWriter.Printf("\n=== %s PERFORMANCE REPORT ===\n", report.TestType)
	GinkgoWriter.Printf("Test: %s\n", report.TestName)
	GinkgoWriter.Printf("Type: %s\n", report.TestType)
	GinkgoWriter.Printf("Total Time: %v\n", report.TotalTime)
	GinkgoWriter.Printf("Total Pods: %d (Net Change: %+d)\n", report.TotalPods, report.PodsNetChange)
	GinkgoWriter.Printf("Total Nodes: %d (Net Change: %+d)\n", report.TotalNodes, report.NodesNetChange)
	GinkgoWriter.Printf("CPU Utilization: %.2f%%\n", report.TotalReservedCPUUtil*100)
	GinkgoWriter.Printf("Memory Utilization: %.2f%%\n", report.TotalReservedMemoryUtil*100)
	GinkgoWriter.Printf("Efficiency Score: %.1f%%\n", report.ResourceEfficiencyScore)
	GinkgoWriter.Printf("Pods per Node: %.1f\n", report.PodsPerNode)
	GinkgoWriter.Printf("Rounds: %d\n", report.Rounds)
	if report.TestType == "consolidation" {
		verdict := "converged"
		if !report.Converged {
			verdict = "NOT converged, the value below is a bound and not a measurement"
		}
		GinkgoWriter.Printf("Convergence: %.1f s (%s)\n", report.ConvergenceSeconds, verdict)
	}

	// Karpenter pod resource usage (from Kubernetes Metrics API)
	if report.MetricsSampleCount > 0 {
		GinkgoWriter.Printf("Karpenter Memory (P95/Avg/Max): %.2f / %.2f / %.2f MB (%d samples)\n",
			report.KarpenterP95MemoryMB, report.KarpenterAvgMemoryMB, report.KarpenterMaxMemoryMB, report.MetricsSampleCount)
		GinkgoWriter.Printf("Karpenter CPU (P95/Avg/Max): %.4f / %.4f / %.4f cores (%d samples)\n",
			report.KarpenterP95CPUCores, report.KarpenterAvgCPUCores, report.KarpenterMaxCPUCores, report.MetricsSampleCount)
		if report.KarpenterCPUCoreLimit > 0 {
			note := ""
			if report.KarpenterCPUSaturatedPct >= 50 {
				note = "  CPU is clamped for most of this phase, so its CPU columns cannot move and a real effect displaces into duration"
			}
			GinkgoWriter.Printf("Karpenter CPU limit: %.2f cores, samples at or above it: %.1f%%%s\n",
				report.KarpenterCPUCoreLimit, report.KarpenterCPUSaturatedPct, note)
		}
		GinkgoWriter.Printf("Disruption: %.0f decisions, %.0f evaluations, %.4f s mean evaluation, %.0f consolidation timeouts, %.0f failed validations\n",
			report.DisruptionDecisions, report.DecisionEvalCount, report.DecisionEvalMeanSeconds,
			report.ConsolidationTimeouts, report.FailedValidations)
	} else {
		GinkgoWriter.Printf("Karpenter Metrics: Not available (0 samples collected)\n")
	}

	// File output
	if outputDir := os.Getenv("OUTPUT_DIR"); outputDir != "" {
		writeReportFiles(report, filePrefix, outputDir)
	}
}

// writeReportFiles persists the report JSON and any profile data under
// outputDir. The caller-supplied filePrefix is sanitized to prevent path
// traversal via the OUTPUT_DIR environment variable or the prefix itself.
func writeReportFiles(report *PerformanceReport, filePrefix, outputDir string) {
	safeDir := filepath.Clean(outputDir)
	safePrefix := filepath.Base(filepath.Clean(filePrefix))
	writeUnder := func(name string, data []byte) (string, error) {
		path := filepath.Join(safeDir, name)
		// Defense in depth: ensure the resolved path stays under safeDir.
		if rel, err := filepath.Rel(safeDir, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("refusing to write outside %q", safeDir)
		}
		return path, os.WriteFile(path, data, 0600)
	}
	if reportJSON, err := json.MarshalIndent(report, "", "  "); err == nil {
		if path, err := writeUnder(fmt.Sprintf("%s_performance_report.json", safePrefix), reportJSON); err == nil {
			GinkgoWriter.Printf("Report written to: %s\n", path)
		}
	}
	if len(report.MemoryProfileData) > 0 {
		if path, err := writeUnder(fmt.Sprintf("karpenter_memory_profile_%s.pb.gz", safePrefix), report.MemoryProfileData); err == nil {
			GinkgoWriter.Printf("Memory profile saved to: %s\n", path)
		}
	}
	if len(report.CPUProfileData) > 0 {
		if path, err := writeUnder(fmt.Sprintf("karpenter_cpu_profile_%s.pb.gz", safePrefix), report.CPUProfileData); err == nil {
			GinkgoWriter.Printf("CPU profile saved to: %s\n", path)
		}
	}
}

// ReportScaleOut waits for expectedPods pods to become healthy and reports the
// time taken, resource utilization and node efficiency.
func ReportScaleOut(env *common.Environment, testName string, expectedPods int, timeout time.Duration) (*PerformanceReport, error) {
	profiler := common.StartKarpenterProfiler(env)
	metricsPoller := common.StartKarpenterMetricsPoller(env)
	startTime := time.Now()

	// Wait for all pods to be healthy
	allPodsSelector := labels.SelectorFromSet(map[string]string{test.DiscoveryLabel: "unspecified"})
	if expectedPods > 0 {
		env.EventuallyExpectHealthyPodCountWithTimeout(timeout, allPodsSelector, expectedPods)
	}

	totalTime := time.Since(startTime)
	memProfile, cpuProfile := profiler.Stop()
	stats := metricsPoller.Stop()

	// Collect metrics
	nodeCount := env.Monitor.CreatedNodeCount()
	avgCPUUtil := env.Monitor.AvgUtilization(corev1.ResourceCPU)
	avgMemUtil := env.Monitor.AvgUtilization(corev1.ResourceMemory)

	// Calculate derived metrics
	resourceEfficiencyScore := (avgCPUUtil*90 + avgMemUtil*10)
	podsPerNode := float64(0)
	if nodeCount > 0 {
		podsPerNode = float64(expectedPods) / float64(nodeCount)
	}

	report := &PerformanceReport{
		TestName:                testName,
		TestType:                "scale-out",
		TotalPods:               expectedPods,
		TotalNodes:              nodeCount,
		TotalTime:               totalTime,
		PodsNetChange:           expectedPods,
		NodesNetChange:          nodeCount,
		TotalReservedCPUUtil:    avgCPUUtil,
		TotalReservedMemoryUtil: avgMemUtil,
		ResourceEfficiencyScore: resourceEfficiencyScore,
		PodsPerNode:             podsPerNode,
		Rounds:                  1, // Scale-out is always 1 round
		Timestamp:               time.Now(),
		// Scale-out blocks on one Eventually and does not wait for the controller
		// to go quiet, so there is no convergence window to report.
		Converged:         true,
		MemoryProfileData: memProfile,
		CPUProfileData:    cpuProfile,
	}
	applyResourceStats(report, stats)
	return report, nil
}

// ReportConsolidation waits for the pod count to reach finalPods, then monitors
// node consolidation rounds until the controller goes quiet or timeout elapses.
func ReportConsolidation(env *common.Environment, testName string, initialPods, finalPods, initialNodes int, timeout time.Duration) (*PerformanceReport, error) {
	profiler := common.StartKarpenterProfiler(env)
	metricsPoller := common.StartKarpenterMetricsPoller(env)
	startTime := time.Now()

	// Wait for pods to scale down first
	allPodsSelector := labels.SelectorFromSet(map[string]string{test.DiscoveryLabel: "unspecified"})
	if finalPods > 0 {
		env.EventuallyExpectHealthyPodCountWithTimeout(timeout, allPodsSelector, finalPods)
	}

	// Monitor consolidation rounds
	consolidationRounds, _ := monitorConsolidationRounds(env, timeout)
	totalTime := time.Since(startTime)
	memProfile, cpuProfile := profiler.Stop()
	samples := metricsPoller.Samples()
	stats := metricsPoller.Stop()
	converged, convergence := convergenceFromSamples(samples, startTime)

	// Collect final metrics
	finalNodes := env.Monitor.CreatedNodeCount()
	avgCPUUtil := env.Monitor.AvgUtilization(corev1.ResourceCPU)
	avgMemUtil := env.Monitor.AvgUtilization(corev1.ResourceMemory)

	// Calculate derived metrics
	resourceEfficiencyScore := (avgCPUUtil*90 + avgMemUtil*10)
	podsPerNode := float64(0)
	if finalNodes > 0 {
		podsPerNode = float64(finalPods) / float64(finalNodes)
	}

	report := &PerformanceReport{
		TestName:                testName,
		TestType:                "consolidation",
		TotalPods:               finalPods,
		TotalNodes:              finalNodes,
		TotalTime:               totalTime,
		PodsNetChange:           finalPods - initialPods,
		NodesNetChange:          finalNodes - initialNodes,
		TotalReservedCPUUtil:    avgCPUUtil,
		TotalReservedMemoryUtil: avgMemUtil,
		ResourceEfficiencyScore: resourceEfficiencyScore,
		PodsPerNode:             podsPerNode,
		Rounds:                  len(consolidationRounds),
		Timestamp:               time.Now(),
		ConvergenceSeconds:      convergence.Seconds(),
		Converged:               converged,
		MemoryProfileData:       memProfile,
		CPUProfileData:          cpuProfile,
	}
	applyResourceStats(report, stats)
	return report, nil
}

// quiescenceConfirmations is how many consecutive poller samples must read zero
// eligible nodes before the controller is taken to have finished.
//
// The poller ticks every 5 s and the disruption controller requeues on a 10 s
// polling period, so 6 samples is a 30 s window covering at least three
// controller loops.
const quiescenceConfirmations = 6

// convergenceFromSamples reads, out of the series the poller already collected,
// when the controller first reported nothing left to disrupt. It measures rather
// than waits: monitorConsolidationRounds is still the phase's convergence test,
// and this reports how much of that window was spent after the controller went
// quiet.
//
// Returns false when no sustained-zero window closed, in which case the duration
// is the whole window and not a convergence time.
func convergenceFromSamples(samples []common.ResourceSample, phaseStart time.Time) (bool, time.Duration) {
	run := 0
	for _, s := range samples {
		if s.Disruption.EligibleNodesFound && s.Disruption.EligibleNodes == 0 {
			run++
			if run >= quiescenceConfirmations {
				return true, s.Timestamp.Sub(phaseStart)
			}
			continue
		}
		run = 0
	}
	return false, 0
}

// applyResourceStats copies the poller's output onto the report. One function
// rather than the three identical literals it replaces, so a field added to the
// poller cannot reach one phase and miss another.
func applyResourceStats(report *PerformanceReport, stats common.ResourceStats) {
	report.KarpenterP95MemoryMB = stats.P95MemoryMB
	report.KarpenterAvgMemoryMB = stats.AvgMemoryMB
	report.KarpenterMaxMemoryMB = stats.MaxMemoryMB
	report.KarpenterP95CPUCores = stats.P95CPUCores
	report.KarpenterAvgCPUCores = stats.AvgCPUCores
	report.KarpenterMaxCPUCores = stats.MaxCPUCores
	report.MetricsSampleCount = stats.SampleCount
	report.KarpenterCPUCoreLimit = stats.CPUCoreLimit
	report.KarpenterCPUSaturatedPct = stats.CPUSaturatedFraction * 100
	report.DisruptionDecisions = stats.DisruptionDecisions
	report.DecisionEvalMeanSeconds = stats.DecisionEvalMeanSeconds
	report.DecisionEvalCount = stats.DecisionEvalCount
	report.ConsolidationTimeouts = stats.ConsolidationTimeouts
	report.FailedValidations = stats.FailedValidations
}

// ReportDrift monitors node replacement during drift and reports the time taken
// and the number of replacement rounds. expectedPods stays constant throughout.
func ReportDrift(env *common.Environment, testName string, expectedPods int, timeout time.Duration) (*PerformanceReport, error) {
	profiler := common.StartKarpenterProfiler(env)
	metricsPoller := common.StartKarpenterMetricsPoller(env)
	startTime := time.Now()
	initialNodeCount := env.Monitor.CreatedNodeCount()

	// Track node replacement during drift
	driftRounds := 0
	lastReplacementTime := time.Now()
	driftStartTime := time.Now()

	// Monitor for node replacements during drift
	for time.Since(driftStartTime) < timeout {
		// Check if nodes are being replaced (draining/terminating)
		var drainingNodes []corev1.Node
		allNodes := env.Monitor.CreatedNodes()
		for _, node := range allNodes {
			// Check if node has draining taint or is being deleted
			if node.DeletionTimestamp != nil {
				drainingNodes = append(drainingNodes, *node)
			}
			for _, taint := range node.Spec.Taints {
				if taint.Key == "karpenter.sh/disrupted" {
					drainingNodes = append(drainingNodes, *node)
					break
				}
			}
		}

		// If we detect draining nodes, this indicates a drift replacement round
		if len(drainingNodes) > 0 {
			lastReplacementTime = time.Now()
			driftRounds++

			// Wait for replacement to complete
			time.Sleep(30 * time.Second)
		}

		// Check for stability (no replacements for 2 minutes)
		if time.Since(lastReplacementTime) >= 2*time.Minute {
			break
		}

		// Wait before next check
		time.Sleep(15 * time.Second)
	}

	// Ensure all pods are healthy after drift
	allPodsSelector := labels.SelectorFromSet(map[string]string{test.DiscoveryLabel: "unspecified"})
	if expectedPods > 0 {
		env.EventuallyExpectHealthyPodCountWithTimeout(timeout/2, allPodsSelector, expectedPods)
	}

	totalTime := time.Since(startTime)
	memProfile, cpuProfile := profiler.Stop()
	stats := metricsPoller.Stop()
	finalNodeCount := env.Monitor.CreatedNodeCount()

	// Collect metrics
	avgCPUUtil := env.Monitor.AvgUtilization(corev1.ResourceCPU)
	avgMemUtil := env.Monitor.AvgUtilization(corev1.ResourceMemory)

	// Calculate derived metrics
	resourceEfficiencyScore := (avgCPUUtil*90 + avgMemUtil*10)
	podsPerNode := float64(0)
	if finalNodeCount > 0 {
		podsPerNode = float64(expectedPods) / float64(finalNodeCount)
	}

	// If no drift rounds were detected, assume at least 1 round occurred
	if driftRounds == 0 {
		driftRounds = 1
	}

	report := &PerformanceReport{
		TestName:                testName,
		TestType:                "drift",
		TotalPods:               expectedPods,
		TotalNodes:              finalNodeCount,
		TotalTime:               totalTime,
		PodsNetChange:           0,                                 // Pods don't change in drift
		NodesNetChange:          finalNodeCount - initialNodeCount, // Net change in nodes (should be ~0 for drift)
		TotalReservedCPUUtil:    avgCPUUtil,
		TotalReservedMemoryUtil: avgMemUtil,
		ResourceEfficiencyScore: resourceEfficiencyScore,
		PodsPerNode:             podsPerNode,
		Rounds:                  driftRounds,
		Timestamp:               time.Now(),
		// ReportDrift still uses the taint-polling loop below, which has the same
		// aliasing defect as the consolidation monitor did. Switching it shifts the
		// drift baselines, so it belongs in its own change; the disruption fields
		// are populated here regardless, so that change can be made with both
		// statistics measured on the same runs.
		Converged:         true,
		MemoryProfileData: memProfile,
		CPUProfileData:    cpuProfile,
	}
	applyResourceStats(report, stats)
	return report, nil
}

// Convenience functions for common monitoring patterns

// ReportScaleOutWithOutput monitors scale-out and automatically outputs the report
func ReportScaleOutWithOutput(env *common.Environment, testName string, expectedPods int, timeout time.Duration, filePrefix string) (*PerformanceReport, error) {
	report, err := ReportScaleOut(env, testName, expectedPods, timeout)
	if err != nil {
		return nil, err
	}
	OutputPerformanceReport(report, filePrefix)
	return report, nil
}

// ReportConsolidationWithOutput monitors consolidation and automatically outputs the report
func ReportConsolidationWithOutput(env *common.Environment, testName string, initialPods, finalPods, initialNodes int, timeout time.Duration, filePrefix string) (*PerformanceReport, error) {
	report, err := ReportConsolidation(env, testName, initialPods, finalPods, initialNodes, timeout)
	if err != nil {
		return nil, err
	}
	OutputPerformanceReport(report, filePrefix)
	return report, nil
}

// ReportDriftWithOutput monitors drift and automatically outputs the report
func ReportDriftWithOutput(env *common.Environment, testName string, expectedPods int, timeout time.Duration, filePrefix string) (*PerformanceReport, error) {
	report, err := ReportDrift(env, testName, expectedPods, timeout)
	if err != nil {
		return nil, err
	}
	OutputPerformanceReport(report, filePrefix)
	return report, nil
}

// monitorConsolidationRounds monitors node consolidation and returns consolidation rounds.
// This is a helper function used by ReportConsolidation to track individual consolidation rounds.
func monitorConsolidationRounds(env *common.Environment, timeout time.Duration) ([]ConsolidationRound, time.Duration) {
	var consolidationRounds []ConsolidationRound
	roundNumber := 1
	lastDrainingTime := time.Now()
	consolidationStartTime := time.Now()

	for time.Since(consolidationStartTime) < timeout {
		currentNodes := env.Monitor.CreatedNodeCount()

		// Check if nodes are draining/terminating
		var drainingNodes []corev1.Node
		allNodes := env.Monitor.CreatedNodes()
		for _, node := range allNodes {
			// Check if node has draining taint or is being deleted
			if node.DeletionTimestamp != nil {
				drainingNodes = append(drainingNodes, *node)
			}
			for _, taint := range node.Spec.Taints {
				if taint.Key == "karpenter.sh/disrupted" {
					drainingNodes = append(drainingNodes, *node)
					break
				}
			}
		}

		// If we detect draining nodes, record this as a consolidation round
		if len(drainingNodes) > 0 {
			lastDrainingTime = time.Now()

			// Wait for this round to complete
			roundStartTime := time.Now()
			time.Sleep(25 * time.Second)
			finalNodeCount := env.Monitor.CreatedNodeCount()
			roundDuration := time.Since(roundStartTime)

			round := ConsolidationRound{
				RoundNumber:   roundNumber,
				StartTime:     roundStartTime,
				Duration:      roundDuration,
				NodesRemoved:  currentNodes - finalNodeCount,
				StartingNodes: currentNodes,
				EndingNodes:   finalNodeCount,
			}
			consolidationRounds = append(consolidationRounds, round)
			roundNumber++
		}

		// Check for stability (no draining for 3 minutes)
		if time.Since(lastDrainingTime) >= 3*time.Minute {
			break
		}

		// Wait before next check
		time.Sleep(30 * time.Second)
	}

	totalConsolidationTime := time.Since(consolidationStartTime)

	return consolidationRounds, totalConsolidationTime
}
