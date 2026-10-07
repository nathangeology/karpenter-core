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
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

// TestValidateConsolidationMeasurement covers the measurement-validity gate on
// consolidation reports. It runs without a cluster, so it is a plain Go test
// rather than a spec in the Ginkgo suite that TestIntegration starts.
//
// The first case is the one observed in CI: a run that removed 45 nodes while
// monitorConsolidationRounds polled past every drain, closing its window at the
// three-minute floor. Before the gate, that report passed a 0.30-core CPU
// threshold on an average that covered none of the disruption.
func TestValidateConsolidationMeasurement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report PerformanceReport
		reject bool
	}{
		{
			name: "nodes removed with no round observed",
			report: PerformanceReport{
				Rounds:              0,
				NodesNetChange:      -45,
				ConsolidationWindow: 199 * time.Second,
				MetricsSampleCount:  40,
			},
			reject: true,
		},
		{
			name: "nodes removed with rounds observed",
			report: PerformanceReport{
				Rounds:              8,
				NodesNetChange:      -52,
				ConsolidationWindow: 1089 * time.Second,
				MetricsSampleCount:  218,
			},
		},
		{
			name: "no nodes removed, so there was no consolidation to observe",
			report: PerformanceReport{
				Rounds:              0,
				NodesNetChange:      0,
				ConsolidationWindow: 199 * time.Second,
				MetricsSampleCount:  40,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			err := validateConsolidationMeasurement(&tc.report)
			if !tc.reject {
				g.Expect(err).To(Succeed())
				return
			}
			// The message has to carry the numbers a reader needs to tell this
			// apart from a consolidation that genuinely failed.
			g.Expect(err).To(MatchError(ContainSubstring("while 45 nodes were removed")))
			g.Expect(err).To(MatchError(ContainSubstring("3m19s window")))
			g.Expect(err).To(MatchError(ContainSubstring("40 resource samples")))
		})
	}
}
