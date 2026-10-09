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

package scheduling

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("IsKnownEphemeralTaint", func() {
	It("matches an exact entry in KnownEphemeralTaints", func() {
		Expect(IsKnownEphemeralTaint(&corev1.Taint{
			Key:    corev1.TaintNodeNotReady,
			Effect: corev1.TaintEffectNoSchedule,
		})).To(BeTrue())
	})

	It("matches a NodeReadinessController taint by prefix", func() {
		Expect(IsKnownEphemeralTaint(&corev1.Taint{
			Key:    "readiness.k8s.io/my-rule",
			Effect: corev1.TaintEffectNoSchedule,
		})).To(BeTrue())
	})

	It("matches any effect for a prefixed taint", func() {
		Expect(IsKnownEphemeralTaint(&corev1.Taint{
			Key:    "readiness.k8s.io/another",
			Effect: corev1.TaintEffectNoExecute,
		})).To(BeTrue())
	})

	It("does not match an unrelated taint", func() {
		Expect(IsKnownEphemeralTaint(&corev1.Taint{
			Key:    "example.com/some-taint",
			Effect: corev1.TaintEffectNoSchedule,
		})).To(BeFalse())
	})

	It("does not match a taint whose key only contains the prefix in the middle", func() {
		Expect(IsKnownEphemeralTaint(&corev1.Taint{
			Key:    "foo/readiness.k8s.io/bar",
			Effect: corev1.TaintEffectNoSchedule,
		})).To(BeFalse())
	})

	It("returns false for a nil taint", func() {
		Expect(IsKnownEphemeralTaint(nil)).To(BeFalse())
	})
})

var _ = Describe("Taints toleration", func() {
	noSchedule := func(key string) corev1.Taint {
		return corev1.Taint{Key: key, Value: "true", Effect: corev1.TaintEffectNoSchedule}
	}
	equals := func(key string) corev1.Toleration {
		return corev1.Toleration{Key: key, Operator: corev1.TolerationOpEqual, Value: "true", Effect: corev1.TaintEffectNoSchedule}
	}
	exists := corev1.Toleration{Operator: corev1.TolerationOpExists}

	// IsToleratedBy is a second traversal of the same taints, so the risk it carries is
	// disagreeing with Tolerates rather than being wrong on its own. Every case asserts
	// both, which is what keeps the two from drifting.
	DescribeTable("agrees with Tolerates on whether the taints are tolerated",
		func(taints Taints, tolerations []corev1.Toleration, tolerated bool) {
			Expect(taints.IsToleratedBy(tolerations)).To(Equal(tolerated))
			Expect(taints.Tolerates(tolerations) == nil).To(Equal(tolerated))
		},
		Entry("no taints and no tolerations", Taints{}, []corev1.Toleration{}, true),
		Entry("a taint and no tolerations", Taints{noSchedule("a")}, []corev1.Toleration{}, false),
		Entry("no taints and a toleration", Taints{}, []corev1.Toleration{equals("a")}, true),
		Entry("the only toleration matches", Taints{noSchedule("a")}, []corev1.Toleration{equals("a")}, true),
		Entry("the only toleration does not match", Taints{noSchedule("a")}, []corev1.Toleration{equals("b")}, false),
		Entry("an unkeyed Exists matches any taint", Taints{noSchedule("a"), noSchedule("b")}, []corev1.Toleration{exists}, true),
		Entry("every taint is matched", Taints{noSchedule("a"), noSchedule("b")}, []corev1.Toleration{equals("a"), equals("b")}, true),
		// The first taint is tolerated and the second is not. A predicate that returned on
		// the first taint's verdict rather than on the first rejection would call this
		// tolerated.
		Entry("only the first taint is matched", Taints{noSchedule("a"), noSchedule("b")}, []corev1.Toleration{equals("a")}, false),
		Entry("only the last taint is matched", Taints{noSchedule("a"), noSchedule("b")}, []corev1.Toleration{equals("b")}, false),
		// The matching toleration's position decides how far the inner scan runs, so pin
		// both ends: a predicate that stopped at the first toleration would reject the
		// first case, and one that read only the last would reject the second.
		Entry("the matching toleration is last", Taints{noSchedule("b")}, []corev1.Toleration{equals("a"), equals("b")}, true),
		Entry("the matching toleration is first", Taints{noSchedule("a")}, []corev1.Toleration{equals("a"), equals("b")}, true),
	)

	It("agrees with ToleratesPod on a pod's tolerations", func() {
		taints := Taints{noSchedule("a")}
		tolerating := &corev1.Pod{Spec: corev1.PodSpec{Tolerations: []corev1.Toleration{equals("a")}}}
		rejecting := &corev1.Pod{Spec: corev1.PodSpec{Tolerations: []corev1.Toleration{equals("b")}}}

		Expect(taints.IsToleratedByPod(tolerating)).To(BeTrue())
		Expect(taints.ToleratesPod(tolerating)).To(Succeed())
		Expect(taints.IsToleratedByPod(rejecting)).To(BeFalse())
		Expect(taints.ToleratesPod(rejecting)).ToNot(Succeed())
	})

	// Tolerates reports every taint it rejects, and existingnode.go and nodeclaim.go
	// surface that message. Adding the boolean path must not turn Tolerates into a
	// short circuit, which would silently drop all but the first rejected taint.
	It("still reports every rejected taint from Tolerates", func() {
		err := Taints{noSchedule("a"), noSchedule("b"), noSchedule("c")}.Tolerates([]corev1.Toleration{equals("b")})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("a=true:NoSchedule"))
		Expect(err.Error()).To(ContainSubstring("c=true:NoSchedule"))
		Expect(err.Error()).ToNot(ContainSubstring("b=true:NoSchedule"))
	})

	// The reject path is the whole point of the boolean twin: Tolerates formats a message
	// per rejected taint, so a caller that discards the error pays for it. Pinning zero
	// here is what stops the allocation coming back.
	It("allocates nothing on the reject path, where Tolerates allocates per taint", func() {
		taints := Taints{noSchedule("a"), noSchedule("b"), noSchedule("c")}
		tolerations := []corev1.Toleration{equals("d")}

		Expect(testing.AllocsPerRun(100, func() { taints.IsToleratedBy(tolerations) })).To(BeZero())
		Expect(testing.AllocsPerRun(100, func() { _ = taints.Tolerates(tolerations) })).To(BeNumerically(">", 0))
	})
})
