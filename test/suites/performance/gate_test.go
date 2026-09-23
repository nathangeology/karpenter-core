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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/onsi/gomega"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// These are plain Go tests rather than Ginkgo specs. Every spec in this package
// runs under TestIntegration, whose BeforeSuite builds a cluster environment, so
// a Ginkgo spec here could not run without a cluster. The Test prefix is TestUnit
// so `make test-helpers` can select them; see the Makefile.

// TestMain registers a fail handler so the global Gomega is configured. Check
// intercepts whichever handler is registered, so this one is never called on the
// paths under test; it only has to be non-nil. TestIntegration registers
// Ginkgo's Fail over the top of it for the e2e specs.
func TestMain(m *testing.M) {
	gomega.RegisterFailHandler(func(message string, _ ...int) { panic(message) })
	os.Exit(m.Run())
}

// gates returns an accumulator whose failure is recorded rather than ending a
// spec. DeferGates cannot be used here: it calls ginkgo.DeferCleanup, which
// needs a running spec.
func gates() (*PhaseGates, *[]string) {
	var failed []string
	g := &PhaseGates{failf: func(m string) { failed = append(failed, m) }}
	return g, &failed
}

func TestUnitPhaseGatesDeferredFailureStillFails(t *testing.T) {
	g, failed := gates()
	held := g.Check("basic/scaleOut", func() {
		gomega.Expect(300.0).To(gomega.BeNumerically("<", 260.0),
			"P95 memory should be under 260 MB during scale-out")
	})
	if held {
		t.Fatal("Check reported the phase held when its threshold breached")
	}
	if !g.Breached() {
		t.Fatal("Breached is false after a captured threshold failure")
	}
	g.Report()
	if len(*failed) != 1 {
		t.Fatalf("Report produced %d failures, want 1: a deferred breach that never fails is a silent green", len(*failed))
	}
	if !strings.Contains((*failed)[0], "basic/scaleOut") {
		t.Errorf("failure text does not name the phase: %s", (*failed)[0])
	}
	if !strings.Contains((*failed)[0], "300") || !strings.Contains((*failed)[0], "260") {
		t.Errorf("failure text drops the measured value or the threshold: %s", (*failed)[0])
	}
}

func TestUnitPhaseGatesKeepsEveryPhase(t *testing.T) {
	g, failed := gates()
	g.Check("interference/scaleOut", func() {
		gomega.Expect(700.0).To(gomega.BeNumerically("<", 620.0), "scale-out memory")
	})
	// A phase between two breaches that holds must not clear the first one.
	if !g.Check("interference/interference", func() {
		gomega.Expect(1.0).To(gomega.BeNumerically("<", 2.0), "interference memory")
	}) {
		t.Fatal("Check reported a breach for a phase whose threshold held")
	}
	g.Check("interference/consolidation", func() {
		gomega.Expect(900.0).To(gomega.BeNumerically("<", 820.0), "consolidation memory")
	})
	g.Report()
	if len(*failed) != 1 {
		t.Fatalf("Report produced %d failures, want 1 combined failure", len(*failed))
	}
	msg := (*failed)[0]
	for _, want := range []string{"interference/scaleOut", "interference/consolidation", "2 deferred", "2 phase(s)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure text is missing %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "interference/interference") {
		t.Errorf("a phase that held was reported as a breach: %s", msg)
	}
}

func TestUnitPhaseGatesSilentWhenNothingBreached(t *testing.T) {
	g, failed := gates()
	g.Check("basic/scaleOut", func() {})
	g.Report()
	if len(*failed) != 0 {
		t.Fatalf("Report failed a spec with nothing deferred: %v", *failed)
	}
	if g.Summary() != "" {
		t.Errorf("Summary is non-empty with nothing deferred: %q", g.Summary())
	}
}

func TestUnitPhaseGatesReportIsIdempotent(t *testing.T) {
	g, failed := gates()
	g.Check("basic/scaleOut", func() {
		gomega.Expect(1.0).To(gomega.BeNumerically("<", 0.0), "memory")
	})
	g.Report()
	g.Report()
	if len(*failed) != 1 {
		t.Fatalf("Report fired %d times, want 1: the explicit call and the cleanup both reach it", len(*failed))
	}
}

func nodePoolWith(policy v1.ConsolidationPolicy, after string, budgets ...string) *v1.NodePool {
	np := &v1.NodePool{}
	np.Spec.Disruption.ConsolidationPolicy = policy
	np.Spec.Disruption.ConsolidateAfter = v1.MustParseNillableDuration(after)
	for _, b := range budgets {
		np.Spec.Disruption.Budgets = append(np.Spec.Disruption.Budgets, v1.Budget{Nodes: b})
	}
	return np
}

