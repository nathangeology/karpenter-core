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

// Tests in this file assert the scoring helpers' own contracts: what each one
// returns for a missing, malformed, or boundary input. The consolidation
// behavior built on top of them is exercised in
// pkg/controllers/disruption/suite_test.go ("Pod Eviction Cost") and
// pkg/controllers/disruption/balanced_scoring_test.go.

package disruption_test

import (
	"context"
	"math"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clock "k8s.io/utils/clock/testing"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/test"
	disruptionutils "sigs.k8s.io/karpenter/pkg/utils/disruption"
)

// ctx carries default Options, so PodDeletionCostManagement is its declared
// default of false. Specs that need the gate on build their own context with
// gateOnContext.
var ctx context.Context

// fakeClock drives the two helpers that take a clock.Clock. Both read it only
// through Since, so every spec sets up the input time relative to
// fakeClock.Now() rather than to wall time.
var fakeClock *clock.FakeClock

func TestDisruption(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Disruption")
}

var _ = BeforeEach(func() {
	ctx = options.ToContext(context.Background(), test.Options())
	fakeClock = clock.NewFakeClock(time.Now())
})

// instanceTypeWithOffering builds an InstanceType carrying exactly one
// on-demand, test-zone-1 Offering, mirroring makeOffering in
// pkg/controllers/disruption/balanced_scoring_test.go. Specs reach the
// no-matching-offering paths by varying the labels they resolve against, not
// the offering.
func instanceTypeWithOffering(price float64) *cloudprovider.InstanceType {
	return &cloudprovider.InstanceType{
		Name: "test-instance-type",
		Offerings: cloudprovider.Offerings{{
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "test-zone-1"),
				scheduling.NewRequirement(v1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, v1.CapacityTypeOnDemand),
			),
			Price:     price,
			Available: true,
		}},
	}
}

func podWithAnnotations(annotations map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: annotations}}
}

// gateOnContext returns a context whose Options enable PodDeletionCostManagement.
func gateOnContext() context.Context {
	return options.ToContext(context.Background(), test.Options(test.OptionsFields{
		FeatureGates: test.FeatureGates{PodDeletionCostManagement: lo.ToPtr(true)},
	}))
}

// expiringNodeClaim returns a NodeClaim that was created age ago on fakeClock's
// timeline and expires expireAfter after its creation. test.NodeClaim leaves
// ExpireAfter at its zero value, so every LifetimeRemaining spec that wants the
// non-nil branch has to set it.
//
// CreationTimestamp is assigned after the builder returns, not through the
// override: test.ObjectMeta ends with an unconditional
// `om.CreationTimestamp = metav1.Now()` (pkg/test/metadata.go:59), which
// discards anything passed in. Setting it through the override leaves every
// NodeClaim at age 0, where the clamp returns 1.0 and the whole table reads as
// passing-by-accident.
func expiringNodeClaim(expireAfter string, age time.Duration) *v1.NodeClaim {
	nc := test.NodeClaim(v1.NodeClaim{
		Spec: v1.NodeClaimSpec{ExpireAfter: v1.MustParseNillableDuration(expireAfter)},
	})
	nc.CreationTimestamp = metav1.Time{Time: fakeClock.Now().Add(-age)}
	return nc
}

// initializedNodeClaim returns a NodeClaim whose Initialized condition is true,
// with lastPodEvent set lastPodEventAge ago on fakeClock's timeline. A zero
// lastPodEventAge is not the same as a zero LastPodEventTime: pass
// withZeroLastPodEvent for the fallback path.
func initializedNodeClaim(lastPodEventAge time.Duration) *v1.NodeClaim {
	nc := test.NodeClaim()
	nc.StatusConditions().SetTrue(v1.ConditionTypeInitialized)
	nc.Status.LastPodEventTime.Time = fakeClock.Now().Add(-lastPodEventAge)
	return nc
}

// withZeroLastPodEvent zeroes LastPodEventTime and parks fakeClock at the
// Initialized condition's own transition time, so Since(fallback) starts at 0.
// SetTrue stamps that time from wall time rather than from the fake clock, which
// is why it is read back rather than assumed.
func withZeroLastPodEvent(nc *v1.NodeClaim) *v1.NodeClaim {
	nc.Status.LastPodEventTime.Time = time.Time{}
	fakeClock.SetTime(nc.StatusConditions().Get(v1.ConditionTypeInitialized).LastTransitionTime.Time)
	return nc
}

