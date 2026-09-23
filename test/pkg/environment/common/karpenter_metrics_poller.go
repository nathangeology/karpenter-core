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

package common

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/montanaflynn/stats"
	. "github.com/onsi/ginkgo/v2"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
)

// Disruption metric families read out of the scrape. Every one is already in the
// payload the poller fetches; see DisruptionReading for what each is for.
const (
	eligibleNodesMetric   = "karpenter_voluntary_disruption_eligible_nodes"
	decisionsTotalMetric  = "karpenter_voluntary_disruption_decisions_total"
	evalDurationMetric    = "karpenter_voluntary_disruption_decision_evaluation_duration_seconds"
	consolidationTimeouts = "karpenter_voluntary_disruption_consolidation_timeouts_total"
	failedValidations     = "karpenter_voluntary_disruption_failed_validations_total"
)

// DisruptionReading is the disruption controller's own account of what it did,
// read from the same scrape that yields process CPU and memory.
//
// It exists because the phase functions used to infer disruption activity by
// polling the API server for a karpenter.sh/disrupted taint on a 25 to 30 second
// schedule. A taint shorter than one poll interval was missed, so the inferred
// round count aliased the poll schedule rather than the work.
type DisruptionReading struct {
	// EligibleNodes is the sum over reasons of
	// karpenter_voluntary_disruption_eligible_nodes. The gauge is set on every
	// disrupt() call including when the candidate count is zero, and the
	// controller requeues on a 10 second polling period, so a sustained zero is
	// the controller stating it has nothing left to do.
	EligibleNodes float64
	// EligibleNodesFound distinguishes a genuine zero from a scrape that carried
	// no such family, which is what a controller that has not yet run its first
	// disruption loop looks like.
	EligibleNodesFound bool
	// Decisions is the total across every decision, reason and consolidation
	// type. Counter, so the phase takes a delta.
	Decisions float64
	// EvalSum and EvalCount are the histogram's _sum and _count, so delta of sum
	// over delta of count is the mean per-decision evaluation cost. Upstream's
	// metrics.Measure wraps the whole disrupt() call, so that span covers
	// GetCandidatesWithTotals and ComputeCommands.
	EvalSum   float64
	EvalCount float64
	// Timeouts and FailedValidations are counters. A phase that hit an internal
	// consolidation timeout is a different experiment and should be screened
	// rather than averaged; failed validations are candidates chosen and then
	// rejected.
	Timeouts          float64
	FailedValidations float64
}

type ResourceSample struct {
	Timestamp  time.Time
	MemoryMB   float64 // process resident memory in MB
	CPUCores   float64 // CPU usage rate in cores (computed from delta)
	Disruption DisruptionReading
}

type ResourceStats struct {
	P95MemoryMB float64 // 95th percentile memory usage in MB
	AvgMemoryMB float64 // average memory usage in MB
	MaxMemoryMB float64 // peak memory usage in MB
	P95CPUCores float64 // 95th percentile CPU usage in cores
	AvgCPUCores float64 // average CPU usage in cores
	MaxCPUCores float64 // peak CPU usage in cores
	SampleCount int     // number of samples collected

	// DisruptionDecisions is the decisions_total delta across the phase window:
	// the exact number of disruption decisions the controller performed. An
	// integer with no aliasing, unlike the round count it replaces.
	DisruptionDecisions float64
	// DecisionEvalMeanSeconds is delta(_sum) / delta(_count) over the window, the
	// mean per-decision evaluation cost. Zero when no decision was evaluated.
	DecisionEvalMeanSeconds float64
	// DecisionEvalCount is delta(_count), the denominator above. Reported so a
	// mean over one decision is distinguishable from a mean over fifty.
	DecisionEvalCount float64
	// ConsolidationTimeouts and FailedValidations are counter deltas over the
	// window.
	ConsolidationTimeouts float64
	FailedValidations     float64
	// CPUSaturatedFraction is the share of rate samples at or above CPUCoreLimit.
	// A workload that saturates the limit has a CPU column that cannot move, so a
	// real effect displaces into duration and the flat CPU reads as a pass. Zero
	// when CPUCoreLimit is unknown.
	CPUSaturatedFraction float64
	// CPUCoreLimit is the container's CPU limit in cores, read from the pod spec.
	// Zero when the container declares no limit.
	CPUCoreLimit float64
}

