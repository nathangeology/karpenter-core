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
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPerfAggregate(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Perf Aggregate Suite")
}

var _ = Describe("Perf Aggregate", func() {
	Describe("computeStats", func() {
		It("computes n/median/mean/min/max/stddev/cv for odd-count values", func() {
			got := computeStats([]float64{1, 2, 3, 4, 5})
			Expect(got.N).To(Equal(5))
			Expect(got.Median).To(Equal(3.0))
			Expect(got.Mean).To(Equal(3.0))
			Expect(got.Min).To(Equal(1.0))
			Expect(got.Max).To(Equal(5.0))
			// Population stddev of {1,2,3,4,5} == sqrt(2) ~= 1.414.
			Expect(got.Stddev).To(BeNumerically("~", 1.41, 0.01))
			// CV = stddev/mean * 100 = 47.1%.
			Expect(got.CVPct).To(BeNumerically("~", 47.1, 0.1))
		})

		It("averages the two middle values for even-count median", func() {
			got := computeStats([]float64{10, 20, 30, 40})
			Expect(got.Median).To(Equal(25.0))
		})
	})

	Describe("extractValues", func() {
		It("converts total_time > 1e9 from ns to seconds and passes cpu cores through unchanged", func() {
			// total_time > 1e9 is interpreted as nanoseconds and converted to
			// seconds. karpenter_p95_cpu_cores is already in cores in
			// types.go, so it passes through unchanged.
			datas := []map[string]any{
				{"total_time": 2e9},
				{"total_time": 3e9},
				{"karpenter_p95_cpu_cores": 0.5},
			}
			Expect(extractValues(datas, "total_time")).To(Equal([]float64{2.0, 3.0}))
			Expect(extractValues(datas, "karpenter_p95_cpu_cores")).To(Equal([]float64{0.5}))
		})
	})

	Describe("run end to end", func() {
		var tmp string

		BeforeEach(func() {
			var err error
			tmp, err = os.MkdirTemp("", "perf-aggregate-*")
			Expect(err).ToNot(HaveOccurred())
		})

		AfterEach(func() {
			Expect(os.RemoveAll(tmp)).To(Succeed())
		})

		Context("with synthetic per-iteration reports seeded across 3 iterations", func() {
			const iters = 3

			BeforeEach(func() {
				seedSyntheticIterations(tmp, iters)
				Expect(run(tmp, iters, iters, syntheticRegistry(), os.Stdout)).To(Succeed())
			})

			It("gives each gated field its own step so each can carry its own threshold", func() {
				// One file per gated field, holding only that field's keys. That
				// is what lets the registry's per-key numbers reach
				// benchmark-action, which takes one threshold per step.
				duration := loadEntries(filepath.Join(tmp, "benchmark-results-duration.json"))
				cpu := loadEntries(filepath.Join(tmp, "benchmark-results-controller_cpu.json"))
				cpuUtil := loadEntries(filepath.Join(tmp, "benchmark-results-cpu_util.json"))
				nodes := loadEntries(filepath.Join(tmp, "benchmark-results-final_nodes.json"))
				Expect(duration).To(HaveLen(1))
				Expect(duration[0].Name).To(Equal("Test A - Duration (median)"))
				Expect(cpu).To(HaveLen(1))
				Expect(cpu[0].Name).To(Equal("Test B - Controller CPU (median)"))
				Expect(cpuUtil).To(HaveLen(1))
				Expect(cpuUtil[0].Name).To(ContainSubstring("CPU Utilization"))
				// Both tests report total_nodes, so this is the one step here
				// that holds more than one key.
				Expect(nodes).To(HaveLen(2))
			})

			It("routes a screened field to the observe-only file and out of every gating file", func() {
				observed := loadEntries(filepath.Join(tmp, resultsFileFor(observedBiggerSlug)))
				Expect(entryNames(observed)).To(ContainElement("Test A - Efficiency Score (median)"))
				Expect(loadEntries(filepath.Join(tmp, "benchmark-results-efficiency_score.json"))).To(BeEmpty())
			})

			It("emits Consolidation Rounds into the CV file only, never into a gating or observe file", func() {
				cv := loadEntries(filepath.Join(tmp, "benchmark-results-cv.json"))
				Expect(entryNames(cv)).To(ContainElement("Test A - Consolidation Rounds (CV%)"))
				for _, f := range allResultFiles() {
					if f == resultsFileFor(cvSlug) {
						continue
					}
					for _, e := range loadEntries(filepath.Join(tmp, f)) {
						Expect(e.Name).ToNot(ContainSubstring("Consolidation Rounds"), "%s leaked Consolidation Rounds: %s", f, e.Name)
					}
				}
			})

			It("records the correct per-metric medians in aggregated_summary.json", func() {
				var summary map[string]map[string]stats
				loadJSON(filepath.Join(tmp, "aggregated_summary.json"), &summary)
				testA := summary["test_a_performance_report.json"]
				Expect(testA).ToNot(BeNil(), "summary missing test_a")
				Expect(testA["Duration"].Median).To(Equal(3.0))
				Expect(testA["Efficiency Score"].Median).To(Equal(72.0))
				testB := summary["test_b_performance_report.json"]
				Expect(testB).ToNot(BeNil(), "summary missing test_b")
				Expect(testB["Controller CPU"].Median).To(Equal(0.2))
			})

			It("emits one CV entry per (test, metric) tagged with cv-percent and embeds median/stddev/n in Extra", func() {
				entries := loadEntries(filepath.Join(tmp, "benchmark-results-cv.json"))
				Expect(entries).ToNot(BeEmpty(), "expected at least one CV entry")
				// Expected count: sum of metrics present across test_a and
				// test_b in the seed helper. Test A supplies Duration + Final
				// Nodes + CPU Util + Efficiency + Mem Util + Rounds = 6. Test
				// B supplies Controller CPU + Final Nodes = 2. Total = 8.
				Expect(entries).To(HaveLen(8))
				for _, e := range entries {
					Expect(e.Unit).To(Equal("cv-percent"))
					Expect(e.Name).To(ContainSubstring("CV%"))
					Expect(e.Extra).To(ContainSubstring("median="))
					Expect(e.Extra).To(ContainSubstring("stddev="))
				}
			})
		})

		It("publishes one threshold and one gated flag per step, so the yaml carries no numbers", func() {
			seedSyntheticIterations(tmp, 3)
			out := filepath.Join(tmp, "step-output")
			GinkgoT().Setenv("GITHUB_OUTPUT", out)
			Expect(run(tmp, 3, 3, syntheticRegistry(), os.Stdout)).To(Succeed())
			b, err := os.ReadFile(out) //nolint:gosec // G304: test tempdir
			Expect(err).ToNot(HaveOccurred())
			got := string(b)
			// test_a total_time is registered at 1.10.
			Expect(got).To(ContainSubstring("gated_duration=true"))
			Expect(got).To(ContainSubstring("threshold_duration=110%"))
			// Both total_nodes keys are registered at 1.05, so the step runs at
			// the loosest of them, which is that value.
			Expect(got).To(ContainSubstring("threshold_final_nodes=105%"))
			// Screened everywhere, so its step must not run: benchmark-action on
			// an empty array stores a baseline entry holding no keys, which then
			// reads as a baseline that legitimately has none.
			Expect(got).To(ContainSubstring("gated_efficiency_score=false"))
			Expect(got).ToNot(ContainSubstring("threshold_efficiency_score="))
			// No synthetic key reports peak memory.
			Expect(got).To(ContainSubstring("gated_peak_memory=false"))
		})

		It("observes every gateable field and gates none when no registry is present", func() {
			// The local and Regression-suite path. Without a committed list of
			// what should be gated, gating anything is a guess, so the safe
			// direction is to compare nothing and record everything.
			seedSyntheticIterations(tmp, 3)
			Expect(run(tmp, 3, 3, nil, os.Stdout)).To(Succeed())
			for _, m := range gatedMetrics() {
				Expect(loadEntries(filepath.Join(tmp, resultsFileFor(m.slug)))).To(BeEmpty(), "%s gated without a registry", m.slug)
			}
			Expect(loadEntries(filepath.Join(tmp, resultsFileFor(observedSmallerSlug)))).ToNot(BeEmpty())
			Expect(loadEntries(filepath.Join(tmp, resultsFileFor(observedBiggerSlug)))).ToNot(BeEmpty())
		})

		Context("with no iter_* subdirs at all", func() {
			It("fails closed rather than emitting empty benchmark files", func() {
				err := run(tmp, 5, 5, nil, os.Stdout)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no performance reports found"))
			})
		})

		Context("with a test missing an iteration report", func() {
			It("fails closed rather than gating on partial data", func() {
				// Seed 3 iterations for test_a but only 2 for test_b.
				for i := 1; i <= 3; i++ {
					iterDir := filepath.Join(tmp, "iter_"+strconv.Itoa(i))
					Expect(os.MkdirAll(iterDir, 0o755)).To(Succeed())
					writeReport(filepath.Join(iterDir, "test_a_performance_report.json"), map[string]any{
						"total_nodes": 10 + i,
					})
					if i <= 2 {
						writeReport(filepath.Join(iterDir, "test_b_performance_report.json"), map[string]any{
							"total_nodes": 20 + i,
						})
					}
				}
				err := run(tmp, 3, 3, nil, os.Stdout)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("refusing to gate on partial data"))
			})
		})

		// Samples now arrive from one runner each, so a lost runner is a gap in
		// the batch rather than a truncation. The quorum decides whether what
		// arrived is enough to gate on.
		Context("with a batch short of the full sample count", func() {
			BeforeEach(func() {
				seedSyntheticIterations(tmp, 2)
			})

			It("gates and warns when the batch clears the quorum", func() {
				var buf strings.Builder
				Expect(run(tmp, 3, 2, syntheticRegistry(), &buf)).To(Succeed())
				Expect(buf.String()).To(ContainSubstring("Performance batch short"))
				Expect(buf.String()).To(ContainSubstring("gated on 2 of 3 samples"))
				for _, e := range loadEntries(filepath.Join(tmp, "benchmark-results-duration.json")) {
					Expect(e.Extra).To(ContainSubstring("n=2"))
				}
			})

			It("refuses when the batch falls below the quorum", func() {
				err := run(tmp, 3, 3, syntheticRegistry(), os.Stdout)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("below the quorum of 3"))
			})
		})

		// download-artifact names each downloaded directory after its artifact,
		// so a batch gathered from per-iteration runners arrives under names
		// that are not iter_N. The sample identity is the directory, whatever
		// it is called.
		Context("with sample directories named after artifacts", func() {
			It("treats each directory as one sample regardless of its name", func() {
				names := []string{
					"performance-results-Drift Performance-iter-1-9001",
					"performance-results-Drift Performance-iter-2-9001",
					"performance-results-Drift Performance-iter-7-9001",
				}
				for i, name := range names {
					dir := filepath.Join(tmp, name)
					Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
					writeReport(filepath.Join(dir, "drift_execution_performance_report.json"), map[string]any{
						"total_nodes": float64(300 + i),
					})
				}
				var buf strings.Builder
				reg := mustParseRegistry("drift_execution total_nodes 1.05\n")
				Expect(run(tmp, 3, 3, reg, &buf)).To(Succeed())
				Expect(buf.String()).To(ContainSubstring("Collected 3 sample directories"))
				entries := loadEntries(filepath.Join(tmp, "benchmark-results-final_nodes.json"))
				Expect(entries).To(HaveLen(1))
				Expect(entries[0].Name).To(Equal("Drift Execution - Final Nodes (median)"))
				Expect(entries[0].Value).To(Equal(301.0))
				Expect(entries[0].Extra).To(ContainSubstring("n=3"))
			})
		})
	})

	Describe("gate key names", func() {
		var tmp string

		BeforeEach(func() {
			var err error
			tmp, err = os.MkdirTemp("", "perf-aggregate-keys-*")
			Expect(err).ToNot(HaveOccurred())
			seedSyntheticIterations(tmp, 3)
			Expect(run(tmp, 3, 3, syntheticRegistry(), os.Stdout)).To(Succeed())
		})

		AfterEach(func() {
			Expect(os.RemoveAll(tmp)).To(Succeed())
		})

		It("omits the iteration count so changing repeat does not void the stored baseline", func() {
			// benchmark-action matches history by exact key name. An n in the
			// name means a repeat-count change silently drops every baseline.
			seen := 0
			for _, f := range allResultFiles() {
				for _, e := range loadEntries(filepath.Join(tmp, f)) {
					Expect(e.Name).ToNot(ContainSubstring("n="), "%s key embeds the iteration count: %s", f, e.Name)
					seen++
				}
			}
			Expect(seen).ToNot(BeZero())
		})

		It("keeps the iteration count in Extra, which benchmark-action does not match on", func() {
			for _, e := range loadEntries(filepath.Join(tmp, "benchmark-results-duration.json")) {
				Expect(e.Extra).To(ContainSubstring("n=3"))
			}
		})
	})

	Describe("checkBaseline", func() {
		var tmp, cacheDir string
		const prefix = "Karpenter Performance (Test Suite)"

		BeforeEach(func() {
			var err error
			tmp, err = os.MkdirTemp("", "perf-aggregate-baseline-*")
			Expect(err).ToNot(HaveOccurred())
			cacheDir = filepath.Join(tmp, "cache")
			seedSyntheticIterations(tmp, 3)
			Expect(run(tmp, 3, 3, syntheticRegistry(), os.Stdout)).To(Succeed())
		})

		AfterEach(func() {
			Expect(os.RemoveAll(tmp)).To(Succeed())
		})

		cfg := func(root, cache string) baselineConfig {
			return baselineConfig{outputDir: root, baselineDir: cache, stateDir: cache, namePrefix: prefix, runID: "42"}
		}

		// Nothing compared, no cache entry matched, no state file: a scope
		// nobody has written to. Seeding is the only possible outcome and it
		// needs no flag.
		It("reports a seed and records state when the scope has never been written to", func() {
			var buf strings.Builder
			Expect(checkBaseline(cfg(tmp, cacheDir), &buf)).To(Succeed())
			Expect(buf.String()).To(ContainSubstring("Performance baseline seeded"))
			Expect(buf.String()).To(ContainSubstring("0 compared"))
			var state baselineState
			loadJSON(filepath.Join(cacheDir, "baseline-state.json"), &state)
			Expect(state.FirstSeedRun).To(Equal("42"))
			Expect(state.GatedKeys["duration"]).ToNot(BeEmpty())
		})

		// The landing shape: the cache entry matched and the state survived, but
		// the chart names moved, so nothing compares. Without the registry that
		// reads as a first seed on every key. The registry is committed, so it
		// still declares those keys gated and the run fails unless the seed is
		// asked for.
		It("fails when the cache matched and the registry declares keys the history lacks", func() {
			Expect(checkBaseline(cfg(tmp, cacheDir), os.Stdout)).To(Succeed())
			c := cfg(tmp, cacheDir)
			c.cacheHit = "Linux-perf-benchmark-Test Suite--run-8999-1"
			c.registry = syntheticRegistry()
			var buf strings.Builder
			err := checkBaseline(c, &buf)
			Expect(err).To(MatchError(ContainSubstring("baseline missing")))
			Expect(err.Error()).To(ContainSubstring("declared by"))
			Expect(buf.String()).To(ContainSubstring("ALLOW_BASELINE_SEED"))
		})

		It("seeds the same shape once the seed is asked for", func() {
			Expect(checkBaseline(cfg(tmp, cacheDir), os.Stdout)).To(Succeed())
			c := cfg(tmp, cacheDir)
			c.cacheHit = "Linux-perf-benchmark-Test Suite--run-8999-1"
			c.registry = syntheticRegistry()
			c.allowSeed = true
			var buf strings.Builder
			Expect(checkBaseline(c, &buf)).To(Succeed())
			Expect(buf.String()).To(ContainSubstring("ALLOW_BASELINE_SEED"))
		})

		It("fails when a key the last green run gated on has lost its baseline", func() {
			// First run seeds and records the key set. The second run finds no
			// history file, which is the silent-pass case this guards. The state
			// file survived, so this is not the first-run shape.
			Expect(checkBaseline(cfg(tmp, cacheDir), os.Stdout)).To(Succeed())
			var buf strings.Builder
			err := checkBaseline(cfg(tmp, cacheDir), &buf)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("baseline missing"))
			Expect(buf.String()).To(ContainSubstring("Performance baseline missing"))
		})

		It("reports keys as compared when the stored history carries them", func() {
			Expect(checkBaseline(cfg(tmp, cacheDir), os.Stdout)).To(Succeed())
			seedBaselineHistory(tmp, cacheDir, prefix)
			c := cfg(tmp, cacheDir)
			c.registry = syntheticRegistry()
			var buf strings.Builder
			Expect(checkBaseline(c, &buf)).To(Succeed())
			Expect(buf.String()).To(ContainSubstring("0 seeded, 0 missing"))
			Expect(buf.String()).ToNot(ContainSubstring("Performance baseline seeded"))
		})

		// A partial loss is unambiguous whatever the cache says: keys compared,
		// so the scope is populated, and the ones that did not compare lost
		// history they are committed to have.
		It("fails on a partial loss even with no cache hit and no state", func() {
			seedBaselineHistory(tmp, cacheDir, prefix)
			Expect(os.RemoveAll(filepath.Join(cacheDir, "duration"))).To(Succeed())
			c := cfg(tmp, cacheDir)
			c.stateDir = filepath.Join(tmp, "baseline-state")
			c.registry = syntheticRegistry()
			err := checkBaseline(c, os.Stdout)
			Expect(err).To(MatchError(ContainSubstring("baseline missing")))
			Expect(err.Error()).To(ContainSubstring("duration /"))
		})

		It("requires BENCH_NAME_PREFIX so a missing chart name cannot read as an empty baseline", func() {
			c := cfg(tmp, cacheDir)
			c.namePrefix = ""
			Expect(checkBaseline(c, os.Stdout)).To(MatchError(ContainSubstring("BENCH_NAME_PREFIX is required")))
		})

		It("refuses to treat a corrupt history file as an absent one", func() {
			Expect(os.MkdirAll(filepath.Join(cacheDir, "duration"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(cacheDir, "duration", "benchmark-data.json"), []byte("{not json"), 0o600)).To(Succeed())
			Expect(checkBaseline(cfg(tmp, cacheDir), os.Stdout)).To(MatchError(ContainSubstring("parsing")))
		})

		// The state file used to live inside the tree it audits, so one cache
		// eviction removed the history and the record of what had been gated
		// together and every key read as a legitimate first seed.
		It("writes the state outside the audited baseline tree", func() {
			c := cfg(tmp, cacheDir)
			c.stateDir = filepath.Join(tmp, "baseline-state")
			Expect(checkBaseline(c, os.Stdout)).To(Succeed())
			Expect(filepath.Join(cacheDir, "baseline-state.json")).ToNot(BeAnExistingFile())
			var state baselineState
			loadJSON(filepath.Join(c.stateDir, "baseline-state.json"), &state)
			Expect(state.FirstSeedRun).To(Equal("42"))
		})

		It("still carries the audit across runs when the state lives in its own directory", func() {
			c := cfg(tmp, cacheDir)
			c.stateDir = filepath.Join(tmp, "baseline-state")
			Expect(checkBaseline(c, os.Stdout)).To(Succeed())
			c.allowSeed = false
			err := checkBaseline(c, os.Stdout)
			Expect(err).To(MatchError(ContainSubstring("baseline missing")))
		})

		// A restored history entry with no state file is an eviction, not a
		// first run, and seeding over it reports a green gate having compared
		// nothing.
		It("fails when the baseline cache matched but the state file is gone", func() {
			c := cfg(tmp, cacheDir)
			c.stateDir = filepath.Join(tmp, "baseline-state")
			c.cacheHit = "Linux-perf-benchmark-Drift Performance--run-8999-1"
			var buf strings.Builder
			err := checkBaseline(c, &buf)
			Expect(err).To(MatchError(ContainSubstring("cannot tell a first run from an eviction")))
			Expect(buf.String()).To(ContainSubstring("Performance baseline state missing"))
		})

		// The first run after the state moved out of the baseline tree finds the
		// new location empty and the old one populated. Failing that run would
		// fail every existing cache scope once; discarding the old state would
		// throw away the audit history.
		It("carries the state forward from the pre-split location", func() {
			legacy := cfg(tmp, cacheDir)
			Expect(checkBaseline(legacy, os.Stdout)).To(Succeed())
			Expect(filepath.Join(cacheDir, "baseline-state.json")).To(BeAnExistingFile())

			split := cfg(tmp, cacheDir)
			split.allowSeed = false
			split.stateDir = filepath.Join(tmp, "baseline-state")
			split.cacheHit = "Linux-perf-benchmark-Drift Performance--run-8999-1"
			var buf strings.Builder
			err := checkBaseline(split, &buf)
			// The old state says these keys were gated and the history is still
			// absent, so this is the MISSING case rather than the eviction case.
			Expect(err).To(MatchError(ContainSubstring("baseline missing")))
			Expect(buf.String()).To(ContainSubstring("Carrying the baseline audit state forward"))
			Expect(err.Error()).ToNot(ContainSubstring("cannot tell a first run from an eviction"))
		})

		It("allows a matched cache once the state file is present", func() {
			c := cfg(tmp, cacheDir)
			c.stateDir = filepath.Join(tmp, "baseline-state")
			Expect(checkBaseline(c, os.Stdout)).To(Succeed())
			seedBaselineHistory(tmp, cacheDir, prefix)
			c.cacheHit = "Linux-perf-benchmark-Drift Performance--run-8999-1"
			Expect(checkBaseline(c, os.Stdout)).To(Succeed())
		})
	})

	Describe("prettifyTestName", func() {
		DescribeTable("converts snake_case_performance_report.json filenames to Title Case",
			func(in, want string) {
				Expect(prettifyTestName(in)).To(Equal(want))
			},
			Entry("host name spreading", "host_name_spreading_performance_report.json", "Host Name Spreading"),
			Entry("basic deployment", "basic_deployment_performance_report.json", "Basic Deployment"),
		)
	})
})

