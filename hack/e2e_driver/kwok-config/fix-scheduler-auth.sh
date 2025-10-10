#!/bin/bash

echo "Fixing kube-scheduler authentication configuration access..."

# The scheduler needs to read the extension-apiserver-authentication ConfigMap
# to authenticate properly. Without this, it falls back to anonymous access.

# Add ConfigMap permissions to the system:kube-scheduler ClusterRole
kubectl patch clusterrole system:kube-scheduler --type='json' -p='[
  {
    "op": "add",
    "path": "/rules/-",
    "value": {
      "apiGroups": [""],
      "resources": ["configmaps"],
      "resourceNames": ["extension-apiserver-authentication"],
      "verbs": ["get", "list", "watch"]
    }
  }
]'

echo "Added ConfigMap permissions to system:kube-scheduler ClusterRole"

# Also create a specific RoleBinding for the authentication reader role
# This is what the scheduler logs suggest as the fix
kubectl create rolebinding kube-scheduler-auth-reader \
  -n kube-system \
  --role=extension-apiserver-authentication-reader \
  --user=system:kube-scheduler \
  --dry-run=client -o yaml | kubectl apply -f -

echo "Created RoleBinding for extension-apiserver-authentication-reader"

# Show the updated ClusterRole
echo "Updated ClusterRole rules:"
kubectl describe clusterrole system:kube-scheduler | grep -A20 "PolicyRule"

echo "Authentication fix applied successfully"
