//go:build test_performance

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
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/fake"
	"sigs.k8s.io/karpenter/pkg/test"
)

// BenchmarkBuildDomainGroups benchmarks the buildDomainGroups function in isolation
// from the rest of NewTopology. This function is the inner CPU hot-spot identified
// in PR #2671: nested loops over (NodePools × InstanceTypes × topology keys) make
// it O(N × I × T), and a regression in the per-iteration body (e.g. an extra
// requirements.String() allocation) shows up here as a multiplicative ns/op spike
// at the largest sub-bench cell.
//
// To run:
//
//	go test -tags=test_performance -run=XXX -bench=BenchmarkBuildDomainGroups -count=10 -benchmem ./pkg/controllers/provisioning/scheduling/
//
// To compare before/after with benchstat:
//
//	go test -tags=test_performance -run=XXX -bench=BenchmarkBuildDomainGroups -count=10 -benchmem | tee /tmp/old
//	# make changes
//	go test -tags=test_performance -run=XXX -bench=BenchmarkBuildDomainGroups -count=10 -benchmem | tee /tmp/new
//	benchstat /tmp/old /tmp/new
func BenchmarkBuildDomainGroups(b *testing.B) {
	cases := []struct {
		nodePoolCount   int
		instanceTypes   int
		extraTopologies int
	}{
		{nodePoolCount: 5, instanceTypes: 100, extraTopologies: 5},
		{nodePoolCount: 10, instanceTypes: 200, extraTopologies: 5},
		{nodePoolCount: 20, instanceTypes: 500, extraTopologies: 10},
	}

	for _, c := range cases {
		name := fmt.Sprintf("NP%d-IT%d-TK%d", c.nodePoolCount, c.instanceTypes, c.extraTopologies)
		nodePools, instanceTypes := buildDomainGroupsFixture(c.nodePoolCount, c.instanceTypes, c.extraTopologies)

		// Sanity-check the fixture: report the total domain count produced by a
		// single call so reviewers can confirm the (NP, IT, TK) shape is realistic.
		domains := buildDomainGroups(nodePools, instanceTypes)
		domainCount := 0
		for _, g := range domains {
			domainCount += len(g)
		}

		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = buildDomainGroups(nodePools, instanceTypes)
			}
			b.ReportMetric(float64(domainCount), "domains")
		})
	}
}

// buildDomainGroupsFixture builds a deterministic fixture for BenchmarkBuildDomainGroups.
// It returns a slice of nodePoolCount NodePools (each with extraTopologies NP-level
// In-operator requirements that contribute to the topology-key cross-product) and a
// map of npName -> []InstanceType holding instanceTypesPerPool entries per pool.
func buildDomainGroupsFixture(nodePoolCount, instanceTypesPerPool, extraTopologies int) ([]*v1.NodePool, map[string][]*cloudprovider.InstanceType) {
	nodePools := make([]*v1.NodePool, nodePoolCount)
	instanceTypes := make(map[string][]*cloudprovider.InstanceType, nodePoolCount)

	// Generate a shared pool of instance types once and slice it per NodePool.
	// fake.InstanceTypes(N) produces N InstanceTypes with the standard 8 topology
	// keys (instance-type, arch, OS, zone, capacity-type, size, exotic, integer).
	allInstanceTypes := fake.InstanceTypes(instanceTypesPerPool)

	for i := 0; i < nodePoolCount; i++ {
		// Build extraTopologies custom NP-level In-requirements. These show up in
		// the second loop of buildDomainGroups (lines ~129-140) and contribute one
		// new key per requirement to the topology cross-product.
		extraRequirements := make([]v1.NodeSelectorRequirementWithMinValues, 0, extraTopologies+1)
		extraRequirements = append(extraRequirements, v1.NodeSelectorRequirementWithMinValues{
			Key:      v1.CapacityTypeLabelKey,
			Operator: corev1.NodeSelectorOpExists,
		})
		for j := 0; j < extraTopologies; j++ {
			extraRequirements = append(extraRequirements, v1.NodeSelectorRequirementWithMinValues{
				Key:      fmt.Sprintf("topology.benchmark.test/key-%d", j),
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{"v0", "v1", "v2", "v3"},
			})
		}

		np := test.NodePool(v1.NodePool{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("bench-pool-%d", i)},
			Spec: v1.NodePoolSpec{
				Template: v1.NodeClaimTemplate{
					Spec: v1.NodeClaimTemplateSpec{
						Requirements: extraRequirements,
					},
				},
			},
		})
		nodePools[i] = np
		instanceTypes[np.Name] = allInstanceTypes
	}

	return nodePools, instanceTypes
}
