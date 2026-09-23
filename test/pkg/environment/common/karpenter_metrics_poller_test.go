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
	"math"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Plain Go tests, TestUnit prefix, so `make test-helpers` can run them without a
// cluster. Nothing here talks to an API server.

func sample(mem, cpu float64, d DisruptionReading) ResourceSample {
	return ResourceSample{Timestamp: time.Now(), MemoryMB: mem, CPUCores: cpu, Disruption: d}
}

func TestUnitComputeStatsCounterDeltas(t *testing.T) {
	// First and last samples bracket the phase, so the phase's own contribution is
	// last minus first. Anything the controller did before the phase started is
	// already in the first reading and must not leak in.
	got := computeStats([]ResourceSample{
		sample(100, 0, DisruptionReading{Decisions: 40, EvalSum: 10, EvalCount: 40, Timeouts: 2, FailedValidations: 1}),
		sample(110, 0.5, DisruptionReading{Decisions: 45, EvalSum: 12, EvalCount: 45, Timeouts: 2, FailedValidations: 1}),
		sample(120, 0.5, DisruptionReading{Decisions: 52, EvalSum: 16, EvalCount: 52, Timeouts: 3, FailedValidations: 4}),
	}, 0)

	if got.DisruptionDecisions != 12 {
		t.Errorf("DisruptionDecisions = %v, want 12 (52-40)", got.DisruptionDecisions)
	}
	if got.DecisionEvalCount != 12 {
		t.Errorf("DecisionEvalCount = %v, want 12", got.DecisionEvalCount)
	}
	// (16-10)/(52-40) = 0.5 s per decision.
	if math.Abs(got.DecisionEvalMeanSeconds-0.5) > 1e-9 {
		t.Errorf("DecisionEvalMeanSeconds = %v, want 0.5", got.DecisionEvalMeanSeconds)
	}
	if got.ConsolidationTimeouts != 1 {
		t.Errorf("ConsolidationTimeouts = %v, want 1", got.ConsolidationTimeouts)
	}
	if got.FailedValidations != 3 {
		t.Errorf("FailedValidations = %v, want 3", got.FailedValidations)
	}
}

func TestUnitComputeStatsCounterRestart(t *testing.T) {
	// A controller restart resets every counter. A negative delta is that, not
	// negative work, so it reports zero rather than a negative count.
	got := computeStats([]ResourceSample{
		sample(100, 0, DisruptionReading{Decisions: 900, EvalSum: 500, EvalCount: 900}),
		sample(100, 0.1, DisruptionReading{Decisions: 3, EvalSum: 1, EvalCount: 3}),
	}, 0)
	if got.DisruptionDecisions != 0 {
		t.Errorf("DisruptionDecisions = %v, want 0 after a counter reset", got.DisruptionDecisions)
	}
	if got.DecisionEvalMeanSeconds != 0 {
		t.Errorf("DecisionEvalMeanSeconds = %v, want 0 when the count delta is not positive", got.DecisionEvalMeanSeconds)
	}
}

func TestUnitComputeStatsNoDecisions(t *testing.T) {
	// A phase where the controller evaluated nothing must not divide by zero.
	got := computeStats([]ResourceSample{
		sample(100, 0, DisruptionReading{Decisions: 7, EvalSum: 3, EvalCount: 7}),
		sample(100, 0.2, DisruptionReading{Decisions: 7, EvalSum: 3, EvalCount: 7}),
	}, 0)
	if got.DecisionEvalMeanSeconds != 0 {
		t.Errorf("DecisionEvalMeanSeconds = %v, want 0", got.DecisionEvalMeanSeconds)
	}
	if got.DecisionEvalCount != 0 {
		t.Errorf("DecisionEvalCount = %v, want 0", got.DecisionEvalCount)
	}
}

