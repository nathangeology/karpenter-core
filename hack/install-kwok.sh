#!/usr/bin/env bash
# This script uses kustomize to generate the manifests of the latest 
# version of kwok, and will then either apply/delete the resources 
# depending on the UNINSTALL_KWOK environment variable. Set this to 
# true if you want to delete the generated objects.

# This will create 1 partition (should be expanded to multiple once https://github.com/kubernetes-sigs/kwok/issues/1122 is fixed). 
# The more partitions, the higher number of nodes that can be managed by kwok.
# Note: Beware that when using kind clusters, the total compute of nodes is limited, so 
# the higher number of partitions, the less effective each partition might be, since 
# the controller pods could be competing for limited CPU.

set -euo pipefail

# get the latest version
KWOK_REPO=kubernetes-sigs/kwok
KWOK_RELEASE=v0.5.2
# make a base directory for multi-base kustomization
HOME_DIR=$(mktemp -d)
BASE=${HOME_DIR}/base
mkdir ${BASE}

# make the alphabet, so that we can set be flexible to the number of allowed partitions (inferred max at 26)
alphabet=( {a..z} )
# set the number of partitions to 1. Currently only one partition is supported. 
num_partitions=1

# allow it to schedule to critical addons, but not schedule onto kwok nodes.
cat <<EOF > "${BASE}/tolerate-all.yaml"
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kwok-controller
  namespace: kube-system
spec:
  template:
    spec:
      tolerations:
      - operator: "Equal"
        key: CriticalAddonsOnly 
        effect: NoSchedule
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
            - matchExpressions:
              - key: kwok.x-k8s.io/node
                operator: DoesNotExist
EOF

# TODO: Simplify the kustomize to only use one copy of the RBAC that all
# controllers can use.  
cat <<EOF > "${BASE}/kustomization.yaml"
  apiVersion: kustomize.config.k8s.io/v1beta1
  kind: Kustomization
  images:
  - name: registry.k8s.io/kwok/kwok
    newTag: "${KWOK_RELEASE}"
  resources:
  - "https://github.com/${KWOK_REPO}/kustomize/kwok?ref=${KWOK_RELEASE}"
  patches:
  - path: tolerate-all.yaml
EOF

# Create num_partitions
for ((i=0; i<num_partitions; i++))
do
  SUB_LET_DIR=$HOME_DIR/${alphabet[i]}
  mkdir ${SUB_LET_DIR}

  cat <<EOF > "${SUB_LET_DIR}/patch.yaml"
  - op: replace
    path: /spec/template/spec/containers/0/args/2
    value: --manage-nodes-with-label-selector=kwok-partition=${alphabet[i]}
EOF

#cat <<EOF > "${SUB_LET_DIR}/metrics-resource.yaml"
#kind: Metric
#apiVersion: kwok.x-k8s.io/v1alpha1
#metadata:
#  name: metrics-resource
#spec:
#  path: "/metrics/nodes/{nodeName}/metrics/resource"
#  metrics:
#  - name: scrape_error
#    dimension: node
#    help: |
#      [ALPHA] 1 if there was an error while getting metrics from the node, 0 otherwise
#    kind: gauge
#    value: '0'
#  - name: container_start_time_seconds
#    dimension: container
#    help: |
#      [ALPHA] Start time of the container since unix epoch in seconds
#    kind: gauge
#    labels:
#    - name: container
#      value: 'container.name'
#    - name: namespace
#      value: 'pod.metadata.namespace'
#    - name: pod
#      value: 'pod.metadata.name'
#    value: 'pod.SinceSecond()'
#  # CPU of the container
#  - name: container_cpu_usage_seconds_total
#    dimension: container
#    help: |
#      [ALPHA] Cumulative cpu time consumed by the container in core-seconds
#    kind: counter
#    labels:
#    - name: container
#      value: 'container.name'
#    - name: namespace
#      value: 'pod.metadata.namespace'
#    - name: pod
#      value: 'pod.metadata.name'
#    value: 'pod.CumulativeUsage("cpu", container.name)'
#  # Memory of the container
#  - name: container_memory_working_set_bytes
#    dimension: container
#    help: |
#      [ALPHA] Current working set of the container in bytes
#    kind: gauge
#    labels:
#    - name: container
#      value: 'container.name'
#    - name: namespace
#      value: 'pod.metadata.namespace'
#    - name: pod
#      value: 'pod.metadata.name'
#    value: 'pod.Usage("memory", container.name)'
#  # CPU of the pod
#  - name: pod_cpu_usage_seconds_total
#    dimension: pod
#    help: |
#      [ALPHA] Cumulative cpu time consumed by the pod in core-seconds
#    kind: counter
#    labels:
#    - name: namespace
#      value: 'pod.metadata.namespace'
#    - name: pod
#      value: 'pod.metadata.name'
#    value: 'pod.CumulativeUsage("cpu")'
#  # Memory of the pod
#  - name: pod_memory_working_set_bytes
#    dimension: pod
#    help: |
#      [ALPHA] Current working set of the pod in bytes
#    kind: gauge
#    labels:
#    - name: namespace
#      value: 'pod.metadata.namespace'
#    - name: pod
#      value: 'pod.metadata.name'
#    value: 'pod.Usage("memory")'
#  # CPU of the node
#  - name: node_cpu_usage_seconds_total
#    dimension: node
#    help: |
#      [ALPHA] Cumulative cpu time consumed by the node in core-seconds
#    kind: counter
#    value: 'node.CumulativeUsage("cpu")'
#  # Memory of the node
#  - name: node_memory_working_set_bytes
#    dimension: node
#    help: |
#      [ALPHA] Current working set of the node in bytes
#    kind: gauge
#    value: 'node.Usage("memory")'
#EOF
cat <<EOF > "${SUB_LET_DIR}/kustomization.yaml"
  apiVersion: kustomize.config.k8s.io/v1beta1
  kind: Kustomization
  images:
  - name: registry.k8s.io/kwok/kwok
    newTag: "${KWOK_RELEASE}"
  resources:
#  - ./metrics-resource.yaml
  - ./../base
  nameSuffix: -${alphabet[i]}
  patches:
    - path: ${SUB_LET_DIR}/patch.yaml
      target:
        group: apps
        version: v1
        kind: Deployment
        name: kwok-controller
EOF

done

cat <<EOF > "${HOME_DIR}/kustomization.yaml"
resources:
EOF

# Create num_partitions
for ((i=0; i<num_partitions; i++))
do
  echo " - ./${alphabet[i]}" >> "${HOME_DIR}/kustomization.yaml"
done

kubectl kustomize "${HOME_DIR}" > "${HOME_DIR}/kwok.yaml"

# Set UNINSTALL_KWOK=true if you want to uninstall.
if [[ ${UNINSTALL_KWOK} = "true" ]]
then
  kubectl delete -f ${HOME_DIR}/kwok.yaml
else
  kubectl apply -f ${HOME_DIR}/kwok.yaml
fi
