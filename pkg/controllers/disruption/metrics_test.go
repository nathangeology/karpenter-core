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
	"fmt"

	opmetrics "github.com/awslabs/operatorpkg/metrics"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"

	"sigs.k8s.io/karpenter/pkg/controllers/disruption"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/test"
)

var _ = Describe("Metrics", func() {
	// opmetrics.Label documents a non-empty Values list as the stable set of values the
	// dimension can take, and the docs generator renders it as that dimension's value
	// table. A Method whose ConsolidationType() is outside the list emits a series the
	// table cannot explain, which is how the empty string shipped undocumented on the GA
	// voluntary_disruption_decisions_total.
	//
	// Both gate states are checked because NewMethods only appends Repair when NodeRepair
	// is on, and that gate defaults to false. Walking the default set alone would leave
	// Repair's value unchecked.
	DescribeTable("should declare every consolidation_type value a disruption method emits",
		func(nodeRepair bool) {
			gateCtx := options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{NodeRepair: lo.ToPtr(nodeRepair)},
			}))
			methods := disruption.NewMethods(gateCtx, env.Clock, cluster, env.Client, prov, cloudProvider, recorder, queue)
			Expect(methods).ToNot(BeEmpty())
			Expect(repairMethodCount(methods)).To(Equal(lo.Ternary(nodeRepair, 1, 0)),
				"the NodeRepair gate did not decide whether Repair is in the method set")

			declared := lo.Map(disruption.ConsolidationType.Values, func(v opmetrics.Value, _ int) string {
				return v.Name
			})
			// Collect rather than assert per method, so one run names every offender instead
			// of stopping at whichever one NewMethods happens to return first.
			undeclared := lo.FilterMap(methods, func(m disruption.Method, _ int) (string, bool) {
				return fmt.Sprintf("%T emits %q", m, m.ConsolidationType()), !lo.Contains(declared, m.ConsolidationType())
			})
			Expect(undeclared).To(BeEmpty(),
				"these methods emit a consolidation_type outside ConsolidationType.Values %q", declared)
		},
		Entry("with NodeRepair disabled", false),
		Entry("with NodeRepair enabled", true),
	)

	// Naming the value must not change it. The empty string is what the GA
	// voluntary_disruption_decisions_total already carries for drift, static drift and
	// repair, so an existing query or dashboard selects on it today. Giving it a name
	// like "none" would be a breaking change to that series rather than documentation,
	// which is why NoConsolidationType keeps the empty string. Pinned here because the
	// assertions in consolidation_test.go spell the expected value as the method's own
	// ConsolidationType() and so would follow any edit instead of catching it.
	It("should emit the unchanged empty consolidation_type for the non-consolidation methods", func() {
		Expect(disruption.NoConsolidationType.Name).To(BeEmpty())
		// Gate on, so Repair is in the set alongside drift and static drift; it is the
		// third method that emits the empty value and NewMethodsWithNopValidator omits it.
		gateCtx := options.ToContext(ctx, test.Options(test.OptionsFields{
			FeatureGates: test.FeatureGates{NodeRepair: lo.ToPtr(true)},
		}))
		methods := disruption.NewMethods(gateCtx, env.Clock, cluster, env.Client, prov, cloudProvider, recorder, queue)
		Expect(repairMethodCount(methods)).To(Equal(1))
		var empty []string
		named := map[string]string{}
		for _, m := range methods {
			if m.ConsolidationType() == "" {
				empty = append(empty, fmt.Sprintf("%T", m))
				continue
			}
			named[fmt.Sprintf("%T", m)] = m.ConsolidationType()
		}
		Expect(empty).To(ConsistOf("*disruption.Drift", "*disruption.StaticDrift", "*disruption.Repair"))
		Expect(named).To(Equal(map[string]string{
			"*disruption.Emptiness":               disruption.EmptyConsolidationType.Name,
			"*disruption.MultiNodeConsolidation":  disruption.MultiNodeConsolidationType.Name,
			"*disruption.SingleNodeConsolidation": disruption.SingleNodeConsolidationType.Name,
		}))
	})

	// The dimension's value set is what a dashboard query filters on, so a duplicate
	// entry means two Values documenting one series and the generated table shows the
	// value twice with conflicting help text.
	It("should not declare the same consolidation_type value twice", func() {
		declared := lo.Map(disruption.ConsolidationType.Values, func(v opmetrics.Value, _ int) string {
			return v.Name
		})
		Expect(declared).To(HaveLen(len(lo.Uniq(declared))))
	})
})
