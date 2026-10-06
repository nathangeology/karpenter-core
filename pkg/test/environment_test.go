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

package test

import (
	"testing"
)

// TestEnvironmentVersionReportsStartedAPIServer guards the contract that version-gated specs rely
// on: Environment.Version describes the apiserver that envtest started, not the K8S_VERSION the
// suite asked for. The two differ whenever KUBEBUILDER_ASSETS resolves to another minor, and a spec
// that skips on the declared version then asserts against a server that does not serve the field
// under test, which surfaces as a product failure rather than a missing control plane.
func TestEnvironmentVersionReportsStartedAPIServer(t *testing.T) {
	env := NewEnvironment()
	defer func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stopping environment: %v", err)
		}
	}()

	serverVersion, err := env.KubernetesInterface.Discovery().ServerVersion()
	if err != nil {
		t.Fatalf("discovering server version: %v", err)
	}
	if got, want := env.Version.String(), serverVersion.GitVersion; "v"+got != want && got != want {
		t.Errorf("Environment.Version = %q, started apiserver reports %q", got, want)
	}
}
