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
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	clock "k8s.io/utils/clock/testing"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/fake"
	nodeclaimlifecycle "sigs.k8s.io/karpenter/pkg/controllers/nodeclaim/lifecycle"
	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/state/nodepoolhealth"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"

	"sigs.k8s.io/karpenter/pkg/apis"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type testHook struct {
	name string
	fn   func(context.Context, *v1.NodeClaim) (cloudprovider.NodeLifecycleHookResult, error)
}

func (h testHook) Name() string { return h.name }
func (h testHook) Registered(ctx context.Context, nc *v1.NodeClaim) (cloudprovider.NodeLifecycleHookResult, error) {
	return h.fn(ctx, nc)
}

var (
	ctx           context.Context
	env           *test.Environment
	fakeClock     *clock.FakeClock
	cloudProvider *fake.CloudProvider
)

func TestAspirationalsuite(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "Aspirational")
}

var _ = BeforeSuite(func() {
	fakeClock = clock.NewFakeClock(time.Now())
	env = test.NewEnvironment(test.WithCRDs(apis.CRDs...), test.WithCRDs(v1alpha1.CRDs...), test.WithFieldIndexers(test.NodeProviderIDFieldIndexer(ctx)))
	ctx = options.ToContext(ctx, test.Options())
	cloudProvider = fake.NewCloudProvider()
})

var _ = AfterSuite(func() {
	Expect(env.Stop()).To(Succeed())
})

var _ = AfterEach(func() {
	fakeClock.SetTime(time.Now())
	ExpectCleanedUp(ctx, env.Client)
	cloudProvider.Reset()
})

// These tests document the gap identified in PRs #2923/#2980: if a registration
// hook mutates the NodeClaim (e.g., adds a label) and then returns an error,
// the mutation persists on the NodeClaim across reconcile cycles. On the NEXT
// reconcile, syncNode propagates the stale label to the node — leaving the node
// with incorrect labels while registration remains incomplete.
//
// The key insight: the bug manifests across two reconcile cycles:
//   1. First reconcile: hook mutates NodeClaim, then errors. syncNode ran BEFORE
//      hooks so node doesn't get the stale label yet. But the outer controller
//      patches the NodeClaim with the stale label.
//   2. Second reconcile: NodeClaim now has stale label from API server. syncNode
//      propagates it to the node BEFORE hooks run. Node now has stale label.