// KarpenterMetricsPoller polls the Karpenter pod's /metrics endpoint via the
// API server pod proxy for process-level CPU and memory usage. It computes
// CPU rate from the delta of process_cpu_seconds_total between samples.
type KarpenterMetricsPoller struct {
	env     *Environment
	mu      sync.Mutex
	samples []ResourceSample
	cancel  context.CancelFunc
	done    chan struct{}
	errors  int
	// cpuCoreLimit is the controller container's CPU limit in cores, read once
	// from the pod spec. The gate needs it to tell a flat CPU column that is
	// clamped from one that is genuinely unchanged.
	cpuCoreLimit float64
}

func StartKarpenterMetricsPoller(env *Environment) *KarpenterMetricsPoller {
	ctx, cancel := context.WithCancel(env.Context)
	mp := &KarpenterMetricsPoller{
		env:    env,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go mp.run(ctx)
	return mp
}

func (mp *KarpenterMetricsPoller) Stop() ResourceStats {
	mp.cancel()
	<-mp.done
	mp.mu.Lock()
	defer mp.mu.Unlock()
	stats := computeStats(mp.samples, mp.cpuCoreLimit)
	if len(mp.samples) == 0 {
		GinkgoWriter.Printf("KarpenterMetricsPoller: WARNING - stopped with 0 samples (%d errors). Ensure the Karpenter pod is running and exposing /metrics on port 8080.\n", mp.errors)
	} else {
		GinkgoWriter.Printf("KarpenterMetricsPoller: === RESULTS ===\n")
		GinkgoWriter.Printf("KarpenterMetricsPoller:   Samples: %d (errors: %d)\n", len(mp.samples), mp.errors)
		GinkgoWriter.Printf("KarpenterMetricsPoller:   Memory - P95: %.2f MB, Avg: %.2f MB, Max: %.2f MB\n",
			stats.P95MemoryMB, stats.AvgMemoryMB, stats.MaxMemoryMB)
		GinkgoWriter.Printf("KarpenterMetricsPoller:   CPU    - P95: %.4f cores, Avg: %.4f cores, Max: %.4f cores\n",
			stats.P95CPUCores, stats.AvgCPUCores, stats.MaxCPUCores)
		GinkgoWriter.Printf("KarpenterMetricsPoller:   CPU    - limit: %.2f cores, samples at or above it: %.1f%%\n",
			stats.CPUCoreLimit, stats.CPUSaturatedFraction*100)
		GinkgoWriter.Printf("KarpenterMetricsPoller:   Disruption - decisions: %.0f, evaluations: %.0f, mean evaluation: %.4f s, timeouts: %.0f, failed validations: %.0f\n",
			stats.DisruptionDecisions, stats.DecisionEvalCount, stats.DecisionEvalMeanSeconds,
			stats.ConsolidationTimeouts, stats.FailedValidations)
	}
	return stats
}

// Quiesced reports whether the controller's eligible-node gauge has read zero on
// the last `consecutive` samples, which is the controller stating it found nothing
// left to disrupt. A level, not an edge, so it cannot be missed between polls the
// way a karpenter.sh/disrupted taint could.
//
// A sample whose scrape carried no eligible-nodes family does not count as zero.
// That is the state before the controller's first disruption loop, and treating it
// as quiescence would return immediately.
func (mp *KarpenterMetricsPoller) Quiesced(consecutive int) bool {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if len(mp.samples) < consecutive {
		return false
	}
	for _, s := range mp.samples[len(mp.samples)-consecutive:] {
		if !s.Disruption.EligibleNodesFound || s.Disruption.EligibleNodes > 0 {
			return false
		}
	}
	return true
}

// Samples returns a copy of the samples collected so far.
func (mp *KarpenterMetricsPoller) Samples() []ResourceSample {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	return append([]ResourceSample(nil), mp.samples...)
}

type pollerState struct {
	podName        string
	prevCPUSeconds float64
	prevTime       time.Time
	firstSample    bool
	sampleNum      int
	disruption     DisruptionReading
}

func (mp *KarpenterMetricsPoller) run(ctx context.Context) {
	defer close(mp.done)

	state := &pollerState{firstSample: true}

	pod, err := mp.env.FindActiveKarpenterPod(ctx)
	if err != nil || pod == nil {
		mp.recordError(fmt.Errorf("finding karpenter pod: %w", err))
		return
	}
	state.podName = pod.Name
	mp.mu.Lock()
	mp.cpuCoreLimit = containerCPULimitCores(pod)
	mp.mu.Unlock()
	GinkgoWriter.Printf("KarpenterMetricsPoller: starting, scraping pod %s/%s via API server proxy (cpu limit %.2f cores)\n",
		pod.Namespace, pod.Name, containerCPULimitCores(pod))

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		mp.pollOnce(ctx, state)
		select {
		case <-ctx.Done():
			GinkgoWriter.Printf("KarpenterMetricsPoller: context canceled, collected %d samples total\n", state.sampleNum)
			return
		case <-ticker.C:
		}
	}
}