// --- helpers ---

// syntheticRegistry registers exactly the fields seedSyntheticIterations
// produces. A gateable field that is registered and then absent from the batch
// is an error, so the two have to agree.
func syntheticRegistry() *gateRegistry {
	return mustParseRegistry(`
_gate  cpu_core_cap  2.0

test_a total_time                         1.10
test_a total_nodes                        1.05
test_a total_reserved_cpu_utilization     1.05
test_a resource_efficiency_score          none   # derived from the two utilizations
test_a total_reserved_memory_utilization  1.05

test_b karpenter_p95_cpu_cores            1.20
test_b total_nodes                        1.05
`)
}

func mustParseRegistry(body string) *gateRegistry {
	reg, err := parseRegistry("test-registry.txt", strings.NewReader(body))
	Expect(err).ToNot(HaveOccurred())
	return reg
}

// allResultFiles lists every benchmark-action file run emits.
func allResultFiles() []string {
	out := []string{resultsFileFor(observedSmallerSlug), resultsFileFor(observedBiggerSlug), resultsFileFor(cvSlug)}
	for _, m := range gatedMetrics() {
		out = append(out, resultsFileFor(m.slug))
	}
	return out
}

func entryNames(entries []benchmarkEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

// seedSyntheticIterations lays down iter_1..iter_N per-test performance
// reports whose values are chosen so median/mean/min/max are trivial to
// check in the assertions above.
func seedSyntheticIterations(root string, iters int) {
	for i := 1; i <= iters; i++ {
		iterDir := filepath.Join(root, "iter_"+strconv.Itoa(i))
		Expect(os.MkdirAll(iterDir, 0o755)).To(Succeed())
		// Test A: total_time > 1e9 to force the ns->s conversion path.
		// Values 2e9,3e9,4e9 -> 2,3,4 seconds; median = 3.
		writeReport(filepath.Join(iterDir, "test_a_performance_report.json"), map[string]any{
			"total_time":                        float64(i+1) * 1e9,
			"total_nodes":                       10 + i,
			"total_reserved_cpu_utilization":    0.5 + float64(i)*0.1,
			"resource_efficiency_score":         70 + float64(i),
			"total_reserved_memory_utilization": 0.6 + float64(i)*0.05,
			"rounds":                            i,
		})
		// Test B: karpenter_p95_cpu_cores = i*0.1 -> 0.1, 0.2, 0.3; median = 0.2.
		// No conversion applied, types.go emits cores directly.
		writeReport(filepath.Join(iterDir, "test_b_performance_report.json"), map[string]any{
			"karpenter_p95_cpu_cores": float64(i) * 0.1,
			"total_nodes":             float64(20 + i),
		})
	}
}

// seedBaselineHistory writes, for every gate step, a benchmark-action history
// file whose latest entry carries exactly the keys the current run emitted.
// That is the state a healthy second run restores from cache.
func seedBaselineHistory(outputDir, cacheDir, namePrefix string) {
	for _, t := range gateTiers() {
		names, err := readEntryNames(filepath.Join(outputDir, t.resultsFile))
		Expect(err).ToNot(HaveOccurred())
		benches := make([]map[string]any, 0, len(names))
		for _, n := range names {
			benches = append(benches, map[string]any{"name": n, "value": 1.0, "unit": "x"})
		}
		hist := map[string]any{"entries": map[string]any{
			namePrefix + t.nameSuffix: []map[string]any{{"benches": benches}},
		}}
		dir := filepath.Join(cacheDir, t.cacheSubdir)
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		b, err := json.MarshalIndent(hist, "", "  ")
		Expect(err).ToNot(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(dir, "benchmark-data.json"), b, 0o600)).To(Succeed())
	}
}

func writeReport(path string, data map[string]any) {
	b, err := json.MarshalIndent(data, "", "  ")
	Expect(err).ToNot(HaveOccurred())
	Expect(os.WriteFile(path, b, 0o600)).To(Succeed())
}

func loadEntries(path string) []benchmarkEntry {
	var out []benchmarkEntry
	loadJSON(path, &out)
	return out
}

func loadJSON(path string, v any) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: test-controlled tempdir path
	Expect(err).ToNot(HaveOccurred())
	Expect(json.Unmarshal(b, v)).To(Succeed())
}
