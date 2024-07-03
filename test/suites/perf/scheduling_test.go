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

package perf_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/karpenter/pkg/apis/v1beta1"
	"sigs.k8s.io/karpenter/pkg/test"
)

var replicas int = 10
var _ = Describe("Performance", func() {
	Context("Provisioning", func() {
		//It("should do simple provisioning", func() {
		//	deployment := test.Deployment(test.DeploymentOptions{
		//		Replicas: int32(replicas),
		//		PodOptions: test.PodOptions{
		//			ObjectMeta: metav1.ObjectMeta{
		//				Labels: testLabels,
		//			},
		//			ResourceRequirements: v1.ResourceRequirements{
		//				Requests: v1.ResourceList{
		//					v1.ResourceCPU: resource.MustParse("1"),
		//				},
		//			},
		//		}})
		//	env.ExpectCreated(deployment)
		//	env.ExpectCreated(nodePool, nodeClass)
		//	env.EventuallyExpectHealthyPodCount(labelSelector, replicas)
		//})
		//It("should do simple provisioning and simple drift", func() {
		//	deployment := test.Deployment(test.DeploymentOptions{
		//		Replicas: int32(replicas),
		//		PodOptions: test.PodOptions{
		//			ObjectMeta: metav1.ObjectMeta{
		//				Labels: testLabels,
		//			},
		//			ResourceRequirements: v1.ResourceRequirements{
		//				Requests: v1.ResourceList{
		//					v1.ResourceCPU: resource.MustParse("1"),
		//				},
		//			},
		//		}})
		//	env.ExpectCreated(deployment)
		//	env.ExpectCreated(nodePool, nodeClass)
		//	env.EventuallyExpectHealthyPodCount(labelSelector, replicas)
		//
		//	env.TimeIntervalCollector.Start("Drift")
		//	nodePool.Spec.Template.ObjectMeta.Labels = lo.Assign(nodePool.Spec.Template.ObjectMeta.Labels, map[string]string{
		//		"test-drift": "true",
		//	})
		//	env.ExpectUpdated(nodePool)
		//	// Eventually expect one node to be drifted
		//	Eventually(func(g Gomega) {
		//		nodeClaims := &v1beta1.NodeClaimList{}
		//		g.Expect(env.Client.List(env, nodeClaims, client.MatchingFields{"status.conditions[*].type": v1beta1.ConditionTypeDrifted})).To(Succeed())
		//		g.Expect(len(nodeClaims.Items)).ToNot(Equal(0))
		//	}).WithTimeout(5 * time.Second).Should(Succeed())
		//	// Then eventually expect no nodes to be drifted
		//	Eventually(func(g Gomega) {
		//		nodeClaims := &v1beta1.NodeClaimList{}
		//		g.Expect(env.Client.List(env, nodeClaims, client.MatchingFields{"status.conditions[*].type": v1beta1.ConditionTypeDrifted})).To(Succeed())
		//		g.Expect(len(nodeClaims.Items)).To(Equal(0))
		//	}).WithTimeout(300 * time.Second).Should(Succeed())
		//	env.TimeIntervalCollector.End("Drift")
		//})
		//It("should do complex provisioning", func() {
		//	deployments := []*appsv1.Deployment{}
		//	podOptions := test.MakeDiversePodOptions()
		//	for _, option := range podOptions {
		//		deployments = append(deployments, test.Deployment(
		//			test.DeploymentOptions{
		//				PodOptions: option,
		//				Replicas:   int32(replicas / len(podOptions)),
		//			},
		//		))
		//	}
		//	for _, dep := range deployments {
		//		env.ExpectCreated(dep)
		//	}
		//	env.TimeIntervalCollector.Start("PostDeployment")
		//	env.ExpectCreated(nodePool, nodeClass)
		//	env.EventuallyExpectHealthyPodCountWithTimeout(10*time.Minute, labelSelector, len(deployments)*replicas)
		//	env.TimeIntervalCollector.End("PostDeployment")
		//})
		//It("should do complex provisioning and complex drift", func() {
		//	deployments := []*appsv1.Deployment{}
		//	podOptions := test.MakeDiversePodOptions()
		//	for _, option := range podOptions {
		//		deployments = append(deployments, test.Deployment(
		//			test.DeploymentOptions{
		//				PodOptions: option,
		//				Replicas:   int32(replicas / len(podOptions)),
		//			},
		//		))
		//	}
		//	for _, dep := range deployments {
		//		env.ExpectCreated(dep)
		//	}
		//
		//	env.ExpectCreated(nodePool, nodeClass)
		//	env.EventuallyExpectHealthyPodCountWithTimeout(10*time.Minute, labelSelector, len(deployments)*replicas)
		//
		//	env.TimeIntervalCollector.Start("Drift")
		//	nodePool.Spec.Template.ObjectMeta.Labels = lo.Assign(nodePool.Spec.Template.ObjectMeta.Labels, map[string]string{
		//		"test-drift": "true",
		//	})
		//	env.ExpectUpdated(nodePool)
		//	// Eventually expect one node to be drifted
		//	Eventually(func(g Gomega) {
		//		nodeClaims := &v1beta1.NodeClaimList{}
		//		g.Expect(env.Client.List(env, nodeClaims, client.MatchingFields{"status.conditions[*].type": v1beta1.ConditionTypeDrifted})).To(Succeed())
		//		g.Expect(len(nodeClaims.Items)).ToNot(Equal(0))
		//	}).WithTimeout(5 * time.Second).Should(Succeed())
		//	// Then eventually expect no nodes to be drifted
		//	Eventually(func(g Gomega) {
		//		nodeClaims := &v1beta1.NodeClaimList{}
		//		g.Expect(env.Client.List(env, nodeClaims, client.MatchingFields{"status.conditions[*].type": v1beta1.ConditionTypeDrifted})).To(Succeed())
		//		g.Expect(len(nodeClaims.Items)).To(Equal(0))
		//	}).WithTimeout(10 * time.Minute).Should(Succeed())
		//	env.TimeIntervalCollector.End("Drift")
		//})
		It("should do staggered multi-deployment provisioning and drift", func() {
			var scaleInReplicas int32 = 1
			deployments := []*appsv1.Deployment{}
			// TODO: Adjust pod options to be a fixed set of option (maybe update the ones I get from the k8s test api)
			fmt.Printf("Debug printing of pod options so I can make adjustments:\n")
			podOptions := simpleStdScenarioInstanceSpreadPodOptions(750, 1500)
			fmt.Printf("%#v\n", podOptions)
			for _, option := range podOptions {
				deployments = append(deployments, test.Deployment(
					test.DeploymentOptions{
						PodOptions: option,
						Replicas:   int32(replicas / len(podOptions)),
					},
				))
			}
			for _, dep := range deployments {
				time.Sleep(3 * time.Second)
				env.ExpectCreated(dep)
			}
			// NOTE: To update replicas update in the object and then call expect updated
			env.ExpectCreated(nodePool, nodeClass)
			env.EventuallyExpectHealthyPodCountWithTimeout(15*time.Minute, labelSelector, len(deployments)*replicas)

			env.TimeIntervalCollector.Start("Scale-in")
			for _, dep := range deployments {
				dep.Spec.Replicas = &scaleInReplicas
				env.ExpectUpdated(dep)
			}
			//nodePool.Spec.Template.ObjectMeta.Labels = lo.Assign(nodePool.Spec.Template.ObjectMeta.Labels, map[string]string{
			//	"test-drift": "true",
			//})
			//env.ExpectUpdated(nodePool)
			// Eventually expect one node to be drifted
			Eventually(func(g Gomega) {
				nodeClaims := &v1beta1.NodeClaimList{}
				g.Expect(env.Client.List(env, nodeClaims, client.MatchingFields{"status.conditions[*].type": v1beta1.ConditionTypeDrifted})).To(Succeed())
				g.Expect(len(nodeClaims.Items)).ToNot(Equal(0))
			}).WithTimeout(5 * time.Second).Should(Succeed())
			// Then eventually expect no nodes to be drifted
			Eventually(func(g Gomega) {
				nodeClaims := &v1beta1.NodeClaimList{}
				g.Expect(env.Client.List(env, nodeClaims, client.MatchingFields{"status.conditions[*].type": v1beta1.ConditionTypeDrifted})).To(Succeed())
				g.Expect(len(nodeClaims.Items)).To(Equal(0))
			}).WithTimeout(10 * time.Minute).Should(Succeed())
			env.TimeIntervalCollector.End("Scale-in")
		})
	})
})