// consolidateAfterNodePool returns a NodePool with ConsolidateAfter set.
// test.NodePool leaves it nil, which is the "Never" shape.
func consolidateAfterNodePool(consolidateAfter string) *v1.NodePool {
	np := test.NodePool()
	np.Spec.Disruption.ConsolidateAfter = v1.MustParseNillableDuration(consolidateAfter)
	return np
}

var _ = Describe("ResolveOfferingPrice", func() {
	// Every failure mode returns 0, the same value a genuinely free offering
	// returns, so these entries pin which inputs are silently indistinguishable
	// from a zero price.
	DescribeTable("resolves a price from the node's labels",
		func(labels map[string]string, it *cloudprovider.InstanceType, expected float64) {
			Expect(disruptionutils.ResolveOfferingPrice(labels, it)).To(BeNumerically("==", expected))
		},
		Entry("returns 0 for a nil instance type",
			map[string]string{corev1.LabelTopologyZone: "test-zone-1", v1.CapacityTypeLabelKey: v1.CapacityTypeOnDemand},
			nil, 0.0),
		Entry("returns 0 for nil labels",
			nil,
			instanceTypeWithOffering(4.84), 0.0),
		Entry("returns 0 for an empty label map",
			map[string]string{},
			instanceTypeWithOffering(4.84), 0.0),
		Entry("returns 0 when the zone label is missing",
			map[string]string{v1.CapacityTypeLabelKey: v1.CapacityTypeOnDemand},
			instanceTypeWithOffering(4.84), 0.0),
		Entry("returns 0 when the capacity type label is missing",
			map[string]string{corev1.LabelTopologyZone: "test-zone-1"},
			instanceTypeWithOffering(4.84), 0.0),
		Entry("returns 0 when the zone label does not match any offering",
			map[string]string{corev1.LabelTopologyZone: "test-zone-2", v1.CapacityTypeLabelKey: v1.CapacityTypeOnDemand},
			instanceTypeWithOffering(4.84), 0.0),
		Entry("returns 0 when the capacity type label does not match any offering",
			map[string]string{corev1.LabelTopologyZone: "test-zone-1", v1.CapacityTypeLabelKey: v1.CapacityTypeSpot},
			instanceTypeWithOffering(4.84), 0.0),
		Entry("returns 0 for a NaN price so the ratio stays orderable",
			map[string]string{corev1.LabelTopologyZone: "test-zone-1", v1.CapacityTypeLabelKey: v1.CapacityTypeOnDemand},
			instanceTypeWithOffering(math.NaN()), 0.0),
		Entry("returns the offering price on a full match",
			map[string]string{corev1.LabelTopologyZone: "test-zone-1", v1.CapacityTypeLabelKey: v1.CapacityTypeOnDemand},
			instanceTypeWithOffering(4.84), 4.84),
		Entry("returns 0 for a zero-priced offering that did match",
			map[string]string{corev1.LabelTopologyZone: "test-zone-1", v1.CapacityTypeLabelKey: v1.CapacityTypeOnDemand},
			instanceTypeWithOffering(0.0), 0.0),
	)
})

var _ = Describe("SavingsRatio", func() {
	It("panics on a zero denominator, which only a caller bypassing ComputeRescheduleDisruptionCost can produce", func() {
		Expect(func() { disruptionutils.SavingsRatio(4.84, 0.0) }).To(Panic())
	})
	It("returns 0 for a zero price", func() {
		Expect(disruptionutils.SavingsRatio(0.0, 4.0)).To(BeNumerically("==", 0.0))
	})
	It("returns price over cost", func() {
		Expect(disruptionutils.SavingsRatio(4.84, 4.0)).To(BeNumerically("~", 1.21, 0.001))
	})
	It("returns a negative ratio for a negative price rather than clamping", func() {
		Expect(disruptionutils.SavingsRatio(-2.0, 1.0)).To(BeNumerically("==", -2.0))
	})
})

