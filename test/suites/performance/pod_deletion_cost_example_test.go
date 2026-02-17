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

package performance

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"

	"sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/test/pkg/debug"
)

// Example test demonstrating how to configure pod deletion cost settings
// To run this test with pod deletion cost enabled:
//
//  1. Set the variables at the top of suite_test.go:
//     podDeletionCostEnabled = true
//     podDeletionCostRankingStrategy = "UnallocatedVCPUPerPodCost"  // or "Random", "LargestToSmallest", "SmallestToLargest"
//     podDeletionCostChangeDetection = true
//
//  2. Run the test:
//     go test -v ./test/suites/performance -run TestIntegration/Performance/PodDeletionCost
var _ = Describe("Performance", Label(debug.NoWatch), func() {
	Context("Pod Deletion Cost Example", func() {
		// This test is skipped by default. Remove the Skip() call to run it.
		It("should demonstrate pod deletion cost configuration", func() {
			Skip("Example test - remove Skip() to run")

			// ========== PHASE 1: SCALE-OUT TEST ==========
			By("Executing scale-out performance test with pod deletion cost enabled")

			// Create deployments
			smallDeploymentOpts := test.CreateDeploymentOptions("small-resource-app", 300, "900m", "3100Mi")
			largeDeploymentOpts := test.CreateDeploymentOptions("large-resource-app", 300, "3500m", "28Gi")

			smallDeployment := test.Deployment(smallDeploymentOpts)
			largeDeployment := test.Deployment(largeDeploymentOpts)

			env.ExpectCreated(nodePool, nodeClass, smallDeployment, largeDeployment)

			scaleOutReport, err := ReportScaleOutWithOutput(env, "Pod Deletion Cost Scale Out", 600, 10*time.Minute, "pod_deletion_cost_scale_out", sizeClassLockThreshold)
			Expect(err).ToNot(HaveOccurred())
			Expect(scaleOutReport.TotalPods).To(Equal(600), "Should have 600 total pods")

			// Verify pod deletion cost settings are captured in report
			Expect(scaleOutReport.PodDeletionCostEnabled).To(Equal(podDeletionCostEnabled))
			Expect(scaleOutReport.PodDeletionCostRankingStrategy).To(Equal(podDeletionCostRankingStrategy))
			Expect(scaleOutReport.PodDeletionCostChangeDetection).To(Equal(podDeletionCostChangeDetection))

			// ========== PHASE 2: CONSOLIDATION TEST ==========
			By("Executing consolidation performance test with pod deletion cost")
			initialNodes := scaleOutReport.TotalNodes

			// Scale down deployments
			smallDeployment.Spec.Replicas = lo.ToPtr(int32(200))
			largeDeployment.Spec.Replicas = lo.ToPtr(int32(200))
			env.ExpectUpdated(smallDeployment, largeDeployment)

			consolidationReport, err := ReportConsolidationWithOutput(env, "Pod Deletion Cost Consolidation", 600, 400, initialNodes, 15*time.Minute, "pod_deletion_cost_consolidation", sizeClassLockThreshold)
			Expect(err).ToNot(HaveOccurred())
			Expect(consolidationReport.TotalPods).To(Equal(400), "Should have 400 total pods after scale-in")

			// Verify pod deletion cost settings are captured in report
			Expect(consolidationReport.PodDeletionCostEnabled).To(Equal(podDeletionCostEnabled))
			Expect(consolidationReport.PodDeletionCostRankingStrategy).To(Equal(podDeletionCostRankingStrategy))

			// If pod deletion cost is enabled, you can add additional assertions here
			// to verify the behavior matches expectations for the configured strategy
			if podDeletionCostEnabled {
				By("Verifying pod deletion cost behavior")
				// Add custom verification logic here based on your ranking strategy
				// For example, you might check that pods were disrupted in the expected order
			}
		})
	})
})
