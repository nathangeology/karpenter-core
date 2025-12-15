#!/bin/bash

# Script to apply scheduler strategy based on environment variable
# This script modifies test deployments to use the appropriate scheduler

set -e

# Check if SCHEDULER_STRATEGY environment variable is set
if [[ "${SCHEDULER_STRATEGY}" == "MostAllocated" ]]; then
    echo "🔧 Applying MostAllocated scheduler strategy to test deployments"
    
    # Set environment variable that Go tests can read
    export USE_MOST_ALLOCATED_SCHEDULER="true"
    export SCHEDULER_NAME="most-allocated-scheduler"
    
    echo "✅ Scheduler strategy applied: ${SCHEDULER_STRATEGY}"
    echo "   Scheduler name: ${SCHEDULER_NAME}"
    
    # Create a temporary Go file that will be imported to apply scheduler names
    cat > /tmp/scheduler_env.go <<EOF
package main

import (
    "os"
    "strings"
)

func init() {
    // This will be used by the test framework to apply scheduler names
    if os.Getenv("USE_MOST_ALLOCATED_SCHEDULER") == "true" {
        // Set a global variable that tests can check
        os.Setenv("APPLY_SCHEDULER_NAME", "most-allocated-scheduler")
    }
}
EOF
    
else
    echo "ℹ️  Using default scheduler (LeastAllocated strategy)"
    export USE_MOST_ALLOCATED_SCHEDULER="false"
    export SCHEDULER_NAME=""
    export APPLY_SCHEDULER_NAME=""
fi

# Execute the original command with the modified environment
exec "$@"