var _ = Describe("ComputeRescheduleDisruptionCost", func() {
	It("returns the per-node base for no pods", func() {
		Expect(disruptionutils.ComputeRescheduleDisruptionCost(ctx, nil)).
			To(BeNumerically("==", disruptionutils.PerNodeBaseDisruptionCost))
	})
	It("adds one unit per unannotated pod on top of the base", func() {
		pods := []*corev1.Pod{{}, {}, {}}
		Expect(disruptionutils.ComputeRescheduleDisruptionCost(ctx, pods)).
			To(BeNumerically("~", disruptionutils.PerNodeBaseDisruptionCost+3.0, 0.001))
	})
	It("floors a negative pod cost at 0 so one pod cannot discount the base", func() {
		// -2000000000 drives EvictionCost to the -10 clamp. Floored at 0, the
		// node's cost is exactly the base, not 1.0 + (-10.0).
		pods := []*corev1.Pod{podWithAnnotations(map[string]string{corev1.PodDeletionCost: "-2000000000"})}
		Expect(disruptionutils.ComputeRescheduleDisruptionCost(ctx, pods)).
			To(BeNumerically("==", disruptionutils.PerNodeBaseDisruptionCost))
	})
	It("panics when the context carries no Options, which is the caller's contract", func() {
		Expect(func() {
			_ = disruptionutils.ComputeRescheduleDisruptionCost(context.Background(), []*corev1.Pod{{}})
		}).To(Panic())
	})
})

var _ = Describe("EvictionCost", func() {
	const defaultCost = 1.0

	Context("unparseable annotations", func() {
		DescribeTable("falls back to the default cost when karpenter.sh/disruption-cost does not parse as an int32",
			func(annotation string) {
				p := podWithAnnotations(map[string]string{v1.DisruptionCostAnnotationKey: annotation})
				Expect(disruptionutils.EvictionCost(ctx, p)).To(BeNumerically("==", defaultCost))
			},
			Entry("non-numeric", "not-a-number"),
			Entry("overflows int32", "99999999999"),
			Entry("float", "1.5"),
			Entry("hex, because ParseInt is called with base 10", "0xff"),
			Entry("present but empty", ""),
			Entry("whitespace-padded", " 100 "),
		)
		It("does not fall through to pod-deletion-cost when disruption-cost is present but unparseable", func() {
			// The two reads are an if / else if, so a present-but-malformed
			// primary annotation suppresses the fallback rather than deferring
			// to it. Gate is off here, which is the only case the fallback runs.
			p := podWithAnnotations(map[string]string{
				v1.DisruptionCostAnnotationKey: "not-a-number",
				corev1.PodDeletionCost:         "2000000000",
			})
			Expect(disruptionutils.EvictionCost(ctx, p)).To(BeNumerically("==", defaultCost),
				"a malformed disruption-cost must not hand the decision to pod-deletion-cost")
		})
		It("falls back to the default cost when pod-deletion-cost does not parse and the gate is off", func() {
			p := podWithAnnotations(map[string]string{corev1.PodDeletionCost: "garbage"})
			Expect(disruptionutils.EvictionCost(ctx, p)).To(BeNumerically("==", defaultCost))
		})
		It("never reaches the pod-deletion-cost parse when the gate is on", func() {
			// Distinct from the gate=ON spec in
			// pkg/controllers/disruption/suite_test.go, which feeds a parseable
			// value: an unparseable one proves the branch is skipped rather than
			// parsed and discarded.
			p := podWithAnnotations(map[string]string{corev1.PodDeletionCost: "garbage"})
			Expect(disruptionutils.EvictionCost(gateOnContext(), p)).To(BeNumerically("==", defaultCost))
		})
	})

	Context("int32 annotation boundaries", func() {
		// The annotation band is 2^27, so a full-scale int32 overshoots the
		// +/-10 clamp by roughly 6 units in either direction.
		DescribeTable("clamps the cost to [-10, 10]",
			func(annotation string, expected float64) {
				p := podWithAnnotations(map[string]string{v1.DisruptionCostAnnotationKey: annotation})
				Expect(disruptionutils.EvictionCost(ctx, p)).To(BeNumerically("~", expected, 0.001))
			},
			Entry("max int32 clamps to the ceiling", "2147483647", 10.0),
			Entry("min int32 clamps to the floor", "-2147483648", -10.0),
			Entry("one full band is one unit above the default, below the clamp", "134217728", 2.0),
			Entry("one negative band is one unit below the default, above the clamp", "-134217728", 0.0),
			Entry("zero is the default cost", "0", 1.0),
		)
	})
})

