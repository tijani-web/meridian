#!/usr/bin/env bash
# deploy/kind/label-nodes.sh
#
# Applies Meridian topology labels to kind worker nodes.
# The kind cluster config already sets these as node labels at creation time;
# this script is a belt-and-suspenders idempotent re-apply that works even
# when kind drops the labels (observed behavior in some kind versions).
#
# Usage: KUBECONFIG=<path> ./deploy/kind/label-nodes.sh
# Called by: make kind-up, CI e2e setup

set -euo pipefail

KUBECTL=${KUBECTL:-kubectl}

echo "Applying topology labels to kind worker nodes..."

# Get worker node names in order
WORKERS=($($KUBECTL get nodes --selector='!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[*].metadata.name}'))

if [[ ${#WORKERS[@]} -lt 4 ]]; then
  echo "ERROR: Expected at least 4 worker nodes, got ${#WORKERS[@]}"
  exit 1
fi

# Worker 0: domain-a, rack-0, us-east-1a, h100
$KUBECTL label node "${WORKERS[0]}" \
  meridian.dev/nvlink-domain=domain-a \
  meridian.dev/rack=rack-0 \
  topology.kubernetes.io/zone=us-east-1a \
  meridian.dev/gpu-type=nvidia-h100 \
  --overwrite

# Worker 1: domain-a, rack-0, us-east-1a, h100 (same domain as 0)
$KUBECTL label node "${WORKERS[1]}" \
  meridian.dev/nvlink-domain=domain-a \
  meridian.dev/rack=rack-0 \
  topology.kubernetes.io/zone=us-east-1a \
  meridian.dev/gpu-type=nvidia-h100 \
  --overwrite

# Worker 2: domain-b, rack-1, us-east-1b, h100
$KUBECTL label node "${WORKERS[2]}" \
  meridian.dev/nvlink-domain=domain-b \
  meridian.dev/rack=rack-1 \
  topology.kubernetes.io/zone=us-east-1b \
  meridian.dev/gpu-type=nvidia-h100 \
  --overwrite

# Worker 3: domain-b, rack-1, us-east-1b, a100 (different GPU type)
$KUBECTL label node "${WORKERS[3]}" \
  meridian.dev/nvlink-domain=domain-b \
  meridian.dev/rack=rack-1 \
  topology.kubernetes.io/zone=us-east-1b \
  meridian.dev/gpu-type=nvidia-a100 \
  --overwrite

echo "Topology labels applied:"
$KUBECTL get nodes -o custom-columns=\
NAME:.metadata.name,\
NVLINK:.metadata.labels."meridian\.dev/nvlink-domain",\
RACK:.metadata.labels."meridian\.dev/rack",\
ZONE:.metadata.labels."topology\.kubernetes\.io/zone",\
GPU:.metadata.labels."meridian\.dev/gpu-type"
