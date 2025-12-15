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

package test

import "os"

// getSchedulerFromEnv returns the scheduler name from environment variables
func getSchedulerFromEnv() string {
	// Check if we should use the MostAllocated scheduler
	if os.Getenv("USE_MOST_ALLOCATED_SCHEDULER") == "true" {
		return "most-allocated-scheduler"
	}

	// Check for explicit scheduler name
	if schedulerName := os.Getenv("SCHEDULER_NAME"); schedulerName != "" {
		return schedulerName
	}

	// Default to empty string (use default scheduler)
	return ""
}
