cat <<EOF | envsubst | kubectl apply -f -
apiVersion: kwok.x-k8s.io/v1alpha1
kind: Stage
metadata:
  name: pod-delete
spec:
  resourceRef:
    apiGroup: v1
    kind: Pod
  selector:
    matchExpressions:
    - key: '.metadata.deletionTimestamp'
      operator: 'Exists'
    - key: '.metadata.finalizers'
      operator: 'DoesNotExist'
  weight: 1
  delay:
    durationMilliseconds: 5000
    jitterDurationMilliseconds: 1000
  next:
    delete: true
EOF