func healthyReport() *PerformanceReport {
	return &PerformanceReport{
		TotalPods:          1000,
		TotalNodes:         153,
		MetricsSampleCount: 16,
		TotalTime:          80 * time.Second,
	}
}

func TestUnitReadyToConsolidate(t *testing.T) {
	good := nodePoolWith(v1.ConsolidationPolicyWhenEmptyOrUnderutilized, "30s", "100%")
	for _, tc := range []struct {
		name   string
		report func() *PerformanceReport
		np     *v1.NodePool
		ok     bool
		reason string
	}{
		{name: "healthy", report: healthyReport, np: good, ok: true},
		{
			name:   "no report",
			report: func() *PerformanceReport { return nil },
			np:     good, reason: "returned no report",
		},
		{
			name: "one node",
			report: func() *PerformanceReport {
				r := healthyReport()
				r.TotalNodes = 1
				return r
			},
			np: good, reason: "nothing for the next phase to consolidate",
		},
		{
			name: "two samples",
			report: func() *PerformanceReport {
				r := healthyReport()
				r.MetricsSampleCount = 2
				return r
			},
			np: good, reason: "below the 3 a rate needs",
		},
		{
			name: "short of the pod target",
			report: func() *PerformanceReport {
				r := healthyReport()
				r.TotalPods = 998
				return r
			},
			np: good, reason: "reached 998 of 1000 pods",
		},
		{
			name:   "consolidation off",
			report: healthyReport,
			np:     nodePoolWith(v1.ConsolidationPolicyWhenEmpty, "30s", "100%"),
			reason: "does not consolidate underutilized nodes",
		},
		{
			name:   "consolidateAfter never",
			report: healthyReport,
			np:     nodePoolWith(v1.ConsolidationPolicyWhenEmptyOrUnderutilized, "Never", "100%"),
			reason: "consolidateAfter is Never",
		},
		{
			name:   "budget blocks every node",
			report: healthyReport,
			np:     nodePoolWith(v1.ConsolidationPolicyWhenEmptyOrUnderutilized, "30s", "0%"),
			reason: "blocks every node",
		},
		{
			name:   "absolute zero budget",
			report: healthyReport,
			np:     nodePoolWith(v1.ConsolidationPolicyWhenEmptyOrUnderutilized, "30s", "0"),
			reason: "blocks every node",
		},
		{
			name:   "balanced policy is accepted",
			report: healthyReport,
			np:     nodePoolWith(v1.ConsolidationPolicyBalanced, "30s", "100%"),
			ok:     true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, why := ReadyToConsolidate(tc.report(), 1000, tc.np)
			if ok != tc.ok {
				t.Fatalf("ReadyToConsolidate = %v (%q), want %v", ok, why, tc.ok)
			}
			if tc.ok {
				if why != "" {
					t.Errorf("a reason was returned on a healthy report: %q", why)
				}
				return
			}
			if !strings.Contains(why, tc.reason) {
				t.Errorf("reason %q does not contain %q", why, tc.reason)
			}
		})
	}
}

// The drift phase measures node replacement, which budgets gate and
// consolidationPolicy does not. A drift boundary must not be rejected for a
// setting drift never consults.
func TestUnitReadyToDisruptIgnoresConsolidationPolicy(t *testing.T) {
	np := nodePoolWith(v1.ConsolidationPolicyWhenEmpty, "Never", "100%")
	if ok, why := ReadyToDisrupt(healthyReport(), 1000, np); !ok {
		t.Errorf("ReadyToDisrupt rejected a NodePool whose budgets permit disruption: %q", why)
	}
	if ok, _ := ReadyToConsolidate(healthyReport(), 1000, np); ok {
		t.Error("ReadyToConsolidate accepted consolidationPolicy WhenEmpty with consolidateAfter Never")
	}
	blocked := nodePoolWith(v1.ConsolidationPolicyWhenEmptyOrUnderutilized, "30s", "0%")
	if ok, why := ReadyToDisrupt(healthyReport(), 1000, blocked); ok {
		t.Error("ReadyToDisrupt accepted a budget that blocks every node")
	} else if !strings.Contains(why, "blocks every node") {
		t.Errorf("reason %q does not name the budget", why)
	}
}

// Ready takes no NodePool: a phase that only schedules onto the cluster the
// previous phase built needs no disruption setting.
func TestUnitReadyIsReportOnly(t *testing.T) {
	if ok, why := Ready(healthyReport(), 1000); !ok {
		t.Errorf("Ready rejected a healthy report: %q", why)
	}
	if ok, why := Ready(nil, 1000); ok || !strings.Contains(why, "returned no report") {
		t.Errorf("Ready(nil) = %v %q", ok, why)
	}
}
