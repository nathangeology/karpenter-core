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

import "math"

// PhaseStats holds Normal-Gamma conjugate prior sufficient statistics.
type PhaseStats struct {
	Mu    float64 `json:"mu"`
	Kappa float64 `json:"kappa"`
	Alpha float64 `json:"alpha"`
	Beta  float64 `json:"beta"`
	N     int     `json:"n"`
}

// NewPhaseStats returns a moderately informative prior.
// kappa=20 means ~20 observations to wash out the prior.
func NewPhaseStats() PhaseStats {
	return PhaseStats{Mu: 0, Kappa: 20, Alpha: 10, Beta: 10, N: 0}
}

// Update performs a single Normal-Gamma Bayesian update with observation x.
func (s *PhaseStats) Update(x float64) {
	kappaNew := s.Kappa + 1
	diff := x - s.Mu
	s.Beta += (s.Kappa * diff * diff) / (2 * kappaNew)
	s.Mu = (s.Kappa*s.Mu + x) / kappaNew
	s.Kappa = kappaNew
	s.Alpha += 0.5
	s.N++
}

// Sigma returns the posterior standard deviation estimate.
func (s *PhaseStats) Sigma() float64 {
	if s.Alpha <= 1 {
		return 0
	}
	return math.Sqrt(s.Beta / (s.Alpha - 1))
}
