//go:build !perf_inject

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

// Package perfinject holds synthetic performance-regression injections used to
// calibrate the minimum detectable effect of the kind performance e2e. It is
// EXPERIMENT-ONLY CODE and must never reach a release build.
//
// This file is the production variant. It is selected whenever the perf_inject
// build tag is absent, which is every build that is not an explicit calibration
// build. Both hooks are empty, take no arguments the compiler cannot discard,
// and are inlined away, so a normal binary contains no injection logic, no
// environment-variable names, and no reference to time.Sleep on these paths.
//
// The proof of that claim is mechanical rather than asserted: compare
//
//	go build -o /tmp/off  ./kwok && go tool nm /tmp/off  | grep perfinject
//	go build -o /tmp/on   -tags perf_inject ./kwok && go tool nm /tmp/on | grep perfinject
//
// The first command returns nothing.
package perfinject

// ScaleOut is the provisioning-path hook. No-op in a production build.
func ScaleOut() {}

// Consolidation is the disruption-path hook. No-op in a production build.
func Consolidation() {}