// ReschedulingCost and ComputeRescheduleDisruptionCost both sum EvictionCost
// over a pod list and differ in two ways that no spec pinned before: this one
// has no per-node base and no per-pod floor. Both differences are live, because
// pkg/controllers/disruption/types.go:221 and
// pkg/controllers/static/deprovisioning/controller.go:302-303 call this one, not
// the floored one.
var _ = Describe("ReschedulingCost", func() {
	It("returns 0 for no pods, where ComputeRescheduleDisruptionCost returns the per-node base", func() {
		Expect(disruptionutils.ReschedulingCost(ctx, nil)).To(BeNumerically("==", 0.0))
		Expect(disruptionutils.ComputeRescheduleDisruptionCost(ctx, nil)).
			To(BeNumerically("==", disruptionutils.PerNodeBaseDisruptionCost))
	})
	It("sums one unit per unannotated pod with no base added", func() {
		pods := []*corev1.Pod{{}, {}, {}}
		Expect(disruptionutils.ReschedulingCost(ctx, pods)).To(BeNumerically("~", 3.0, 0.001))
	})
	It("returns a negative cost for a pod at the -10 clamp rather than flooring at 0", func() {
		// The floored sibling returns exactly the base for this input. Here the
		// -10 reaches the caller, so Candidate.DisruptionCost can go negative.
		pods := []*corev1.Pod{podWithAnnotations(map[string]string{corev1.PodDeletionCost: "-2000000000"})}
		Expect(disruptionutils.ReschedulingCost(ctx, pods)).To(BeNumerically("~", -10.0, 0.001))
		Expect(disruptionutils.ComputeRescheduleDisruptionCost(ctx, pods)).
			To(BeNumerically("==", disruptionutils.PerNodeBaseDisruptionCost))
	})
	It("lets a cheap pod cancel an expensive one, because the floor is absent at the pod level", func() {
		// Floored per pod this would be 10.0. Summed unfloored it is 0, so the
		// absent floor changes the ranking rather than only the magnitude.
		pods := []*corev1.Pod{
			podWithAnnotations(map[string]string{corev1.PodDeletionCost: "2000000000"}),
			podWithAnnotations(map[string]string{corev1.PodDeletionCost: "-2000000000"}),
		}
		Expect(disruptionutils.ReschedulingCost(ctx, pods)).To(BeNumerically("~", 0.0, 0.001))
	})
	It("panics when the context carries no Options, the same contract EvictionCost imposes", func() {
		Expect(func() {
			_ = disruptionutils.ReschedulingCost(context.Background(), []*corev1.Pod{{}})
		}).To(Panic())
	})
})

