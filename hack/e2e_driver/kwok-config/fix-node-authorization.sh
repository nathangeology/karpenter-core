#!/bin/bash

echo "Fixing Node authorization issue for kube-scheduler..."

# The API server is using --authorization-mode=Node,RBAC
# Node authorization runs first and blocks the scheduler before RBAC can grant permissions
# We need to add the scheduler to node authorization bypass rules

# Add the scheduler to the kubeadm:get-nodes ClusterRoleBinding
# This allows the scheduler to bypass Node authorization for node access
kubectl patch clusterrolebinding kubeadm:get-nodes --type='json' -p='[
  {
    "op": "add",
    "path": "/subjects/-",
    "value": {
      "kind": "User",
      "name": "system:kube-scheduler",
      "apiGroup": "rbac.authorization.k8s.io"
    }
  }
]'

echo "Added system:kube-scheduler to kubeadm:get-nodes ClusterRoleBinding"

# Also add the scheduler to the system:node ClusterRoleBinding for broader node access
# The system:node binding has no subjects, so we need to create the subjects array
kubectl patch clusterrolebinding system:node --type='json' -p='[
  {
    "op": "replace", 
    "path": "/subjects",
    "value": [
      {
        "kind": "User",
        "name": "system:kube-scheduler",
        "apiGroup": "rbac.authorization.k8s.io"
      }
    ]
  }
]'

echo "Added system:kube-scheduler to system:node ClusterRoleBinding (created subjects array)"

# Show the updated ClusterRoleBindings
echo "Updated kubeadm:get-nodes ClusterRoleBinding:"
kubectl describe clusterrolebinding kubeadm:get-nodes

echo "Updated system:node ClusterRoleBinding:"
kubectl describe clusterrolebinding system:node

echo "Node authorization fix applied successfully"
