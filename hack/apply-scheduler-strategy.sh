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
else
    echo "ℹ️  Using default scheduler (LeastAllocated strategy)"
    export USE_MOST_ALLOCATED_SCHEDULER="false"
    export SCHEDULER_NAME=""
fi

# Execute the original command with the modified environment
exec "$@"
