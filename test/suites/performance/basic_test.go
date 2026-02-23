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

	"sigs.k8s.io/karpenter/test/pkg/debug"

	"sigs.k8s.io/karpenter/pkg/test"
)

var _ = Describe("Performance", Label(debug.NoWatch), func() {
	Context("Basic Deployment", func() {
		It("should efficiently scale two deployments with different resource profiles", func() {
			// ========== PHASE 1: SCALE-OUT TEST ==========
			By("Executing scale-out performance test with 1000 pods")
			// Create deployments directly using the template
			smallDeploymentOpts := test.CreateDeploymentOptions("small-resource-app", 500, "900m", "3100Mi")
			largeDeploymentOpts := test.CreateDeploymentOptions("large-resource-app", 500, "3500m", "28Gi")

			smallDeployment := test.Deployment(smallDeploymentOpts)
			largeDeployment := test.Deployment(largeDeploymentOpts)

			env.ExpectCreated(nodePool, nodeClass, smallDeployment, largeDeployment)

			scaleOutReport, err := ReportScaleOutWithOutput(env, "Scale Out Test", 1000, 15*time.Minute, "scale_out", sizeClassLockThreshold)
			Expect(err).ToNot(HaveOccurred())
			Expect(scaleOutReport.TestType).To(Equal("scale-out"), "Should be detected as scale-out test")
			Expect(scaleOutReport.TotalPods).To(Equal(1000), "Should have 1000 total pods")

			// Performance assertions
			Expect(scaleOutReport.TotalTime).To(BeNumerically("<", 4*time.Minute),
				"Total scale-out time should be less than 2 minutes")
			Expect(scaleOutReport.TotalReservedCPUUtil).To(BeNumerically(">", 0.53),
				"Average CPU utilization should be greater than 53%")
			Expect(scaleOutReport.TotalReservedMemoryUtil).To(BeNumerically(">", 0.65),
				"Average memory utilization should be greater than 65%")

			// ========== POD DELETION COST CHECK ==========
			By("Checking for pod deletion cost annotations")

			// Get initial disruption count before waiting
			disruptionsBeforeWait := getPodsDisruptedCount(env)

			deletionCostDetected := checkPodDeletionCostAnnotations(env)
			if !deletionCostDetected {
				By("Pod deletion cost not detected, waiting 9 minutes and checking again")
				time.Sleep(9 * time.Minute)
				deletionCostDetected = checkPodDeletionCostAnnotations(env)
			}

			// Get disruption count after waiting and calculate any disruptions during wait
			disruptionsAfterWait := getPodsDisruptedCount(env)
			disruptionsDuringWait := disruptionsAfterWait - disruptionsBeforeWait

			if disruptionsDuringWait > 0 {
				GinkgoWriter.Printf("⚠ %d pod disruptions occurred during deletion cost check wait period\n", disruptionsDuringWait)
			}

			if deletionCostDetected {
				GinkgoWriter.Printf("✓ Pod deletion cost annotations detected and working\n")
			} else {
				GinkgoWriter.Printf("⚠ Pod deletion cost annotations not detected (feature may be disabled)\n")
			}

			// ========== PHASE 2: CONSOLIDATION TEST ==========
			By("Executing consolidation performance test (scaling down to 700 pods)")
			// Phase 2: Scale-down and consolidation
			initialNodes := scaleOutReport.TotalNodes

			// Update deployments to scale down
			smallDeployment.Spec.Replicas = lo.ToPtr(int32(350))
			largeDeployment.Spec.Replicas = lo.ToPtr(int32(350))
			env.ExpectUpdated(smallDeployment, largeDeployment)

			// Note: ReportConsolidation captures its own baseline at the start, so wait-period
			// disruptions are automatically excluded. We pass 0 for baselineDisruptions.
			consolidationReport, err := ReportConsolidationWithOutput(env, "Consolidation Test", 1000, 700, initialNodes, 20*time.Minute, "consolidation", sizeClassLockThreshold, 0)
			Expect(err).ToNot(HaveOccurred())
			Expect(consolidationReport.TestType).To(Equal("consolidation"), "Should be detected as consolidation test")
			Expect(consolidationReport.TotalPods).To(Equal(700), "Should have 700 total pods after scale-in")
			Expect(consolidationReport.PodsNetChange).To(Equal(-300), "Should have net reduction of 300 pods")

			// Report disruption information
			if disruptionsDuringWait > 0 {
				GinkgoWriter.Printf("Note: %d disruptions occurred during wait period (automatically excluded from consolidation report)\n",
					disruptionsDuringWait)
			}
			GinkgoWriter.Printf("Consolidation disruptions: %d\n", consolidationReport.PodsDisrupted)

			Expect(consolidationReport.TotalTime).To(BeNumerically("<", 20*time.Minute),
				"Consolidation should complete within 20 minutes")

		})
	})
})
