#!/usr/bin/env bash
# generate-fleet.sh generates KWOK simulated GPU node manifests.
# Usage: ./generate-fleet.sh [node_count] > nodes.yaml
set -euo pipefail

COUNT=${1:-500}

cat <<EOF
# Generated simulated KWOK GPU fleet ($COUNT nodes)
# Every simulated result must be labeled "simulated" per AGENTS.md §1.1
EOF

for i in $(seq 1 "$COUNT"); do
  INDEX=$(printf "%04d" "$i")
  DOMAIN=$(( (i - 1) / 8 ))
  RACK=$(( (i - 1) / 32 ))
  ZONE="us-east-1a"
  if [ $((i % 2)) -eq 0 ]; then
    ZONE="us-east-1b"
  fi
  MODEL="nvidia-h100"
  if [ $((i % 4)) -eq 0 ]; then
    MODEL="nvidia-a100"
  fi

  cat <<EOF
---
apiVersion: v1
kind: Node
metadata:
  name: kwok-gpu-node-$INDEX
  annotations:
    kwok.x-k8s.io/node: fake
    node.alpha.kubernetes.io/ttl: "0"
  labels:
    type: kwok
    meridian.dev/simulation: "true"
    meridian.dev/gpu-model: $MODEL
    meridian.dev/nvlink-domain: domain-$DOMAIN
    meridian.dev/rack: rack-$RACK
    topology.kubernetes.io/zone: $ZONE
    kubernetes.io/hostname: kwok-gpu-node-$INDEX
    kubernetes.io/os: linux
    kubernetes.io/arch: amd64
spec:
  taints: []
status:
  allocatable:
    cpu: "64"
    memory: "512Gi"
    pods: "110"
    nvidia.com/gpu: "8"
  capacity:
    cpu: "64"
    memory: "512Gi"
    pods: "110"
    nvidia.com/gpu: "8"
  conditions:
    - type: Ready
      status: "True"
      reason: KwokNodeReady
      message: KWOK simulated GPU node ready
    - type: meridian.dev/GPUHealthy
      status: "True"
      reason: SimulatedHealthy
      message: All 8 GPUs healthy
  nodeInfo:
    kubeletVersion: v1.33.1
    operatingSystem: linux
    architecture: amd64
EOF
done