func (mp *KarpenterMetricsPoller) pollOnce(ctx context.Context, state *pollerState) {
	now := time.Now()
	memBytes, cpuSeconds, disruption, err := mp.scrapeMetrics(ctx, state.podName)
	state.disruption = disruption
	if err != nil {
		var notFound *metricsNotFoundError
		if !errors.As(err, &notFound) {
			// Pod may have been replaced (e.g. leader election change). Try to find the new one.
			if pod, findErr := mp.env.FindActiveKarpenterPod(ctx); findErr == nil && pod != nil && pod.Name != state.podName {
				GinkgoWriter.Printf("KarpenterMetricsPoller: active pod changed from %s to %s\n", state.podName, pod.Name)
				state.podName = pod.Name
				state.firstSample = true
			}
		}
		mp.recordError(err)
		return
	}

	if state.firstSample {
		mp.recordFirstSample(state, now, memBytes, cpuSeconds)
	} else {
		mp.recordSample(state, now, memBytes, cpuSeconds)
	}
}

// metricsNotFoundError indicates the HTTP response was missing expected metrics
// (pod temporarily overloaded).
type metricsNotFoundError struct {
	foundMem bool
	foundCPU bool
}

func (e *metricsNotFoundError) Error() string {
	return fmt.Sprintf("metrics not found in response (mem=%v, cpu=%v)", e.foundMem, e.foundCPU)
}

func (mp *KarpenterMetricsPoller) recordFirstSample(state *pollerState, now time.Time, memBytes, cpuSeconds float64) {
	state.prevCPUSeconds = cpuSeconds
	state.prevTime = now
	state.firstSample = false
	state.sampleNum++
	mp.mu.Lock()
	mp.samples = append(mp.samples, ResourceSample{
		Timestamp:  now,
		MemoryMB:   memBytes / (1024 * 1024),
		CPUCores:   0,
		Disruption: state.disruption,
	})
	mp.mu.Unlock()
	GinkgoWriter.Printf("KarpenterMetricsPoller: [sample %d] first sample - memory=%.2f MB, process_cpu_seconds_total=%.4f (CPU rate available after next sample)\n",
		state.sampleNum, memBytes/(1024*1024), cpuSeconds)
}

