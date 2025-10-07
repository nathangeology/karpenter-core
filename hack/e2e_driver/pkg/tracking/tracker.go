package tracking

import (
	"sync"
	"time"
)

// ResourceEvent represents a single event in a resource's lifecycle
type ResourceEvent struct {
	Timestamp   time.Time   `json:"timestamp"`
	Action      string      `json:"action"` // create, update, delete
	ResourceObj interface{} `json:"resource"`
}

// ResourceHistory stores the complete history of a resource
type ResourceHistory struct {
	ResourceType string          `json:"resource_type"` // deployment, pod, node, etc.
	Name         string          `json:"name"`
	Namespace    string          `json:"namespace,omitempty"`
	Events       []ResourceEvent `json:"events"`
}

// ResourceTracker tracks all resources and their lifecycle events
type ResourceTracker struct {
	mutex     sync.RWMutex
	history   map[string]*ResourceHistory // key: "type/namespace/name"
	startTime time.Time
}

// NewResourceTracker creates a new resource tracker
func NewResourceTracker() *ResourceTracker {
	return &ResourceTracker{
		history:   make(map[string]*ResourceHistory),
		startTime: time.Now(),
	}
}

// TrackResource records an event for a resource
func (rt *ResourceTracker) TrackResource(resourceType, name, namespace, action string, obj interface{}) {
	rt.mutex.Lock()
	defer rt.mutex.Unlock()

	key := buildResourceKey(resourceType, namespace, name)

	event := ResourceEvent{
		Timestamp:   time.Now(),
		Action:      action,
		ResourceObj: obj,
	}

	if history, exists := rt.history[key]; exists {
		history.Events = append(history.Events, event)
	} else {
		rt.history[key] = &ResourceHistory{
			ResourceType: resourceType,
			Name:         name,
			Namespace:    namespace,
			Events:       []ResourceEvent{event},
		}
	}
}

// GetHistory returns the complete history of all tracked resources
func (rt *ResourceTracker) GetHistory() map[string]*ResourceHistory {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()

	// Create a deep copy of the history
	historyCopy := make(map[string]*ResourceHistory, len(rt.history))
	for key, resource := range rt.history {
		historyCopy[key] = resource
	}

	return historyCopy
}

// GetHistoryByType returns the history of resources organized by type
func (rt *ResourceTracker) GetHistoryByType() map[string]map[string]*ResourceHistory {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()

	// Organize by resource type
	result := make(map[string]map[string]*ResourceHistory)

	for key, resource := range rt.history {
		if _, exists := result[resource.ResourceType]; !exists {
			result[resource.ResourceType] = make(map[string]*ResourceHistory)
		}
		result[resource.ResourceType][key] = resource
	}

	return result
}

// GetResourceTypes returns a list of all tracked resource types
func (rt *ResourceTracker) GetResourceTypes() []string {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()

	types := make(map[string]struct{})
	for _, resource := range rt.history {
		types[resource.ResourceType] = struct{}{}
	}

	result := make([]string, 0, len(types))
	for t := range types {
		result = append(result, t)
	}

	return result
}

// GetResourceCount returns the total number of tracked resources
func (rt *ResourceTracker) GetResourceCount() int {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()

	return len(rt.history)
}

// GetEventCount returns the total number of tracked events
func (rt *ResourceTracker) GetEventCount() int {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()

	count := 0
	for _, resource := range rt.history {
		count += len(resource.Events)
	}

	return count
}

// GetRunDuration returns the duration since the tracker was created
func (rt *ResourceTracker) GetRunDuration() time.Duration {
	return time.Since(rt.startTime)
}

// Helper function to build a unique key for a resource
func buildResourceKey(resourceType, namespace, name string) string {
	if namespace != "" {
		return resourceType + "/" + namespace + "/" + name
	}
	return resourceType + "/" + name
}
