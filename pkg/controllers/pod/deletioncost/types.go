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
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/karpenter/pkg/controllers/state"
)

// NodeRank is the per-node record RankNodes produces for the deletioncost
// pipeline (filterNoOpNodes, capNodeRanks, enqueueAnnotationWrites). Fields
// bundle every value the downstream stages read so no stage re-consults the
// informer cache mid-cycle:
//   - Rank: sequential across Groups B+C, math.MinInt32 sentinel for Group A,
//     unread for Group D. filter and cap both branch on the sentinel vs a
//     sequential value.
//   - CleanupOnly: true for Group D entries whose annotations get cleared
//     rather than written; Rank is unused in that case.
//   - Pods: snapshot captured by classifyNode's node.Pods call. Reusing this
//     slice in filter and enqueue guarantees the classification decision and
//     the pod set the queue acts on stay in agreement even if the informer
//     cache updates mid-cycle.
//   - Node: retained for test observability (correlating rank to node
//     identity in ranking_test.go). Production code paths never dereference
//     it; dropping the field would force tests onto Pods[0].Spec.NodeName
//     which is undefined for pod-less nodes.
type NodeRank struct {
	Node        *state.StateNode
	Rank        int
	CleanupOnly bool
	Pods        []*corev1.Pod
}
