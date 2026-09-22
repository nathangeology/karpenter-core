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
	"fmt"
	"strconv"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// A Gomega failure aborts the whole It(), and every test in this suite is one
// It() with two or three measured phases whose stimulus is an edit to the object
// graph the previous phase left. So a breached threshold in an early phase used
// to discard the later phases' measurements too.

// PhaseGates collects threshold breaches across the phases of one It() and fails
// once, at the end, with all of them.
//
// Deferral costs no re-measurement because OutputPerformanceReport runs inside
// Report*WithOutput ahead of the caller's assertions: the JSON for a phase whose
// thresholds breached is already on disk before the breach is recorded.
type PhaseGates struct {
	// failf ends the spec. Injected so the accumulation and the message can be
	// tested without a running Ginkgo spec.
	failf   func(string)
	entries []string
	// phases that recorded at least one breach, in the order they ran.
	phases []string
}

// DeferGates returns an accumulator and registers its Report as a cleanup. The
// registration is what stops a breach recorded in an early phase from being
// dropped when a later phase takes an early exit: a cleanup node runs on every
// path out of the It(), including the panicking ones.
func DeferGates() *PhaseGates {
	g := &PhaseGates{failf: func(message string) { ginkgo.Fail(message, 1) }}
	ginkgo.DeferCleanup(func() { g.Report() })
	return g
}

// Check runs one phase's threshold assertions with Gomega failures captured
// rather than aborting, and reports whether that phase's thresholds all held.
//
// Everything inside assert is deferred, so assert must hold threshold comparisons
// and nothing else. A nil report or a returned error is structural: deferring it
// runs the next line against a nil pointer. And the Report* calls contain an
// EventuallyExpectHealthyPodCountWithTimeout whose timeout is a Gomega failure
// Check would capture, letting the phase continue against a cluster that never
// reached its target.
func (g *PhaseGates) Check(phase string, assert func()) bool {
	msgs := gomega.InterceptGomegaFailures(assert)
	if len(msgs) == 0 {
		return true
	}
	g.phases = append(g.phases, phase)
	for _, m := range msgs {
		g.entries = append(g.entries, fmt.Sprintf("%s: %s", phase, collapse(m)))
	}
	return false
}

// Breached reports whether any phase recorded a breach.
func (g *PhaseGates) Breached() bool { return len(g.entries) > 0 }

// Summary is one line per breach, each naming the phase and carrying Gomega's own
// message with the measured value and the threshold. A collapsed count would be
// worse than failing fast: the reader would have to open the log to learn which
// number moved.
func (g *PhaseGates) Summary() string {
	if len(g.entries) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d deferred performance gate failure(s) across %d phase(s) [%s].\n",
		len(g.entries), len(g.phases), strings.Join(g.phases, ", "))
	b.WriteString("Each phase ran and wrote its report; the failures below are thresholds, not lost measurements.\n")
	for i, e := range g.entries {
		fmt.Fprintf(&b, "\n%d. %s\n", i+1, e)
	}
	return b.String()
}

// Report fails the spec when anything was deferred, and does nothing otherwise.
// Idempotent, because Ginkgo may reach it through both the explicit call at the
// end of the It() and the registered cleanup.
func (g *PhaseGates) Report() {
	if len(g.entries) == 0 {
		return
	}
	summary := g.Summary()
	g.entries, g.phases = nil, nil
	g.failf(summary)
}

// collapse folds a Gomega failure message onto one line. Gomega formats
// BeNumerically over four lines; the phase list reads better with one line per
// breach, and no information is dropped.
func collapse(msg string) string {
	fields := strings.Fields(msg)
	return strings.Join(fields, " ")
}

// MemoryGrowth is the peak-memory delta across a phase boundary, in MB.
//
// One controller pod serves every phase of a test, and
// process_resident_memory_bytes is a level that Go does not promptly return to
// the OS, so a later phase's P95 RSS is bounded below by roughly the earlier
// phase's ending RSS. An absolute cap on a later phase therefore mostly measures
// what the earlier phase left behind, firing on a scale-out plateau and naming
// the consolidation phase. The first phase of each test keeps its absolute cap,
// where the pod is fresh.
func MemoryGrowth(prior, current *PerformanceReport) float64 {
	return current.KarpenterP95MemoryMB - prior.KarpenterP95MemoryMB
}

