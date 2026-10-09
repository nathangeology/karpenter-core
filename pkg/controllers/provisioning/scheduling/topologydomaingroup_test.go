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

package scheduling_test

import (
	"sort"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/karpenter/pkg/controllers/provisioning/scheduling"
	"sigs.k8s.io/karpenter/pkg/test"
)

var _ = Describe("TopologyDomainGroup", func() {
	var teamTaint, gpuTaint corev1.Taint
	BeforeEach(func() {
		teamTaint = corev1.Taint{Key: "dedicated", Value: "team", Effect: corev1.TaintEffectNoSchedule}
		gpuTaint = corev1.Taint{Key: "nvidia.com/gpu", Value: "true", Effect: corev1.TaintEffectNoSchedule}
	})
	Context("Insert", func() {
		It("should track one taint set when the same set is inserted for every instance type", func() {
			// buildDomainGroups inserts a NodePool's template taints once per instance type offering the
			// domain, always with the same set. Only the first insert should be stored.
			domainGroup := scheduling.NewTopologyDomainGroup()
			for range 10 {
				domainGroup.Insert("test-zone-1", teamTaint)
			}
			Expect(domainGroup["test-zone-1"]).To(HaveLen(1))
			Expect(domainGroup["test-zone-1"][0]).To(ConsistOf(teamTaint))
		})
		It("should track a taint set per distinct set when NodePools taint a domain differently", func() {
			domainGroup := scheduling.NewTopologyDomainGroup()
			for range 10 {
				domainGroup.Insert("test-zone-1", teamTaint)
			}
			for range 10 {
				domainGroup.Insert("test-zone-1", gpuTaint)
			}
			Expect(domainGroup["test-zone-1"]).To(HaveLen(2))
		})
		It("should track a taint set per insert when equal sets are not consecutive", func() {
			// Deduplication only compares against the last set inserted, which is all buildDomainGroups
			// needs: it finishes one NodePool before starting the next. A caller interleaving NodePools
			// keeps the pre-existing behavior rather than a wrong one.
			domainGroup := scheduling.NewTopologyDomainGroup()
			domainGroup.Insert("test-zone-1", teamTaint)
			domainGroup.Insert("test-zone-1", gpuTaint)
			domainGroup.Insert("test-zone-1", teamTaint)
			Expect(domainGroup["test-zone-1"]).To(HaveLen(3))
		})
		It("should discard tracked taint sets once an untainted NodePool provides the domain", func() {
			domainGroup := scheduling.NewTopologyDomainGroup()
			domainGroup.Insert("test-zone-1", teamTaint)
			domainGroup.Insert("test-zone-1", gpuTaint)
			domainGroup.Insert("test-zone-1")
			Expect(domainGroup["test-zone-1"]).To(HaveLen(1))
			Expect(domainGroup["test-zone-1"][0]).To(BeEmpty())
			// An untainted domain stays untainted.
			domainGroup.Insert("test-zone-1", teamTaint)
			Expect(domainGroup["test-zone-1"]).To(HaveLen(1))
			Expect(domainGroup["test-zone-1"][0]).To(BeEmpty())
		})
	})
	Context("ForEachDomain", func() {
		// Duplicate taint sets change how many sets ForEachDomain traverses before it finds a tolerated
		// one, never whether it finds one. These assert the domains yielded are identical either way.
		var deduplicated, duplicated scheduling.TopologyDomainGroup
		BeforeEach(func() {
			deduplicated = scheduling.TopologyDomainGroup{
				"test-zone-1": {{teamTaint}, {gpuTaint}},
				"test-zone-2": {{gpuTaint}},
				"test-zone-3": {{}},
			}
			duplicated = scheduling.TopologyDomainGroup{
				"test-zone-1": {{teamTaint}, {teamTaint}, {teamTaint}, {gpuTaint}, {gpuTaint}},
				"test-zone-2": {{gpuTaint}, {gpuTaint}, {gpuTaint}},
				"test-zone-3": {{}},
			}
		})
		DescribeTable("should yield the same domains with and without duplicate taint sets",
			func(taintHonorPolicy corev1.NodeInclusionPolicy, tolerations []corev1.Toleration, expected []string) {
				pod := test.Pod(test.PodOptions{Tolerations: tolerations})
				Expect(domainsFor(deduplicated, pod, taintHonorPolicy)).To(Equal(expected))
				Expect(domainsFor(duplicated, pod, taintHonorPolicy)).To(Equal(expected))
			},
			Entry("honor, tolerating nothing", corev1.NodeInclusionPolicyHonor, nil,
				[]string{"test-zone-3"}),
			Entry("honor, tolerating one of the two taint sets on a domain", corev1.NodeInclusionPolicyHonor,
				[]corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}},
				[]string{"test-zone-1", "test-zone-3"}),
			Entry("honor, tolerating every taint", corev1.NodeInclusionPolicyHonor, []corev1.Toleration{
				{Key: "dedicated", Operator: corev1.TolerationOpExists},
				{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists},
			}, []string{"test-zone-1", "test-zone-2", "test-zone-3"}),
			Entry("ignore, tolerating nothing", corev1.NodeInclusionPolicyIgnore, nil,
				[]string{"test-zone-1", "test-zone-2", "test-zone-3"}),
		)
	})
})

func domainsFor(domainGroup scheduling.TopologyDomainGroup, pod *corev1.Pod, taintHonorPolicy corev1.NodeInclusionPolicy) []string {
	var domains []string
	domainGroup.ForEachDomain(pod, taintHonorPolicy, func(domain string) {
		domains = append(domains, domain)
	})
	sort.Strings(domains)
	return domains
}