var _ = Describe("RegistrationHookPartialFailure", func() {
	var nodePool *v1.NodePool

	BeforeEach(func() {
		nodePool = test.NodePool()
	})

	It("should not persist stale labels on the NodeClaim when a mutating hook fails", func() {
		// This is the root cause: a hook that mutates the NodeClaim pointer and then
		// returns an error causes the mutation to persist because the outer controller
		// patches the NodeClaim unconditionally after reconcile.
		hookController := nodeclaimlifecycle.NewController(fakeClock, env.Client, cloudProvider,
			events.NewRecorder(&record.FakeRecorder{}), nodepoolhealth.NewState(),
			[]cloudprovider.NodeLifecycleHook{
				testHook{
					name: "mutating-then-failing",
					fn: func(_ context.Context, nc *v1.NodeClaim) (cloudprovider.NodeLifecycleHookResult, error) {
						nc.Labels["test.karpenter.sh/partial-label"] = "stale-value"
						return cloudprovider.NodeLifecycleHookResult{}, fmt.Errorf("simulated cloud API failure after mutation")
					},
				},
			},
		)

		nodeClaim := test.NodeClaim(v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name},
			},
		})
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, hookController, nodeClaim)
		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)

		node := test.Node(test.NodeOptions{
			ProviderID: nodeClaim.Status.ProviderID,
			Taints:     []corev1.Taint{v1.UnregisteredNoExecuteTaint},
		})
		ExpectApplied(ctx, env.Client, node)

		// First reconcile: hook mutates then errors. The outer controller patches
		// the NodeClaim with the stale mutation.
		_ = ExpectObjectReconcileFailed(ctx, env.Client, hookController, nodeClaim)

		// ASPIRATIONAL: The stale label should NOT be persisted on the NodeClaim.
		// Currently, it IS persisted because the controller patches all in-memory changes.
		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.Labels).NotTo(HaveKey("test.karpenter.sh/partial-label"),
			"NodeClaim should not persist labels from a hook that failed — "+
				"currently fails because the controller patches mutations from erroring hooks")
	})

	It("should not propagate stale labels to the node across reconcile cycles", func() {
		// The more severe manifestation: stale label persists on NodeClaim, then
		// on the next reconcile syncNode propagates it to the node.
		hookController := nodeclaimlifecycle.NewController(fakeClock, env.Client, cloudProvider,
			events.NewRecorder(&record.FakeRecorder{}), nodepoolhealth.NewState(),
			[]cloudprovider.NodeLifecycleHook{
				testHook{
					name: "mutating-then-failing",
					fn: func(_ context.Context, nc *v1.NodeClaim) (cloudprovider.NodeLifecycleHookResult, error) {
						nc.Labels["test.karpenter.sh/leaked-label"] = "stale-across-reconciles"
						return cloudprovider.NodeLifecycleHookResult{}, fmt.Errorf("persistent cloud failure")
					},
				},
			},
		)

		nodeClaim := test.NodeClaim(v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name},
			},
		})
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, hookController, nodeClaim)
		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)

		node := test.Node(test.NodeOptions{
			ProviderID: nodeClaim.Status.ProviderID,
			Taints:     []corev1.Taint{v1.UnregisteredNoExecuteTaint},
		})
		ExpectApplied(ctx, env.Client, node)

		// First reconcile: hook mutates and errors
		_ = ExpectObjectReconcileFailed(ctx, env.Client, hookController, nodeClaim)

		// Second reconcile: syncNode runs with the now-persisted stale label
		_ = ExpectObjectReconcileFailed(ctx, env.Client, hookController, nodeClaim)

		// ASPIRATIONAL: The stale label should NOT appear on the node.
		// Currently it DOES because syncNode propagates all NodeClaim labels to node.
		node = ExpectExists(ctx, env.Client, node)
		Expect(node.Labels).NotTo(HaveKey("test.karpenter.sh/leaked-label"),
			"Node should not have stale labels leaked from a repeatedly-failing hook — "+
				"currently fails because syncNode propagates mutations that were persisted from erroring hooks")
	})

	It("should converge to correct labels after transient hook failure with mutation", func() {
		// Tests the recovery path: hook fails with stale mutation on first try,
		// then succeeds with correct value. The node should have ONLY the correct value.
		var callCount atomic.Int32
		hookController := nodeclaimlifecycle.NewController(fakeClock, env.Client, cloudProvider,
			events.NewRecorder(&record.FakeRecorder{}), nodepoolhealth.NewState(),
			[]cloudprovider.NodeLifecycleHook{
				testHook{
					name: "eventually-consistent-hook",
					fn: func(_ context.Context, nc *v1.NodeClaim) (cloudprovider.NodeLifecycleHookResult, error) {
						count := callCount.Add(1)
						if count <= 2 {
							nc.Labels["test.karpenter.sh/hook-value"] = "stale-incorrect"
							return cloudprovider.NodeLifecycleHookResult{}, fmt.Errorf("transient failure")
						}
						nc.Labels["test.karpenter.sh/hook-value"] = "correct-final"
						return cloudprovider.NodeLifecycleHookResult{}, nil
					},
				},
			},
		)

		nodeClaim := test.NodeClaim(v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name},
			},
		})
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, hookController, nodeClaim)
		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)

		node := test.Node(test.NodeOptions{
			ProviderID: nodeClaim.Status.ProviderID,
			Taints:     []corev1.Taint{v1.UnregisteredNoExecuteTaint},
		})
		ExpectApplied(ctx, env.Client, node)

		// Two failed reconciles (stale mutations persist)
		_ = ExpectObjectReconcileFailed(ctx, env.Client, hookController, nodeClaim)
		_ = ExpectObjectReconcileFailed(ctx, env.Client, hookController, nodeClaim)

		// Third reconcile succeeds
		ExpectObjectReconciled(ctx, env.Client, hookController, nodeClaim)

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.StatusConditions().Get(v1.ConditionTypeRegistered).IsTrue()).To(BeTrue(),
			"Registration should complete on successful retry")

		node = ExpectExists(ctx, env.Client, node)
		Expect(node.Labels).To(HaveKeyWithValue("test.karpenter.sh/hook-value", "correct-final"),
			"Node should have the correct final value after recovery")

		// ASPIRATIONAL: Verify the node NEVER had the stale value visible.
		// In practice, the stale value was on the node during the second failed reconcile.
		// This test passes because the final state is correct — but the intermediate
		// inconsistency window existed. A proper fix would prevent the stale mutation
		// from ever being persisted.
	})

	It("should not have ConditionTypeRegistered=True while node has stale labels from partial failure", func() {
		// Tests the invariant: there should never be a window where a node has
		// stale labels AND ConditionTypeRegistered=True simultaneously.
		// This invariant holds in the current implementation because registration
		// only completes when all hooks pass. The real risk is nodes with stale
		// labels + unregistered taint being used by other controllers that don't
		// check the taint.
		hookController := nodeclaimlifecycle.NewController(fakeClock, env.Client, cloudProvider,
			events.NewRecorder(&record.FakeRecorder{}), nodepoolhealth.NewState(),
			[]cloudprovider.NodeLifecycleHook{
				testHook{
					name: "mutating-then-failing",
					fn: func(_ context.Context, nc *v1.NodeClaim) (cloudprovider.NodeLifecycleHookResult, error) {
						nc.Labels["test.karpenter.sh/status-label"] = "stale"
						return cloudprovider.NodeLifecycleHookResult{}, fmt.Errorf("hook failure")
					},
				},
			},
		)

		nodeClaim := test.NodeClaim(v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name},
			},
		})
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, hookController, nodeClaim)
		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)

		node := test.Node(test.NodeOptions{
			ProviderID: nodeClaim.Status.ProviderID,
			Taints:     []corev1.Taint{v1.UnregisteredNoExecuteTaint},
		})
		ExpectApplied(ctx, env.Client, node)

		// Multiple reconcile cycles with persistent failure
		_ = ExpectObjectReconcileFailed(ctx, env.Client, hookController, nodeClaim)
		_ = ExpectObjectReconcileFailed(ctx, env.Client, hookController, nodeClaim)

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		node = ExpectExists(ctx, env.Client, node)

		// This invariant HOLDS: registration never completes when hooks fail
		Expect(nodeClaim.StatusConditions().Get(v1.ConditionTypeRegistered).IsTrue()).To(BeFalse())

		// The unregistered taint is still present
		_, hasUnregisteredTaint := lo.Find(node.Spec.Taints, func(t corev1.Taint) bool {
			return t.MatchTaint(&v1.UnregisteredNoExecuteTaint)
		})
		Expect(hasUnregisteredTaint).To(BeTrue())

		// ASPIRATIONAL: The stale label should not be on the node AT ALL.
		// Currently it IS there after the second reconcile because syncNode propagated it.
		Expect(node.Labels).NotTo(HaveKey("test.karpenter.sh/status-label"),
			"Stale labels from failed hooks should not appear on unregistered nodes — "+
				"currently fails because syncNode propagates mutations from prior failed hooks")
	})

	It("should handle multi-hook scenario where hook-2 fails after hook-1 mutates", func() {
		// Hook 1 succeeds and mutates a label. Hook 2 mutates a different label then errors.
		// Since hooks run in parallel, BOTH mutations apply to the in-memory NodeClaim.
		// After the error, hook-1's mutation is valid but hook-2's is not.
		hookController := nodeclaimlifecycle.NewController(fakeClock, env.Client, cloudProvider,
			events.NewRecorder(&record.FakeRecorder{}), nodepoolhealth.NewState(),
			[]cloudprovider.NodeLifecycleHook{
				testHook{
					name: "hook-one-succeeds",
					fn: func(_ context.Context, nc *v1.NodeClaim) (cloudprovider.NodeLifecycleHookResult, error) {
						nc.Labels["test.karpenter.sh/hook-one"] = "valid-from-passing-hook"
						return cloudprovider.NodeLifecycleHookResult{}, nil
					},
				},
				testHook{
					name: "hook-two-fails",
					fn: func(_ context.Context, nc *v1.NodeClaim) (cloudprovider.NodeLifecycleHookResult, error) {
						nc.Labels["test.karpenter.sh/hook-two"] = "stale-from-failed-hook"
						return cloudprovider.NodeLifecycleHookResult{}, fmt.Errorf("hook-two cloud operation failed")
					},
				},
			},
		)

		nodeClaim := test.NodeClaim(v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name},
			},
		})
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, hookController, nodeClaim)
		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)

		node := test.Node(test.NodeOptions{
			ProviderID: nodeClaim.Status.ProviderID,
			Taints:     []corev1.Taint{v1.UnregisteredNoExecuteTaint},
		})
		ExpectApplied(ctx, env.Client, node)

		// First reconcile with failure
		_ = ExpectObjectReconcileFailed(ctx, env.Client, hookController, nodeClaim)
		// Second reconcile: syncNode propagates persisted labels
		_ = ExpectObjectReconcileFailed(ctx, env.Client, hookController, nodeClaim)

		node = ExpectExists(ctx, env.Client, node)
		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)

		// Registration must not complete
		Expect(nodeClaim.StatusConditions().Get(v1.ConditionTypeRegistered).IsTrue()).To(BeFalse())

		// ASPIRATIONAL: Only hook-one's label (from the passing hook) should persist.
		// Hook-two's stale label should be rolled back or never persisted.
		// Currently BOTH labels persist because the controller doesn't distinguish
		// between mutations from passing vs failing hooks.
		Expect(node.Labels).NotTo(HaveKeyWithValue("test.karpenter.sh/hook-two", "stale-from-failed-hook"),
			"Labels from failing hooks should not persist on the node — "+
				"currently fails because all hook mutations are persisted regardless of hook success/failure")
	})
})
