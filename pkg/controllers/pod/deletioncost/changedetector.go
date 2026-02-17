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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"sigs.k8s.io/karpenter/pkg/controllers/state"
)

// ChangeDetector detects changes in cluster state to optimize ranking operations
type ChangeDetector struct {
	lastHash string
}

// NewChangeDetector creates a new change detector
func NewChangeDetector() *ChangeDetector {
	return &ChangeDetector{
		lastHash: "",
	}
}

// HasChanged checks if the node state has changed since the last check
func (c *ChangeDetector) HasChanged(nodes []*state.StateNode) bool {
	currentHash := c.computeHash(nodes)

	if c.lastHash == "" {
		// First call, always consider it changed
		c.lastHash = currentHash
		return true
	}

	if currentHash != c.lastHash {
		c.lastHash = currentHash
		return true
	}

	return false
}

// computeHash computes a hash of the current node state
func (c *ChangeDetector) computeHash(nodes []*state.StateNode) string {
	if len(nodes) == 0 {
		return "empty"
	}

	// Collect node information
	nodeInfos := make([]string, 0, len(nodes))
	for _, node := range nodes {
		info := c.getNodeInfo(node)
		nodeInfos = append(nodeInfos, info)
	}

	// Sort for deterministic hashing
	sort.Strings(nodeInfos)

	// Compute hash
	hasher := sha256.New()
	for _, info := range nodeInfos {
		hasher.Write([]byte(info))
		hasher.Write([]byte("\n"))
	}

	return hex.EncodeToString(hasher.Sum(nil))
}

// getNodeInfo extracts relevant information from a node for hashing
func (c *ChangeDetector) getNodeInfo(node *state.StateNode) string {
	if node == nil {
		return "nil"
	}

	var nodeName string
	var creationTime string
	var capacity string
	var podCount int

	if node.Node != nil {
		nodeName = node.Node.Name
		creationTime = node.Node.CreationTimestamp.String()

		// Get capacity
		allocatable := node.Node.Status.Allocatable
		capacity = fmt.Sprintf("cpu:%s,mem:%s",
			allocatable.Cpu().String(),
			allocatable.Memory().String())

		// Pod count would be calculated from actual pods in real implementation
		podCount = 0
	} else if node.NodeClaim != nil {
		nodeName = node.NodeClaim.Name
		creationTime = node.NodeClaim.CreationTimestamp.String()
		capacity = "unknown"
		podCount = 0
	}

	return fmt.Sprintf("%s|%s|%s|pods:%d", nodeName, creationTime, capacity, podCount)
}