var _ = Describe("LifetimeRemaining", func() {
	// The nil branch returns 1.0 without touching the clock. It is also the only
	// thing standing between a NodeClaim with no ExpireAfter and a nil
	// dereference: NillableDuration embeds *time.Duration, so the promoted
	// Seconds() panics on a nil inner pointer.
	Context("a nil ExpireAfter duration", func() {
		It("returns 1.0 for a NodeClaim with no ExpireAfter set", func() {
			Expect(disruptionutils.LifetimeRemaining(fakeClock, test.NodePool(), test.NodeClaim())).
				To(BeNumerically("==", 1.0))
		})
		It("returns 1.0 for ExpireAfter: Never", func() {
			nc := expiringNodeClaim("Never", 30*time.Minute)
			Expect(nc.Spec.ExpireAfter.Duration).To(BeNil(), "Never must parse to a nil duration for this spec to mean anything")
			Expect(disruptionutils.LifetimeRemaining(fakeClock, test.NodePool(), nc)).
				To(BeNumerically("==", 1.0))
		})
		It("ignores age entirely on the nil branch, so an ancient NodeClaim still scores 1.0", func() {
			nc := expiringNodeClaim("Never", 365*24*time.Hour)
			Expect(disruptionutils.LifetimeRemaining(fakeClock, test.NodePool(), nc)).
				To(BeNumerically("==", 1.0))
		})
	})

	Context("a set ExpireAfter duration", func() {
		DescribeTable("scales the remaining fraction and clamps it to [0, 1]",
			func(expireAfter string, age time.Duration, expected float64) {
				nc := expiringNodeClaim(expireAfter, age)
				Expect(disruptionutils.LifetimeRemaining(fakeClock, test.NodePool(), nc)).
					To(BeNumerically("~", expected, 0.001))
			},
			Entry("a NodeClaim created this instant has its whole lifetime", "1h", time.Duration(0), 1.0),
			Entry("halfway through is 0.5", "1h", 30*time.Minute, 0.5),
			Entry("three quarters through is 0.25", "1h", 45*time.Minute, 0.25),
			Entry("exactly at expiry is 0", "1h", time.Hour, 0.0),
			Entry("past expiry clamps to 0 rather than going negative", "1h", 3*time.Hour, 0.0),
			Entry("a creation timestamp ahead of the clock clamps to 1 rather than exceeding it", "1h", -2*time.Hour, 1.0),
		)
		It("reads the clock rather than wall time, so stepping it ages the NodeClaim", func() {
			nc := expiringNodeClaim("1h", 0)
			Expect(disruptionutils.LifetimeRemaining(fakeClock, test.NodePool(), nc)).To(BeNumerically("~", 1.0, 0.001))
			fakeClock.Step(45 * time.Minute)
			Expect(disruptionutils.LifetimeRemaining(fakeClock, test.NodePool(), nc)).To(BeNumerically("~", 0.25, 0.001))
		})
		It("ignores the NodePool argument, which it accepts but never reads", func() {
			nc := expiringNodeClaim("1h", 30*time.Minute)
			Expect(disruptionutils.LifetimeRemaining(fakeClock, nil, nc)).To(BeNumerically("~", 0.5, 0.001))
		})
	})

	// A zero ExpireAfter is a non-nil pointer at 0, so it takes the division
	// branch with a zero denominator. lo.Clamp compares with < and >, both of
	// which are false for NaN, so NaN passes through unclamped. Pinned because
	// the result feeds a multiplication into Candidate.DisruptionCost, where a
	// NaN is unorderable and silently loses every ranking comparison.
	Context("a zero ExpireAfter duration, which divides by zero", func() {
		It("returns NaN when the age is also zero", func() {
			nc := expiringNodeClaim("0s", 0)
			Expect(nc.Spec.ExpireAfter.Duration).ToNot(BeNil())
			Expect(math.IsNaN(disruptionutils.LifetimeRemaining(fakeClock, test.NodePool(), nc))).To(BeTrue(),
				"0/0 is NaN and lo.Clamp does not filter it")
		})
		It("returns 0 for any non-zero age, because -Inf does clamp", func() {
			nc := expiringNodeClaim("0s", time.Second)
			Expect(disruptionutils.LifetimeRemaining(fakeClock, test.NodePool(), nc)).
				To(BeNumerically("==", 0.0))
		})
	})
})

