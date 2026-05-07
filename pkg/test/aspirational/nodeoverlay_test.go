//go:build test_aspirational

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

package aspirational

import (
	"testing"
)

// TestNodeOverlayController_CascadingFailureIsolation documents the scenario from
// https://github.com/kubernetes-sigs/karpenter/pull/3002
//
// When one NodePool references a missing or broken NodeClass, the entire
// nodeoverlay controller reconcile loop fails, blocking overlay updates for
// ALL NodePools — including healthy ones. The desired behavior is per-NodePool
// error isolation: failures in one NodePool should not block processing of others.
//
// Currently, GetInstanceTypes returns an error for the broken NodePool and the
// reconcile loop returns early. This prevents overlays from being applied to
// any healthy NodePool until the broken one is fixed.
//
// This test will pass once the controller skips failing NodePools and continues
// processing healthy ones.
func TestNodeOverlayController_CascadingFailureIsolation(t *testing.T) {
	t.Skip("aspirational: blocked on nodeoverlay controller per-NodePool error isolation (#3002)")
}

// TestNodeOverlayController_RecoveryAfterNodeClassCreated verifies that after a
// previously missing NodeClass is created, the affected NodePool's overlays
// start being applied without requiring a Karpenter restart.
//
// This is the recovery variant of the cascading failure isolation scenario.
// The controller should eventually notice the NodeClass exists and begin
// processing its overlays.
func TestNodeOverlayController_RecoveryAfterNodeClassCreated(t *testing.T) {
	t.Skip("aspirational: blocked on nodeoverlay controller per-NodePool error isolation (#3002)")
}