func TestUnitComputeStatsCPUSaturation(t *testing.T) {
	// The first sample's CPU is always 0 because a rate needs two points, so it is
	// excluded from the CPU series and from the saturation denominator.
	samples := []ResourceSample{
		sample(100, 0, DisruptionReading{}),
		sample(100, 0.99, DisruptionReading{}),
		sample(100, 1.00, DisruptionReading{}),
		sample(100, 1.02, DisruptionReading{}),
		sample(100, 0.40, DisruptionReading{}),
	}
	got := computeStats(samples, 1.0)
	if got.CPUCoreLimit != 1.0 {
		t.Errorf("CPUCoreLimit = %v, want 1.0", got.CPUCoreLimit)
	}
	// 3 of the 4 rate samples are at or above 0.99 cores.
	if math.Abs(got.CPUSaturatedFraction-0.75) > 1e-9 {
		t.Errorf("CPUSaturatedFraction = %v, want 0.75", got.CPUSaturatedFraction)
	}

	// With no declared limit the fraction is not computed, because there is no
	// ceiling to compare against.
	if unclamped := computeStats(samples, 0); unclamped.CPUSaturatedFraction != 0 {
		t.Errorf("CPUSaturatedFraction = %v with no limit, want 0", unclamped.CPUSaturatedFraction)
	}
}

func TestUnitComputeStatsEmpty(t *testing.T) {
	got := computeStats(nil, 2.0)
	if got.SampleCount != 0 {
		t.Errorf("SampleCount = %d, want 0", got.SampleCount)
	}
	// The limit is a property of the pod, not of the samples, so it survives an
	// empty batch. A gate reading a zero limit would treat a clamped key as
	// unclamped.
	if got.CPUCoreLimit != 2.0 {
		t.Errorf("CPUCoreLimit = %v, want 2.0 even with no samples", got.CPUCoreLimit)
	}
}

func TestUnitQuiesced(t *testing.T) {
	zero := DisruptionReading{EligibleNodes: 0, EligibleNodesFound: true}
	busy := DisruptionReading{EligibleNodes: 3, EligibleNodesFound: true}
	absent := DisruptionReading{EligibleNodes: 0, EligibleNodesFound: false}

	for _, tc := range []struct {
		name    string
		samples []DisruptionReading
		want    bool
	}{
		{"fewer samples than the window", []DisruptionReading{zero, zero}, false},
		{"the window is all zero", []DisruptionReading{busy, zero, zero, zero}, true},
		{"a busy sample inside the window", []DisruptionReading{zero, zero, busy, zero}, false},
		{"the last sample is busy", []DisruptionReading{zero, zero, zero, busy}, false},
		// Before the controller's first disruption loop the family is not in the
		// payload at all. Reading that as quiescence would return immediately, so
		// an absent family is not a zero.
		{"the family was absent", []DisruptionReading{absent, absent, absent, absent}, false},
		{"absent then zero, not yet a full window", []DisruptionReading{zero, zero, absent, zero}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mp := &KarpenterMetricsPoller{}
			for _, d := range tc.samples {
				mp.samples = append(mp.samples, sample(100, 0.1, d))
			}
			if got := mp.Quiesced(3); got != tc.want {
				t.Errorf("Quiesced(3) = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnitContainerCPULimitCores(t *testing.T) {
	pod := func(limits ...string) *corev1.Pod {
		p := &corev1.Pod{}
		for _, l := range limits {
			c := corev1.Container{}
			if l != "" {
				c.Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(l)}
			}
			p.Spec.Containers = append(p.Spec.Containers, c)
		}
		return p
	}
	for _, tc := range []struct {
		name string
		pod  *corev1.Pod
		want float64
	}{
		{"one container with a limit", pod("1"), 1},
		{"millicores", pod("1500m"), 1.5},
		{"no limit declared", pod(""), 0},
		{"the largest of several", pod("500m", "2", "1"), 2},
		{"a sidecar with no limit does not zero it", pod("1", ""), 1},
		{"no containers", pod(), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := containerCPULimitCores(tc.pod); math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("containerCPULimitCores = %v, want %v", got, tc.want)
			}
		})
	}
}

// The three disruption families are read out of a real exposition payload rather
// than a hand-built map, so a name or type change upstream shows up here.
const exposition = `
# HELP karpenter_voluntary_disruption_eligible_nodes Number of nodes eligible for disruption by Karpenter.
# TYPE karpenter_voluntary_disruption_eligible_nodes gauge
karpenter_voluntary_disruption_eligible_nodes{reason="underutilized"} 0
karpenter_voluntary_disruption_eligible_nodes{reason="empty"} 4
# HELP karpenter_voluntary_disruption_decisions_total Number of disruption decisions performed.
# TYPE karpenter_voluntary_disruption_decisions_total counter
karpenter_voluntary_disruption_decisions_total{consolidation_type="multi",decision="delete",reason="underutilized"} 11
karpenter_voluntary_disruption_decisions_total{consolidation_type="single",decision="replace",reason="underutilized"} 5
# HELP karpenter_voluntary_disruption_decision_evaluation_duration_seconds Duration of the disruption decision evaluation process in seconds.
# TYPE karpenter_voluntary_disruption_decision_evaluation_duration_seconds histogram
karpenter_voluntary_disruption_decision_evaluation_duration_seconds_bucket{consolidation_type="multi",reason="underutilized",le="1"} 6
karpenter_voluntary_disruption_decision_evaluation_duration_seconds_bucket{consolidation_type="multi",reason="underutilized",le="+Inf"} 8
karpenter_voluntary_disruption_decision_evaluation_duration_seconds_sum{consolidation_type="multi",reason="underutilized"} 12.5
karpenter_voluntary_disruption_decision_evaluation_duration_seconds_count{consolidation_type="multi",reason="underutilized"} 8
karpenter_voluntary_disruption_decision_evaluation_duration_seconds_bucket{consolidation_type="single",reason="underutilized",le="1"} 2
karpenter_voluntary_disruption_decision_evaluation_duration_seconds_bucket{consolidation_type="single",reason="underutilized",le="+Inf"} 2
karpenter_voluntary_disruption_decision_evaluation_duration_seconds_sum{consolidation_type="single",reason="underutilized"} 0.5
karpenter_voluntary_disruption_decision_evaluation_duration_seconds_count{consolidation_type="single",reason="underutilized"} 2
# HELP karpenter_voluntary_disruption_consolidation_timeouts_total Number of times the Consolidation algorithm has reached a timeout.
# TYPE karpenter_voluntary_disruption_consolidation_timeouts_total counter
karpenter_voluntary_disruption_consolidation_timeouts_total{consolidation_type="multi"} 3
# HELP process_resident_memory_bytes Resident memory size in bytes.
# TYPE process_resident_memory_bytes gauge
process_resident_memory_bytes 2.62144e+08
`

func fixtureFamilies(t *testing.T) map[string]*dto.MetricFamily {
	t.Helper()
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader([]byte(exposition)))
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	return families
}

