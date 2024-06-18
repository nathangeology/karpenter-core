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

package experimental_st_controller

import (
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/controllers/disruption/orchestration"
	"sigs.k8s.io/karpenter/pkg/controllers/provisioning"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/events"
	"sync"
	"time"
)

// Should conform to both disruption and consolidation
// The provisioner takes requests from the node and pod controllers
// The disruption controller is a sort of root controller that triggers itself on a clock
// The main controllers.go initializes the disruption, node, and pod controllers.
// The node/pod controllers create and reference the 'provisioner'

type Controller struct {
	queue         *orchestration.Queue
	kubeClient    client.Client
	cluster       *state.Cluster
	provisioner   *provisioning.Provisioner
	recorder      events.Recorder
	clock         clock.Clock
	cloudProvider cloudprovider.CloudProvider
	methods       []Method
	mu            sync.Mutex
	lastRun       map[string]time.Time
	// batcher -- Provisioner needs this
	// volume topology -- same
	// change monitor -- same
}

// Methods to implement

// newController
func NewController(clk clock.Clock, kubeClient client.Client, provisioner *provisioning.Provisioner,
	cp cloudprovider.CloudProvider, recorder events.Recorder, cluster *state.Cluster, queue *orchestration.Queue,
) *Controller {
	return &Controller{
		// TODO: Implement This
	}
}

// NOTE: The above has some methods
// that get passed in when the controller
// is created that could potentially be overridden for some alternate approaches.

// Name

// Builder

// Reconcile

// disrupt

// executeCommand

// createReplacementNodeClaims

// log invalid budgets
