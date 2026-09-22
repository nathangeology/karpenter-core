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

package main

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("gate-key registry", func() {
	parse := func(body string) (*gateRegistry, error) {
		return parseRegistry("registry.txt", strings.NewReader(body))
	}

	Describe("parsing", func() {
		It("reads thresholds, the screen sentinel with its reason, and the reserved bounds", func() {
			reg, err := parse(`
# a comment line on its own
_gate  cpu_core_cap  2.0

scale_out timeout_seconds    900   # basic_test.go:44
scale_out assert_max_seconds 120   # basic_test.go:50
scale_out total_time         1.05  # T_min 1.040
consolidation total_time     none  # loop-instrumented
`)
			Expect(err).ToNot(HaveOccurred())
			t, gated := reg.threshold("scale_out", "total_time")
			Expect(gated).To(BeTrue())
			Expect(t).To(Equal(1.05))
			_, gated = reg.threshold("consolidation", "total_time")
			Expect(gated).To(BeFalse())
			Expect(reg.registered("consolidation", "total_time")).To(BeTrue())
			// The reason is the whole point of the sentinel: a screened key has
			// to say why, otherwise it is indistinguishable from an omission.
			Expect(reg.screened["consolidation"]["total_time"]).To(Equal("loop-instrumented"))
			Expect(reg.timeouts["scale_out"]).To(Equal(900.0))
			Expect(reg.assertMax["scale_out"]).To(Equal(120.0))
			Expect(reg.params[paramCPUCoreCap]).To(Equal(2.0))
			Expect(reg.phaseKeys()).To(Equal([]string{"consolidation", "scale_out"}))
		})

		It("records a screen with no comment as deliberate but unexplained", func() {
			reg, err := parse("scale_out total_time none\n")
			Expect(err).ToNot(HaveOccurred())
			Expect(reg.screened["scale_out"]["total_time"]).To(Equal("no reason recorded"))
		})

		It("reports an unregistered pair as unregistered", func() {
			reg, err := parse("scale_out total_time 1.05\n")
			Expect(err).ToNot(HaveOccurred())
			Expect(reg.registered("scale_out", "total_nodes")).To(BeFalse())
			Expect(reg.registered("other_key", "total_time")).To(BeFalse())
		})

		DescribeTable("rejects a malformed record at parse time rather than at gate time",
			func(body, want string) {
				_, err := parse(body)
				Expect(err).To(MatchError(ContainSubstring(want)))
			},
			Entry("two fields", "scale_out total_time\n", "want 3"),
			Entry("four fields", "scale_out total_time 1.05 extra\n", "want 3"),
			// A threshold of 1 alerts on a ratio of exactly 1.000, which several
			// keys report on every sample because their value is quantized.
			Entry("threshold at 1", "scale_out total_time 1.0\n", "alerts on no change"),
			Entry("threshold below 1", "scale_out total_time 0.9\n", "alerts on no change"),
			Entry("non-numeric threshold", "scale_out total_time loose\n", "want a ratio above 1"),
			Entry("duplicate key", "scale_out total_time 1.05\nscale_out total_time 1.10\n", "duplicate entry"),
			Entry("duplicate bound", "scale_out timeout_seconds 900\nscale_out timeout_seconds 901\n", "duplicate timeout_seconds"),
			Entry("negative bound", "scale_out timeout_seconds -1\n", "positive number of seconds"),
			Entry("unknown parameter", "_gate mystery 2.0\n", "unknown _gate parameter"),
			Entry("no entries", "# only comments\n", "no key entries found"),
		)

		It("treats an absent file as no registry rather than an error", func() {
			// Local invocations and the Regression suite's caller run with no
			// file present. Failing there would break callers that never gated.
			reg, err := loadRegistry(filepath.Join(os.TempDir(), "definitely-not-a-registry-file.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(reg).To(BeNil())
		})

		It("parses the committed registry and screens every loop-instrumented key", func() {
			// Guards the shipped file itself, not just the parser. A registry
			// that parses but routes nothing is the failure mode worth catching.
			reg, err := loadRegistry(filepath.Join("..", "..", "perf-gate-keys.txt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(reg).ToNot(BeNil())
			Expect(reg.phaseKeys()).To(HaveLen(15))
			gatedKeys := map[string]bool{}
			nGated, nScreened := 0, 0
			for _, phase := range reg.phaseKeys() {
				for _, m := range gatedMetrics() {
					Expect(reg.registered(phase, m.jsonField)).To(BeTrue(), "%s %s is unregistered", phase, m.jsonField)
					if _, gated := reg.threshold(phase, m.jsonField); gated {
						gatedKeys[phase] = true
						nGated++
					} else {
						nScreened++
					}
				}
				// Every phase needs a measurement timeout, because the censoring
				// screen cannot recover it from the reports.
				Expect(reg.timeouts).To(HaveKey(phase))
			}
			// 15 phase keys x 7 gateable fields.
			Expect(nGated + nScreened).To(Equal(105))
			// The eight ReportScaleOut keys gate; the six ReportConsolidation
			// keys and the one ReportDrift key are screened outright.
			Expect(gatedKeys).To(HaveLen(8))
			for _, screened := range []string{
				"consolidation", "do_not_disrupt_consolidation", "hostname_spread_consolidation",
				"hostname_spread_xl_consolidation", "self_antiaffinity_interference_consolidation",
				"wide_deployments_consolidation", "drift_execution",
			} {
				Expect(gatedKeys).ToNot(HaveKey(screened))
			}
			// resource_efficiency_score is 90*cpu_util + 10*mem_util exactly
			// (report.go:128), so it carries no information the two fields it is
			// built from do not, and gating it would inflate the multiplicity
			// count that sets every other threshold.
			for _, phase := range reg.phaseKeys() {
				_, gated := reg.threshold(phase, "resource_efficiency_score")
				Expect(gated).To(BeFalse(), "%s gates a derived field", phase)
			}
		})
	})

	Describe("censoring screen", func() {
		// The measurement-timeout bound. ReportConsolidation's monitor loop
		// exits on the clock and returns normally, so the phase records the
		// timeout as its value. Measured on
		// self_antiaffinity_interference_consolidation: 19 of 19 samples between
		// 922.9 and 977.9 s against a 900 s timeout, sample CV 1.23%.
		It("drops a sample that reached the measurement timeout and says which bound fired", func() {
			reg := mustParseRegistry("k timeout_seconds 900\nk total_time 1.05\n")
			byTest := map[string][]map[string]any{
				"k_performance_report.json": {
					{"total_time": 300e9}, {"total_time": 310e9}, {"total_time": 305e9},
					{"total_time": 302e9}, {"total_time": 940e9},
				},
			}
			var buf strings.Builder
			kept, res := reg.censor(byTest, &buf)
			Expect(kept["k_performance_report.json"]).To(HaveLen(4))
			Expect(res["k"].censored).To(Equal(1))
			Expect(res["k"].reason).To(BeEmpty(), "1 of 5 is at the tolerance, not above it")
			Expect(buf.String()).To(ContainSubstring("measurement timeout"))
		})

		// The suite's own absolute bound. A sample above TotalTimeThreshold
		// would have failed the leg, so it reaches the batch only when a
		// provider widened the bound through KARPENTER_PERF_THRESHOLDS.
		// Measured on self_antiaffinity_scale_out_small: one sample at 456.4 s
		// against a 300 s bound, in a batch whose other nine sit at 123 to 150.
		It("drops a sample above the suite's own absolute bound even when far below the timeout", func() {
			reg := mustParseRegistry("k timeout_seconds 900\nk assert_max_seconds 300\nk total_time 1.05\n")
			byTest := map[string][]map[string]any{
				"k_performance_report.json": {
					{"total_time": 123e9}, {"total_time": 130e9}, {"total_time": 149e9},
					{"total_time": 456e9},
				},
			}
			var buf strings.Builder
			kept, res := reg.censor(byTest, &buf)
			Expect(kept["k_performance_report.json"]).To(HaveLen(3))
			Expect(res["k"].censored).To(Equal(1))
			Expect(buf.String()).To(ContainSubstring("the suite's own 300s bound"))
		})

		It("reports a key unmeasurable once more than a fifth of its samples are censored", func() {
			// Set so 2 of 10 is tolerable and 3 of 10 is not.
			reg := mustParseRegistry("k timeout_seconds 900\nk total_time 1.05\n")
			samples := []map[string]any{}
			for i := 0; i < 7; i++ {
				samples = append(samples, map[string]any{"total_time": 300e9})
			}
			for i := 0; i < 3; i++ {
				samples = append(samples, map[string]any{"total_time": 940e9})
			}
			var buf strings.Builder
			_, res := reg.censor(map[string][]map[string]any{"k_performance_report.json": samples}, &buf)
			Expect(res["k"].reason).To(ContainSubstring("3 of 10 samples censored"))
			Expect(buf.String()).To(ContainSubstring("Performance key unmeasurable"))
		})

		It("emits no entry at all for a key whose every sample is censored", func() {
			// The correct disposition for a phase that only ever records its
			// timeout. Not a screened comparison: no comparison.
			reg := mustParseRegistry("k timeout_seconds 900\nk total_time 1.05\n")
			var buf strings.Builder
			kept, res := reg.censor(map[string][]map[string]any{
				"k_performance_report.json": {{"total_time": 923e9}, {"total_time": 978e9}},
			}, &buf)
			Expect(kept).To(BeEmpty())
			Expect(res["k"].reason).ToNot(BeEmpty())
			Expect(buf.String()).To(ContainSubstring("every one of its 2 samples was censored"))
		})

		It("leaves a key with no declared bounds untouched", func() {
			reg := mustParseRegistry("k total_time 1.05\n")
			kept, res := reg.censor(map[string][]map[string]any{
				"k_performance_report.json": {{"total_time": 99999e9}},
			}, &strings.Builder{})
			Expect(kept["k_performance_report.json"]).To(HaveLen(1))
			Expect(res).To(BeEmpty())
		})
	})

	Describe("ceiling screen", func() {
		// HELM_OPTS limits the controller to 2 cores (Makefile:9), so
		// karpenter_p95_cpu_cores is a rate bounded above and the largest ratio
		// a key can report is 2.0/baseline. Recomputed per run because the
		// baseline is not stable: hostname_spread_consolidation reads 1.456
		// cores on the fresh-runner batch, 0.752 in the positive control and
		// 1.560 on upstream main.
		var cpu metricSpec
		BeforeEach(func() {
			for _, m := range metrics {
				if m.jsonField == "karpenter_p95_cpu_cores" {
					cpu = m
				}
			}
			Expect(cpu.cappedBy).To(Equal(paramCPUCoreCap))
		})

		It("computes the ceiling from the batch median", func() {
			reg := mustParseRegistry("_gate cpu_core_cap 2.0\nk karpenter_p95_cpu_cores 1.14\n")
			ceil, capped := reg.ceiling(cpu, 1.802)
			Expect(capped).To(BeTrue())
			Expect(ceil).To(BeNumerically("~", 1.110, 0.001))
		})

		It("treats an uncapped field as uncapped", func() {
			reg := mustParseRegistry("_gate cpu_core_cap 2.0\nk total_time 1.05\n")
			_, capped := reg.ceiling(metrics[0], 100)
			Expect(capped).To(BeFalse())
		})

		It("screens a key whose shipped threshold sits at or above its ceiling", func() {
			// hostname_spread_xl_scale_out: baseline 1.802 cores, ceiling 1.11,
			// T_min 1.14. No regression however large can trip it, so a pass
			// carries no information. The registry ships it as none for that
			// reason; this covers the run-time screen that catches the same
			// condition when the baseline moves under a gated key.
			tmp := GinkgoT().TempDir()
			for i := 1; i <= 3; i++ {
				dir := filepath.Join(tmp, "iter_"+string(rune('0'+i)))
				Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
				writeReport(filepath.Join(dir, "k_performance_report.json"), map[string]any{
					"karpenter_p95_cpu_cores": 1.802,
				})
			}
			reg := mustParseRegistry("_gate cpu_core_cap 2.0\nk karpenter_p95_cpu_cores 1.14\n")
			var buf strings.Builder
			Expect(run(tmp, 3, 3, reg, &buf)).To(Succeed())
			Expect(buf.String()).To(ContainSubstring("Performance key unfalsifiable"))
			Expect(loadEntries(filepath.Join(tmp, resultsFileFor("controller_cpu")))).To(BeEmpty())
			Expect(entryNames(loadEntries(filepath.Join(tmp, resultsFileFor(observedSmallerSlug))))).
				To(ContainElement("K - Controller CPU (median)"))
		})
	})

	Describe("routing errors", func() {
		var tmp string
		BeforeEach(func() {
			tmp = GinkgoT().TempDir()
			seedSyntheticIterations(tmp, 3)
		})

		// This is the whole reason the file exists. benchmark-action skips a key
		// it cannot match without saying so, so a key nobody registered and a
		// key screened on purpose produce the same green step.
		It("refuses to run when a gateable field of a present key has no entry", func() {
			reg := mustParseRegistry("test_a total_time 1.10\n")
			err := run(tmp, 3, 3, reg, os.Stdout)
			Expect(err).To(MatchError(ContainSubstring("has no entry in")))
			Expect(err.Error()).To(ContainSubstring("total_nodes"))
		})

		// Measured: replayed on 33 real upstream main runs, which take one
		// iteration each, the shipped thresholds fire on 93.8% of them. At n=10
		// the same thresholds carry a 0.49% union false-positive rate. The
		// thresholds and the sample design are one artefact.
		It("refuses to gate a batch smaller than the sample count the thresholds were derived for", func() {
			reg := mustParseRegistry("_gate derived_for_n 10\ntest_a total_time 1.10\n")
			err := run(tmp, 3, 3, reg, os.Stdout)
			Expect(err).To(MatchError(ContainSubstring("derived_for_n=10 but ITERATIONS=3")))
			Expect(err.Error()).To(ContainSubstring("Refusing to gate"))
		})

		It("refuses to run when a registered field went missing from the batch", func() {
			// The other direction: the registry expects a measurement the suite
			// stopped producing. Left as a skip this is a silent green.
			reg := mustParseRegistry(`
test_a total_time                         1.10
test_a karpenter_p95_memory_mb            1.15
test_a total_nodes                        1.05
test_a total_reserved_cpu_utilization     1.05
test_a resource_efficiency_score          none
test_a total_reserved_memory_utilization  1.05
test_b karpenter_p95_cpu_cores            1.20
test_b total_nodes                        1.05
`)
			err := run(tmp, 3, 3, reg, os.Stdout)
			Expect(err).To(MatchError(ContainSubstring("absent from every sample")))
			Expect(err.Error()).To(ContainSubstring("karpenter_p95_memory_mb"))
		})
	})
})
