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

package deletioncost

import (
	"math"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Specs for nodeMutatesAnyPod, which is unexported and declared in controller.go.
// The dot import shares suite_test.go's RunSpecs.
var _ = Describe("nodeMutatesAnyPod", func() {
	pod := func(value string) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{}}
		if value != "" {
			p.Annotations = map[string]string{corev1.PodDeletionCost: value}
		}
		return p
	}
	It("reports mutation when any pod's annotation differs from the planned rank", func() {
		pods := []*corev1.Pod{pod("-5"), pod("-3")}
		Expect(nodeMutatesAnyPod(pods, -5, false)).To(BeTrue())
	})
	It("reports no-op when every pod already carries the planned rank", func() {
		pods := []*corev1.Pod{pod("-5"), pod("-5")}
		Expect(nodeMutatesAnyPod(pods, -5, false)).To(BeFalse())
	})
	It("reports no-op when every pod's annotation is already cleared", func() {
		pods := []*corev1.Pod{pod(""), pod("")}
		Expect(nodeMutatesAnyPod(pods, 0, true)).To(BeFalse())
	})
	It("reports mutation for cleanup when any pod still carries an annotation", func() {
		pods := []*corev1.Pod{pod("-2"), pod("")}
		Expect(nodeMutatesAnyPod(pods, 0, true)).To(BeTrue())
	})
	It("reports no-op for an empty pod list", func() {
		Expect(nodeMutatesAnyPod(nil, -1, false)).To(BeFalse())
		Expect(nodeMutatesAnyPod(nil, 0, true)).To(BeFalse())
	})
	It("reports mutation when any Group A pod lacks the sentinel", func() {
		sentinel := strconv.Itoa(math.MinInt32)
		pods := []*corev1.Pod{pod(sentinel), pod("")}
		Expect(nodeMutatesAnyPod(pods, math.MinInt32, false)).To(BeTrue())
	})
})
