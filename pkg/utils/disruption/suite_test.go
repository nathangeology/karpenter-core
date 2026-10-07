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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

func TestDisruption(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Disruption")
}

var _ = BeforeEach(func() {
	ctx = options.ToContext(context.Background(), test.Options())
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