// Ready reports whether the cluster a phase left is a usable subject for the next
// phase, and says why not when it is not. It deliberately ignores whether the
// phase's thresholds passed: a breached memory cap says the controller was fat,
// not that the cluster is unmeasurable. Use ReadyToDisrupt or ReadyToConsolidate
// when the next phase needs the controller to act on the cluster rather than only
// to schedule onto it.
func Ready(report *PerformanceReport, expectedPods int) (bool, string) {
	if report == nil {
		return false, "the phase returned no report"
	}
	// Tautological today: ReportScaleOut and ReportConsolidation copy their
	// expectedPods argument into TotalPods rather than measuring it, and the count
	// is enforced by the EventuallyExpectHealthyPodCount inside them. Kept because
	// it starts failing for real the day TotalPods becomes a measurement.
	if report.TotalPods != expectedPods {
		return false, fmt.Sprintf("reached %d of %d pods", report.TotalPods, expectedPods)
	}
	// One node admits no consolidation decision, so a later phase measuring
	// consolidation on it would report a near-zero result that reads as an
	// improvement rather than as an absent experiment.
	if report.TotalNodes < 2 {
		return false, fmt.Sprintf("%d node(s), nothing for the next phase to consolidate", report.TotalNodes)
	}
	// One seed sample plus the two consecutive samples a CPU rate needs. Below
	// that the controller was not observable and the next phase's controller
	// metrics would be built from the same starved poller.
	if report.MetricsSampleCount < 3 {
		return false, fmt.Sprintf("%d controller metric sample(s), below the 3 a rate needs", report.MetricsSampleCount)
	}
	return true, ""
}

// ReadyToDisrupt is Ready plus the budget check a phase needs when it measures
// the controller removing or replacing nodes rather than only filling them.
func ReadyToDisrupt(report *PerformanceReport, expectedPods int, np *v1.NodePool) (bool, string) {
	if ok, why := Ready(report, expectedPods); !ok {
		return false, why
	}
	if np == nil {
		return false, "no NodePool"
	}
	for _, b := range np.Spec.Disruption.Budgets {
		if budgetBlocksAll(b) {
			return false, fmt.Sprintf("a disruption budget of %q blocks every node", b.Nodes)
		}
	}
	return true, ""
}

// ReadyToConsolidate is ReadyToDisrupt plus the two settings that decide whether
// the controller will consolidate an underutilized node at all. suite_test.go's
// BeforeEach sets them, so this guards against that drifting: a NodePool edit
// elsewhere would otherwise make a consolidation phase measure a controller that
// was never allowed to act, and fewer nodes removed would read as an improvement.
func ReadyToConsolidate(report *PerformanceReport, expectedPods int, np *v1.NodePool) (bool, string) {
	if ok, why := ReadyToDisrupt(report, expectedPods, np); !ok {
		return false, why
	}
	d := np.Spec.Disruption
	switch d.ConsolidationPolicy {
	case v1.ConsolidationPolicyWhenEmptyOrUnderutilized, v1.ConsolidationPolicyBalanced:
	default:
		return false, fmt.Sprintf("consolidationPolicy is %q, which does not consolidate underutilized nodes", d.ConsolidationPolicy)
	}
	if d.ConsolidateAfter.Duration == nil {
		return false, "consolidateAfter is Never"
	}
	return true, ""
}

// budgetBlocksAll reports whether a budget pins the allowed count at zero, in
// either the absolute or the percentage form. A malformed value is not treated
// as blocking; the API validation pattern already rejects those.
func budgetBlocksAll(b v1.Budget) bool {
	v := strings.TrimSuffix(b.Nodes, "%")
	n, err := strconv.Atoi(v)
	return err == nil && n == 0
}
