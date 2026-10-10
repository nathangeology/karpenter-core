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

package disruption_test

import (
	"math"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clock "k8s.io/utils/clock/testing"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/test"
	disruptionutils "sigs.k8s.io/karpenter/pkg/utils/disruption"
)

// fakeClock drives the helpers that take a clock.Clock. They read it only
// through Since, so every spec sets up the input time relative to
// fakeClock.Now() rather than to wall time.
var fakeClock *clock.FakeClock

func TestDisruption(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Disruption")
}

var _ = BeforeEach(func() {
	fakeClock = clock.NewFakeClock(time.Now())
})

// expiringNodeClaim returns a NodeClaim that was created age ago on fakeClock's
// timeline and expires expireAfter after its creation. test.NodeClaim leaves
// ExpireAfter at its zero value, so every LifetimeRemaining spec that wants the
// non-nil branch has to set it. A negative age puts creation ahead of the clock.
//
// CreationTimestamp is assigned after the builder returns, not through the
// override: test.ObjectMeta ends with an unconditional
// `om.CreationTimestamp = metav1.Now()`, which discards anything passed in.
// Setting it through the override leaves every NodeClaim at age 0, where the
// clamp returns 1.0 and the whole table reads as passing by accident.
func expiringNodeClaim(expireAfter string, age time.Duration) *v1.NodeClaim {
	nc := test.NodeClaim(v1.NodeClaim{
		Spec: v1.NodeClaimSpec{ExpireAfter: v1.MustParseNillableDuration(expireAfter)},
	})
	nc.CreationTimestamp = metav1.Time{Time: fakeClock.Now().Add(-age)}
	return nc
}

var _ = Describe("LifetimeRemaining", func() {
	// The CRD pattern for expireAfter is ^(([0-9]+(s|m|h))+|Never)$, so a zero
	// duration is admissible and parses to a non-nil pointer at zero rather
	// than to the nil that "Never" produces. That puts it on the dividing
	// branch with totalLifetimeSeconds == 0, where the quotient is NaN at age
	// zero, -Inf once the node is older, and +Inf when the recorded creation
	// time leads the clock. lo.Clamp compares with < and >, both false for NaN,
	// so without a guard the function leaves its documented [0.0, 1.0] range on
	// one of those three and reports an expires-on-creation node as brand new
	// on another.
	DescribeTable("a zero expireAfter, which puts the division's denominator at zero",
		func(expireAfter string, age time.Duration) {
			nc := expiringNodeClaim(expireAfter, age)
			// Without this the spec is vacuous: a nil duration would take the
			// untouched branch and return 1.0 for a reason unrelated to the guard.
			Expect(nc.Spec.ExpireAfter.Duration).ToNot(BeNil(),
				"a zero expireAfter must parse to a non-nil pointer for this spec to exercise the guard")
			Expect(nc.Spec.ExpireAfter.Seconds()).To(BeZero())

			remaining := disruptionutils.LifetimeRemaining(fakeClock, test.NodePool(), nc)
			Expect(math.IsNaN(remaining)).To(BeFalse(), "a fraction outside [0.0, 1.0] breaks the documented contract")
			Expect(remaining).To(Equal(0.0), "a node that expires on creation has no lifetime left at any age")
		},
		// Age zero is the 0/0 case. It needs the clock to sit exactly on the
		// recorded creation instant, which a fake clock gives exactly and a
		// real one gives about never, so it is pinned for the contract rather
		// than for its reachability.
		Entry("at age zero, where the quotient is 0/0", "0s", time.Duration(0)),
		Entry("one nanosecond past creation", "0s", time.Nanosecond),
		Entry("an hour past creation", "0s", time.Hour),
		// A creation time ahead of the clock makes the numerator positive over
		// a zero denominator. That quotient clamps to 1.0, which reads as a
		// brand-new node, so this entry is the one that changes a plausible
		// answer rather than an unreachable one.
		Entry("with creation a second ahead of the clock", "0s", -time.Second),
		// The pattern admits every zero spelling, including repeated terms.
		Entry("spelled 0m", "0m", time.Hour),
		Entry("spelled 0h", "0h", time.Hour),
		Entry("spelled with repeated zero terms", "0h0m0s", time.Hour),
	)

	// Controls. The guard returns early for the whole zero branch, so these
	// pin that it did not swallow either of the two paths that were already
	// correct.
	It("leaves a nil expireAfter at 1.0, which is how Never parses", func() {
		nc := expiringNodeClaim("Never", time.Hour)
		Expect(nc.Spec.ExpireAfter.Duration).To(BeNil(), "Never must parse to a nil duration for this control to mean anything")
		Expect(disruptionutils.LifetimeRemaining(fakeClock, test.NodePool(), nc)).To(Equal(1.0))
	})
	DescribeTable("scales a non-zero expireAfter by the fraction of its lifetime left",
		func(age time.Duration, expected float64) {
			nc := expiringNodeClaim("24h", age)
			Expect(disruptionutils.LifetimeRemaining(fakeClock, test.NodePool(), nc)).To(BeNumerically("~", expected, 0.001))
		},
		Entry("at creation", time.Duration(0), 1.0),
		Entry("a quarter of the way through", 6*time.Hour, 0.75),
		Entry("past expiry, clamped", 25*time.Hour, 0.0),
	)
})
