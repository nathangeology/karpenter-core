#!/bin/bash

echo "Granting cluster-admin permissions to kube-scheduler..."

# Nuclear option: Add the scheduler to the cluster-admin ClusterRoleBinding
# This gives the scheduler full admin access to everything
kubectl patch clusterrolebinding cluster-admin --type='json' -p='[
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

echo "Added system:kube-scheduler to cluster-admin ClusterRoleBinding"

# Show the updated ClusterRoleBinding
echo "Updated cluster-admin ClusterRoleBinding:"
kubectl describe clusterrolebinding cluster-admin

echo "Scheduler now has cluster-admin permissions"

# Test the permissions immediately
echo "Testing scheduler permissions:"
kubectl auth can-i get nodes --as=system:kube-scheduler && echo "✅ Node access: YES" || echo "❌ Node access: NO"
kubectl auth can-i get pods --as=system:kube-scheduler && echo "✅ Pod access: YES" || echo "❌ Pod access: NO"
kubectl auth can-i get configmaps --as=system:kube-scheduler -n kube-system && echo "✅ ConfigMap access: YES" || echo "❌ ConfigMap access: NO"
kubectl auth can-i "*" "*" --as=system:kube-scheduler && echo "✅ Admin access: YES" || echo "❌ Admin access: NO"