var _ = Describe("IsUnderConsolidateAfter", func() {
	Context("the early-return guards", func() {
		It("returns false for a nil NodePool, before any NodeClaim field is read", func() {
			Expect(disruptionutils.IsUnderConsolidateAfter(nil, initializedNodeClaim(0), fakeClock)).To(BeFalse())
		})
		It("returns false for a nil NodeClaim", func() {
			Expect(disruptionutils.IsUnderConsolidateAfter(consolidateAfterNodePool("1h"), nil, fakeClock)).To(BeFalse())
		})
		It("returns false for a nil ConsolidateAfter duration, which is ConsolidateAfter: Never", func() {
			np := consolidateAfterNodePool("Never")
			Expect(np.Spec.Disruption.ConsolidateAfter.Duration).To(BeNil())
			Expect(disruptionutils.IsUnderConsolidateAfter(np, initializedNodeClaim(0), fakeClock)).To(BeFalse())
		})
		It("returns false for a zero ConsolidateAfter, which is a non-nil pointer the nil check misses", func() {
			np := consolidateAfterNodePool("0s")
			Expect(np.Spec.Disruption.ConsolidateAfter.Duration).ToNot(BeNil())
			Expect(disruptionutils.IsUnderConsolidateAfter(np, initializedNodeClaim(0), fakeClock)).To(BeFalse())
		})
		// The two specs above pass whether or not their guard is present, because
		// a lastPodEvent at the clock's own time gives Since == 0 and the
		// comparator reads `0 < 0`, which is false either way. A lastPodEvent in
		// the future is the only input where guard and comparator disagree: the
		// guard returns false, while `Since(future) < 0` is true. Without these
		// two entries, deleting either guard is an undetected change.
		DescribeTable("returns false for a disabled ConsolidateAfter even when the comparator alone would say true",
			func(consolidateAfter string) {
				np := consolidateAfterNodePool(consolidateAfter)
				nc := initializedNodeClaim(-time.Minute)
				Expect(fakeClock.Since(nc.Status.LastPodEventTime.Time)).To(BeNumerically("<", 0),
					"the entry is only meaningful while Since is negative")
				Expect(disruptionutils.IsUnderConsolidateAfter(np, nc, fakeClock)).To(BeFalse())
			},
			Entry("Never, a nil duration", "Never"),
			Entry("0s, a non-nil duration at zero", "0s"),
		)
		It("returns false for a NodeClaim with no Initialized condition", func() {
			// test.NodeClaim sets no conditions, so Initialized is absent rather
			// than false. IsTrue covers both, and only this spec covers absent.
			Expect(disruptionutils.IsUnderConsolidateAfter(consolidateAfterNodePool("1h"), test.NodeClaim(), fakeClock)).
				To(BeFalse())
		})
		It("returns false for a NodeClaim whose Initialized condition is explicitly false", func() {
			nc := test.NodeClaim()
			nc.StatusConditions().SetFalse(v1.ConditionTypeInitialized, "NotInitialized", "test")
			nc.Status.LastPodEventTime.Time = fakeClock.Now()
			Expect(disruptionutils.IsUnderConsolidateAfter(consolidateAfterNodePool("1h"), nc, fakeClock)).To(BeFalse())
		})
	})

	Context("the lastPodEvent window", func() {
		DescribeTable("compares the time since the last pod event against ConsolidateAfter",
			func(consolidateAfter string, lastPodEventAge time.Duration, expected bool) {
				np := consolidateAfterNodePool(consolidateAfter)
				Expect(disruptionutils.IsUnderConsolidateAfter(np, initializedNodeClaim(lastPodEventAge), fakeClock)).
					To(Equal(expected))
			},
			Entry("a pod event this instant is inside the window", "1m", time.Duration(0), true),
			Entry("a pod event well inside the window counts", "1m", 30*time.Second, true),
			Entry("a pod event exactly ConsolidateAfter ago is outside, because the comparator is strict", "1m", time.Minute, false),
			Entry("one nanosecond short of the boundary is still inside", "1m", time.Minute-time.Nanosecond, true),
			Entry("a pod event past the window is outside", "1m", 5*time.Minute, false),
			Entry("a pod event in the future is inside, since a negative Since is below any positive duration", "1m", -time.Minute, true),
		)
		It("leaves the window as the clock advances past ConsolidateAfter", func() {
			np := consolidateAfterNodePool("1m")
			nc := initializedNodeClaim(0)
			Expect(disruptionutils.IsUnderConsolidateAfter(np, nc, fakeClock)).To(BeTrue())
			fakeClock.Step(time.Minute)
			Expect(disruptionutils.IsUnderConsolidateAfter(np, nc, fakeClock)).To(BeFalse())
		})
	})

	// With no pod event recorded the helper falls back to the Initialized
	// condition's transition time, the point at which Karpenter first accepted
	// that pods could schedule. Without the fallback a never-scheduled node
	// would measure Since(zero time), which is decades and always outside the
	// window.
	Context("a zero LastPodEventTime", func() {
		It("falls back to the Initialized transition time and reports inside the window", func() {
			nc := withZeroLastPodEvent(initializedNodeClaim(0))
			Expect(disruptionutils.IsUnderConsolidateAfter(consolidateAfterNodePool("1m"), nc, fakeClock)).To(BeTrue())
		})
		It("reports outside the window once the clock passes ConsolidateAfter from that transition time", func() {
			nc := withZeroLastPodEvent(initializedNodeClaim(0))
			fakeClock.Step(time.Minute)
			Expect(disruptionutils.IsUnderConsolidateAfter(consolidateAfterNodePool("1m"), nc, fakeClock)).To(BeFalse())
		})
		It("does not measure from the zero time, which would put every fresh node outside the window", func() {
			// The distinguishing input: a long ConsolidateAfter that the zero
			// time still exceeds. Measuring from time.Time{} gives decades and
			// returns false; measuring from the transition time gives ~0 and
			// returns true.
			nc := withZeroLastPodEvent(initializedNodeClaim(0))
			Expect(disruptionutils.IsUnderConsolidateAfter(consolidateAfterNodePool("8760h"), nc, fakeClock)).To(BeTrue())
		})
	})
})