func TestUnitSumGauge(t *testing.T) {
	families := fixtureFamilies(t)
	// Summed across reasons: 0 + 4.
	eligible, found := sumGauge(families, eligibleNodesMetric)
	if !found || eligible != 4 {
		t.Errorf("sumGauge(eligible) = %v, %v; want 4, true", eligible, found)
	}
	// A zero total is still a reading. Only an absent family is not, because the
	// two mean different things to the convergence test.
	if _, found := sumGauge(families, "karpenter_nonexistent_gauge"); found {
		t.Error("sumGauge reported an absent family as found")
	}
}

func TestUnitSumCounter(t *testing.T) {
	families := fixtureFamilies(t)
	// Summed across decision and consolidation_type: 11 + 5.
	if d, found := sumCounter(families, decisionsTotalMetric); !found || d != 16 {
		t.Errorf("sumCounter(decisions) = %v, %v; want 16, true", d, found)
	}
	if to, _ := sumCounter(families, consolidationTimeouts); to != 3 {
		t.Errorf("sumCounter(timeouts) = %v, want 3", to)
	}
	// A family the payload does not carry reads as zero rather than erroring: the
	// chart may be running a Karpenter build that predates the metric.
	if fv, found := sumCounter(families, failedValidations); found || fv != 0 {
		t.Errorf("sumCounter(failed validations) = %v, %v; want 0, false", fv, found)
	}
}

func TestUnitSumHistogram(t *testing.T) {
	families := fixtureFamilies(t)
	// Summed across label sets: sums 12.5 + 0.5, counts 8 + 2.
	sum, count := sumHistogram(families, evalDurationMetric)
	if math.Abs(sum-13.0) > 1e-9 || count != 10 {
		t.Errorf("sumHistogram = %v, %v; want 13, 10", sum, count)
	}
	if s, c := sumHistogram(families, "karpenter_nonexistent_histogram"); s != 0 || c != 0 {
		t.Errorf("sumHistogram on an absent family = %v, %v; want 0, 0", s, c)
	}
}
