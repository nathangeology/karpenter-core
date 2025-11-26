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

package issues_test

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	clock "k8s.io/utils/clock/testing"

	coreapis "sigs.k8s.io/karpenter/pkg/apis"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/fake"
	"sigs.k8s.io/karpenter/pkg/controllers/disruption"
	"sigs.k8s.io/karpenter/pkg/controllers/provisioning"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/controllers/state/informer"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/state/cost"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var ctx context.Context
var env *test.Environment
var cluster *state.Cluster
var disruptionController *disruption.Controller
var prov *provisioning.Provisioner
var cloudProvider *fake.CloudProvider
var nodeStateController *informer.NodeController
var nodeClaimStateController *informer.NodeClaimController
var fakeClock *clock.FakeClock
var recorder *test.EventRecorder
var queue *disruption.Queue

var onDemandInstances []*cloudprovider.InstanceType
var leastExpensiveInstance, mostExpensiveInstance *cloudprovider.InstanceType
var leastExpensiveOffering, mostExpensiveOffering *cloudprovider.Offering

func TestIssues(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "Issues")
}

var _ = BeforeSuite(func() {
	env = test.NewEnvironment(test.WithCRDs(coreapis.CRDs...), test.WithCRDs(v1alpha1.CRDs...))
	ctx = options.ToContext(ctx, test.Options())
	cloudProvider = fake.NewCloudProvider()
	fakeClock = clock.NewFakeClock(time.Now())
	clusterCost := cost.NewClusterCost(ctx, cloudProvider, env.Client)
	cluster = state.NewCluster(fakeClock, env.Client, cloudProvider)
	nodeStateController = informer.NewNodeController(env.Client, cluster)
	nodeClaimStateController = informer.NewNodeClaimController(env.Client, cloudProvider, cluster, clusterCost)
	recorder = test.NewEventRecorder()
	prov = provisioning.NewProvisioner(env.Client, recorder, cloudProvider, cluster, fakeClock)
	queue = disruption.NewQueue(env.Client, recorder, cluster, fakeClock, prov)
})

var _ = AfterSuite(func() {
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
	cloudProvider.Reset()
	cloudProvider.InstanceTypes = fake.InstanceTypesAssorted()

	recorder.Reset()

	disruptionController = disruption.NewController(fakeClock, env.Client, prov, cloudProvider, recorder, cluster, queue)
	fakeClock.SetTime(time.Now())
	cluster.Reset()
	*queue = lo.FromPtr(disruption.NewQueue(env.Client, recorder, cluster, fakeClock, prov))
	cluster.MarkUnconsolidated()

	ctx = options.ToContext(ctx, test.Options())

	// Set up instance types for testing
	onDemandInstances = lo.Filter(cloudProvider.InstanceTypes, func(i *cloudprovider.InstanceType, _ int) bool {
		for _, o := range i.Offerings.Available() {
			if o.Requirements.Get(v1.CapacityTypeLabelKey).Any() == v1.CapacityTypeOnDemand {
				return true
			}
		}
		return false
	})

	// Sort by price
	for i := 0; i < len(onDemandInstances)-1; i++ {
		for j := i + 1; j < len(onDemandInstances); j++ {
			if onDemandInstances[i].Offerings.Cheapest().Price > onDemandInstances[j].Offerings.Cheapest().Price {
				onDemandInstances[i], onDemandInstances[j] = onDemandInstances[j], onDemandInstances[i]
			}
		}
	}

	leastExpensiveInstance, mostExpensiveInstance = onDemandInstances[0], onDemandInstances[len(onDemandInstances)-1]
	leastExpensiveOffering, mostExpensiveOffering = leastExpensiveInstance.Offerings[0], mostExpensiveInstance.Offerings[0]
})

var _ = AfterEach(func() {
	ExpectCleanedUp(ctx, env.Client)
})

// Helper functions for testing
func NewMethodsWithRealValidator() []disruption.Method {
	return disruption.NewMethods(fakeClock, cluster, env.Client, prov, cloudProvider, recorder, queue)
}
