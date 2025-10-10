#!/bin/bash

echo "Patching system:kube-scheduler ClusterRole to add missing permissions..."

# Patch the existing system:kube-scheduler ClusterRole to add missing permissions
kubectl patch clusterrole system:kube-scheduler --type='json' -p='[
  {
    "op": "add",
    "path": "/rules/-",
    "value": {
      "apiGroups": [""],
      "resources": ["nodes"],
      "verbs": ["get", "list", "watch"]
    }
  },
  {
    "op": "add", 
    "path": "/rules/-",
    "value": {
      "apiGroups": [""],
      "resources": ["pods"],
      "verbs": ["get", "list", "watch"]
    }
  },
  {
    "op": "add",
    "path": "/rules/-", 
    "value": {
      "apiGroups": [""],
      "resources": ["services"],
      "verbs": ["get", "list", "watch"]
    }
  },
  {
    "op": "add",
    "path": "/rules/-",
    "value": {
      "apiGroups": ["apps"],
      "resources": ["replicasets", "statefulsets"],
      "verbs": ["get", "list", "watch"]
    }
  },
  {
    "op": "add",
    "path": "/rules/-",
    "value": {
      "apiGroups": ["policy"],
      "resources": ["poddisruptionbudgets"],
      "verbs": ["get", "list", "watch"]
    }
  },
  {
    "op": "add",
    "path": "/rules/-",
    "value": {
      "apiGroups": ["storage.k8s.io"],
      "resources": ["storageclasses", "csinodes", "csidrivers", "csistoragecapacities", "volumeattachments"],
      "verbs": ["get", "list", "watch"]
    }
  }
]'

echo "Patched system:kube-scheduler ClusterRole successfully"

# Show the updated ClusterRole
echo "Updated ClusterRole rules:"
kubectl describe clusterrole system:kube-scheduler
