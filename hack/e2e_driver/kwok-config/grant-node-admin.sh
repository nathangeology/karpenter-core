#!/bin/bash

echo "Granting node-level admin permissions for control plane..."

# Grant admin permissions to the control plane node itself
kubectl patch clusterrolebinding cluster-admin --type='json' -p='[
  {
    "op": "add",
    "path": "/subjects/-",
    "value": {
      "kind": "Node",
      "name": "chart-testing-control-plane",
      "apiGroup": ""
    }
  }
]'

echo "Added control plane node to cluster-admin ClusterRoleBinding"

# Grant admin permissions to the system:nodes group (all nodes)
kubectl patch clusterrolebinding cluster-admin --type='json' -p='[
  {
    "op": "add", 
    "path": "/subjects/-",
    "value": {
      "kind": "Group",
      "name": "system:nodes",
      "apiGroup": "rbac.authorization.k8s.io"
    }
  }
]'

echo "Added system:nodes group to cluster-admin ClusterRoleBinding"

# Grant admin permissions to system:node-proxier (node-level identity)
kubectl patch clusterrolebinding cluster-admin --type='json' -p='[
  {
    "op": "add",
    "path": "/subjects/-", 
    "value": {
      "kind": "User",
      "name": "system:node-proxier",
      "apiGroup": "rbac.authorization.k8s.io"
    }
  }
]'

echo "Added system:node-proxier to cluster-admin ClusterRoleBinding"

# Grant admin permissions to kubelet identity
kubectl patch clusterrolebinding cluster-admin --type='json' -p='[
  {
    "op": "add",
    "path": "/subjects/-",
    "value": {
      "kind": "User", 
      "name": "system:node:chart-testing-control-plane",
      "apiGroup": "rbac.authorization.k8s.io"
    }
  }
]'

echo "Added kubelet node identity to cluster-admin ClusterRoleBinding"

# Show the updated ClusterRoleBinding
echo "Updated cluster-admin ClusterRoleBinding:"
kubectl describe clusterrolebinding cluster-admin

echo "Node-level admin permissions granted successfully"

# Test node-level permissions
echo "Testing node-level permissions:"
kubectl auth can-i get nodes --as=system:node:chart-testing-control-plane && echo "✅ Node identity access: YES" || echo "❌ Node identity access: NO"
kubectl auth can-i get pods --as=system:node:chart-testing-control-plane && echo "✅ Node pod access: YES" || echo "❌ Node pod access: NO"
kubectl auth can-i "*" "*" --as=system:node:chart-testing-control-plane && echo "✅ Node admin access: YES" || echo "❌ Node admin access: NO"