func (mp *KarpenterMetricsPoller) recordSample(state *pollerState, now time.Time, memBytes, cpuSeconds float64) {
	elapsed := now.Sub(state.prevTime).Seconds()
	cpuRate := 0.0
	if elapsed > 0 {
		cpuRate = (cpuSeconds - state.prevCPUSeconds) / elapsed
	}

	// Negative rate means counter reset (pod restart). Reset baseline and skip this sample.
	if cpuRate < 0 {
		GinkgoWriter.Printf("KarpenterMetricsPoller: CPU counter reset detected (delta=%.4f), resetting baseline\n",
			cpuSeconds-state.prevCPUSeconds)
		state.prevCPUSeconds = cpuSeconds
		state.prevTime = now
		return
	}

	state.prevCPUSeconds = cpuSeconds
	state.prevTime = now
	state.sampleNum++

	mp.mu.Lock()
	mp.samples = append(mp.samples, ResourceSample{
		Timestamp:  now,
		MemoryMB:   memBytes / (1024 * 1024),
		CPUCores:   cpuRate,
		Disruption: state.disruption,
	})
	mp.mu.Unlock()

	if state.sampleNum <= 5 || state.sampleNum%5 == 0 {
		GinkgoWriter.Printf("KarpenterMetricsPoller: [sample %d] memory=%.2f MB, cpu=%.4f cores (elapsed=%.1fs)\n",
			state.sampleNum, memBytes/(1024*1024), cpuRate, elapsed)
	}
}

// scrapeMetrics uses the API server pod proxy to fetch /metrics from the Karpenter pod.
func (mp *KarpenterMetricsPoller) scrapeMetrics(ctx context.Context, podName string) (memBytes float64, cpuSeconds float64, d DisruptionReading, err error) {
	data, err := mp.env.KubeClient.CoreV1().Pods("kube-system").ProxyGet("http", podName, "8080", "/metrics", nil).DoRaw(ctx)
	if err != nil {
		return 0, 0, d, fmt.Errorf("proxy GET /metrics: %w", err)
	}

	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(data))
	if err != nil {
		return 0, 0, d, fmt.Errorf("parsing metrics: %w", err)
	}

	memBytes = getGaugeValue(families, "process_resident_memory_bytes")
	cpuSeconds = getCounterValue(families, "process_cpu_seconds_total")

	if memBytes == 0 && cpuSeconds == 0 {
		return 0, 0, d, &metricsNotFoundError{foundMem: false, foundCPU: false}
	}

	// Every one of these is summed across label sets. The phase measures the
	// controller's whole disruption workload, so splitting by reason or
	// consolidation type would only narrow it. The labels stay available for a
	// later per-reason breakdown.
	d.EligibleNodes, d.EligibleNodesFound = sumGauge(families, eligibleNodesMetric)
	d.Decisions, _ = sumCounter(families, decisionsTotalMetric)
	d.EvalSum, d.EvalCount = sumHistogram(families, evalDurationMetric)
	d.Timeouts, _ = sumCounter(families, consolidationTimeouts)
	d.FailedValidations, _ = sumCounter(families, failedValidations)
	return memBytes, cpuSeconds, d, nil
}

func getGaugeValue(families map[string]*dto.MetricFamily, name string) float64 {
	if mf, ok := families[name]; ok && len(mf.GetMetric()) > 0 {
		return mf.GetMetric()[0].GetGauge().GetValue()
	}
	return 0
}

func getCounterValue(families map[string]*dto.MetricFamily, name string) float64 {
	if mf, ok := families[name]; ok && len(mf.GetMetric()) > 0 {
		return mf.GetMetric()[0].GetCounter().GetValue()
	}
	return 0
}

// sumGauge totals a gauge family across its label sets. The second return
// distinguishes a family that summed to zero from one that was not in the
// payload, which is what a controller that has not yet run the loop looks like.
func sumGauge(families map[string]*dto.MetricFamily, name string) (float64, bool) {
	mf, ok := families[name]
	if !ok {
		return 0, false
	}
	total := 0.0
	for _, m := range mf.GetMetric() {
		total += m.GetGauge().GetValue()
	}
	return total, true
}

func sumCounter(families map[string]*dto.MetricFamily, name string) (float64, bool) {
	mf, ok := families[name]
	if !ok {
		return 0, false
	}
	total := 0.0
	for _, m := range mf.GetMetric() {
		total += m.GetCounter().GetValue()
	}
	return total, true
}

