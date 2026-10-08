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

package nodepoolhealth

import (
	"math"
	"sync"

	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/karpenter/pkg/utils/ringbuffer"
)

const (
	BufferSize     = 4
	ThresholdFalse = 0.5 // 50% of 0s for NodeRegistrationHealthy=False
)

type Status int

const (
	StatusUnknown Status = iota
	StatusHealthy
	StatusUnhealthy
)

type Tracker struct {
	sync.RWMutex
	buffer ringbuffer.RingBuffer[bool]
}

// NewTracker returns a Tracker over a window of the given number of launch results. Status
// weighs the failures in the window against the whole window, so a capacity of 0 holds nothing
// and stays StatusUnknown.
func NewTracker(capacity int) *Tracker {
	return &Tracker{
		buffer: *ringbuffer.New[bool](capacity),
	}
}

func (t *Tracker) Update(success bool) {
	t.Lock()
	defer t.Unlock()

	t.buffer.Insert(success)
}

func (t *Tracker) Reset() {
	t.Lock()
	defer t.Unlock()

	t.buffer.Reset()
}

func (t *Tracker) Status() Status {
	t.RLock()
	defer t.RUnlock()

	if t.buffer.Len() == 0 {
		return StatusUnknown
	}
	// Count number of true and false
	var unhealthyCount int
	for _, value := range t.buffer.Items() {
		if !value {
			unhealthyCount++
		}
	}
	// Determine health status based on threshold. The divisor is the window this tracker was
	// built with rather than the number of entries in it, so that a single failure in a partly
	// filled window stays below the threshold.
	if (float64(unhealthyCount) / float64(t.buffer.Cap())) >= ThresholdFalse {
		return StatusUnhealthy
	} else {
		return StatusHealthy
	}
}

func (t *Tracker) SetStatus(status Status) {
	t.Lock()
	defer t.Unlock()

	switch status {
	case StatusUnknown:
		t.buffer.Reset()
	case StatusHealthy:
		t.buffer.Reset()
		t.buffer.Insert(true)
	case StatusUnhealthy:
		t.buffer.Reset()
		// Round up, so that a window whose threshold falls between two entries still reaches it
		// rather than landing just below.
		for range int(math.Ceil(float64(t.buffer.Cap()) * ThresholdFalse)) {
			t.buffer.Insert(false)
		}
	}
}

type State struct {
	sync.RWMutex
	trackers map[types.UID]*Tracker
}

func NewState() *State {
	return &State{
		trackers: make(map[types.UID]*Tracker),
	}
}

func (s *State) nodePoolNodeRegistration(nodePoolUID types.UID) *Tracker {
	s.RLock()
	tracker, exists := s.trackers[nodePoolUID]
	s.RUnlock()

	if !exists {
		s.Lock()
		// Double-check after acquiring write lock
		if tracker, exists = s.trackers[nodePoolUID]; !exists {
			tracker = NewTracker(BufferSize)
			s.trackers[nodePoolUID] = tracker
		}
		s.Unlock()
	}
	return tracker
}

func (s *State) Status(nodePoolUID types.UID) Status {
	return s.nodePoolNodeRegistration(nodePoolUID).Status()
}

func (s *State) Update(nodePoolUID types.UID, launchStatus bool) {
	s.nodePoolNodeRegistration(nodePoolUID).Update(launchStatus)
}

func (s *State) SetStatus(nodePoolUID types.UID, status Status) {
	s.nodePoolNodeRegistration(nodePoolUID).SetStatus(status)
}

func (s *State) DryRun(nodePoolUID types.UID, launchStatus bool) *Tracker {
	trackerCopy := NewTracker(BufferSize)
	originalTracker := s.nodePoolNodeRegistration(nodePoolUID)

	originalTracker.RLock()
	for _, item := range originalTracker.buffer.Items() {
		trackerCopy.buffer.Insert(item)
	}
	originalTracker.RUnlock()

	trackerCopy.Update(launchStatus)
	return trackerCopy
}
