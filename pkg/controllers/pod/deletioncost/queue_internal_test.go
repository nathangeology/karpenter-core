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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/karpenter/pkg/controllers/node/termination/terminator"
)

// Specs for unexported Queue state. The dot import shares suite_test.go's RunSpecs.
// These reach into q.source and q.items because the two halves of the mid-flight
// Add fix are not both observable through the exported surface: Add's channel
// push is what makes controller-runtime re-deliver, and no exported method
// reports whether a push happened.
var _ = Describe("Queue.Add", func() {
	newPod := func() *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pod", UID: "pod-uid"}}
	}

	It("pushes to the source channel on every Add, not only the first", func() {
		q := NewQueue(nil)
		pod := newPod()

		q.Add(pod, -1, false)
		q.Add(pod, -5, false)

		// Two pushes is the whole point: the second Add models one landing while a
		// reconcile holds the first value, and the workqueue turns the duplicate
		// request into a re-delivery after the in-flight reconcile calls Done.
		// Pending requests still collapse, since the request carries only the key.
		Expect(q.source).To(HaveLen(2))
		Expect(q.items[terminator.NewQueueKey(pod)]).To(Equal(queueItem{rank: -5}))
	})

	It("keeps a pod enqueued across repeated Adds under one key", func() {
		q := NewQueue(nil)
		pod := newPod()

		q.Add(pod, -1, false)
		q.Add(pod, -5, true)

		Expect(q.items).To(HaveLen(1), "repeated Adds must not grow the side map")
		Expect(q.items[terminator.NewQueueKey(pod)]).To(Equal(queueItem{rank: -5, clear: true}))
	})
})

var _ = Describe("Queue.completeIfUnchanged", func() {
	newPod := func() *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pod", UID: "pod-uid"}}
	}

	It("retains the item when an Add replaced the desired state mid-reconcile", func() {
		q := NewQueue(nil)
		pod := newPod()
		qk := terminator.NewQueueKey(pod)

		q.Add(pod, -1, false)
		acted := q.items[qk]
		q.Add(pod, -5, false)

		q.completeIfUnchanged(qk, acted)

		Expect(q.items).To(HaveKey(qk), "the newer desired state must survive the completing reconcile")
		Expect(q.items[qk]).To(Equal(queueItem{rank: -5}))
	})

	It("drops the item when the desired state is the one that was written", func() {
		q := NewQueue(nil)
		pod := newPod()
		qk := terminator.NewQueueKey(pod)

		q.Add(pod, -1, false)
		q.completeIfUnchanged(qk, q.items[qk])

		Expect(q.items).ToNot(HaveKey(qk))
	})

	It("treats a clear-only difference as a replacement", func() {
		q := NewQueue(nil)
		pod := newPod()
		qk := terminator.NewQueueKey(pod)

		q.Add(pod, -1, false)
		acted := q.items[qk]
		q.Add(pod, -1, true)

		q.completeIfUnchanged(qk, acted)

		Expect(q.items[qk]).To(Equal(queueItem{rank: -1, clear: true}),
			"clear flips the write from patch to delete, so it must not be collapsed with the acted-on state")
	})
})