// sumHistogram totals a histogram family's _sum and _count across label sets.
// Both are cumulative, so summing across labels and then taking a delta over the
// phase window is the same as taking per-label deltas and adding them.
func sumHistogram(families map[string]*dto.MetricFamily, name string) (sum float64, count float64) {
	mf, ok := families[name]
	if !ok {
		return 0, 0
	}
	for _, m := range mf.GetMetric() {
		h := m.GetHistogram()
		sum += h.GetSampleSum()
		count += float64(h.GetSampleCount())
	}
	return sum, count
}

func (mp *KarpenterMetricsPoller) recordError(err error) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	mp.errors++
	if mp.errors <= 5 {
		GinkgoWriter.Printf("KarpenterMetricsPoller: error %d: %v\n", mp.errors, err)
	}
}

// containerCPULimitCores returns the largest CPU limit declared by any container
// in the pod, in cores. Zero when no container declares one, which is the
// unclamped case and the one where a flat CPU column means something.
func containerCPULimitCores(pod *corev1.Pod) float64 {
	limit := 0.0
	for _, c := range pod.Spec.Containers {
		if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
			if cores := q.AsApproximateFloat64(); cores > limit {
				limit = cores
			}
		}
	}
	return limit
}

// saturationTolerance is how close to the CPU limit a rate sample has to be
// before it counts as clamped. cgroup throttling and the 5 second scrape window
// keep a saturated process a little under its limit rather than exactly on it,
// and a sample within 1% of the limit is not distinguishable from one at it.
const saturationTolerance = 0.99

func computeStats(samples []ResourceSample, cpuCoreLimit float64) ResourceStats {
	if len(samples) == 0 {
		return ResourceStats{CPUCoreLimit: cpuCoreLimit}
	}

	memValues := make(stats.Float64Data, len(samples))
	for i, s := range samples {
		memValues[i] = s.MemoryMB
	}

	// Skip the first sample for CPU (always 0 since rate needs two points)
	var cpuValues stats.Float64Data
	if len(samples) > 1 {
		cpuValues = make(stats.Float64Data, len(samples)-1)
		for i, s := range samples[1:] {
			cpuValues[i] = s.CPUCores
		}
	}

	memP95, _ := stats.Percentile(memValues, 95)
	memAvg, _ := stats.Mean(memValues)
	memMax, _ := stats.Max(memValues)

	result := ResourceStats{
		P95MemoryMB:  memP95,
		AvgMemoryMB:  memAvg,
		MaxMemoryMB:  memMax,
		SampleCount:  len(samples),
		CPUCoreLimit: cpuCoreLimit,
	}

	if len(cpuValues) > 0 {
		result.P95CPUCores, _ = stats.Percentile(cpuValues, 95)
		result.AvgCPUCores, _ = stats.Mean(cpuValues)
		result.MaxCPUCores, _ = stats.Max(cpuValues)
		if cpuCoreLimit > 0 {
			at := 0
			for _, v := range cpuValues {
				if v >= cpuCoreLimit*saturationTolerance {
					at++
				}
			}
			result.CPUSaturatedFraction = float64(at) / float64(len(cpuValues))
		}
	}

	// Counter deltas across the window. The first and last samples bracket the
	// phase, so last minus first is the phase's own contribution and nothing
	// earlier leaks in. A negative delta is a controller restart, which resets
	// every counter; report zero rather than a negative count.
	first, last := samples[0].Disruption, samples[len(samples)-1].Disruption
	result.DisruptionDecisions = nonNegativeDelta(last.Decisions, first.Decisions)
	result.ConsolidationTimeouts = nonNegativeDelta(last.Timeouts, first.Timeouts)
	result.FailedValidations = nonNegativeDelta(last.FailedValidations, first.FailedValidations)
	result.DecisionEvalCount = nonNegativeDelta(last.EvalCount, first.EvalCount)
	if sum := nonNegativeDelta(last.EvalSum, first.EvalSum); result.DecisionEvalCount > 0 {
		result.DecisionEvalMeanSeconds = sum / result.DecisionEvalCount
	}

	return result
}

func nonNegativeDelta(last, first float64) float64 {
	if d := last - first; d > 0 {
		return d
	}
	return 0
}
