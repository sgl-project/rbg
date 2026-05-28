#!/bin/bash
# Deploy kwok into an existing Kubernetes cluster for stress testing.
# This installs kwok controller + stage-fast via Helm, then creates
# fake nodes that simulate Pod Ready without real containers.
set -euo pipefail

KWOK_NAMESPACE="${KWOK_NAMESPACE:-kube-system}"
FAKE_NODE_COUNT="${FAKE_NODE_COUNT:-5}"
FAKE_NODE_CPU="${FAKE_NODE_CPU:-128}"
FAKE_NODE_MEMORY="${FAKE_NODE_MEMORY:-512Gi}"
FAKE_NODE_PODS="${FAKE_NODE_PODS:-1000}"

echo "=== RBG Stress Test: In-Cluster KWOK Setup ==="
echo "Namespace: ${KWOK_NAMESPACE}"
echo "Fake nodes: ${FAKE_NODE_COUNT} (each ${FAKE_NODE_CPU} CPU, ${FAKE_NODE_MEMORY} memory, ${FAKE_NODE_PODS} pods)"
echo ""

# 1. Add kwok Helm repo
echo "[1/4] Adding kwok Helm repo..."
helm repo add kwok https://kwok.sigs.k8s.io/charts 2>/dev/null || true
helm repo update kwok

# 2. Install kwok controller (skip if already installed)
echo "[2/4] Installing kwok controller..."
if helm status kwok -n "${KWOK_NAMESPACE}" >/dev/null 2>&1; then
    echo "  kwok already installed, skipping"
else
    helm install kwok kwok/kwok --namespace "${KWOK_NAMESPACE}"
fi

# 3. Install stage-fast (Pod simulation stages)
echo "[3/4] Installing kwok stage-fast..."
if helm status kwok-stage-fast -n "${KWOK_NAMESPACE}" >/dev/null 2>&1; then
    echo "  stage-fast already installed, skipping"
else
    helm install kwok-stage-fast kwok/stage-fast --namespace "${KWOK_NAMESPACE}"
fi

# Wait for kwok controller to be ready
echo "  Waiting for kwok controller..."
kubectl wait --for=condition=Ready pod -l app.kubernetes.io/name=kwok -n "${KWOK_NAMESPACE}" --timeout=60s 2>/dev/null || \
kubectl wait --for=condition=Ready pod -l app=kwok-controller -n "${KWOK_NAMESPACE}" --timeout=60s 2>/dev/null || true

# 4. Create fake nodes
echo "[4/4] Creating ${FAKE_NODE_COUNT} fake nodes..."
for i in $(seq 0 $((FAKE_NODE_COUNT - 1))); do
    NODE_NAME="kwok-node-${i}"
    if kubectl get node "${NODE_NAME}" >/dev/null 2>&1; then
        echo "  ${NODE_NAME} already exists, skipping"
        continue
    fi
    cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Node
metadata:
  name: ${NODE_NAME}
  annotations:
    node.alpha.kubernetes.io/ttl: "0"
    kwok.x-k8s.io/node: fake
  labels:
    type: kwok
    beta.kubernetes.io/os: linux
    kubernetes.io/os: linux
    kubernetes.io/arch: amd64
    node.kubernetes.io/instance-type: kwok
spec:
  taints:
    - key: kwok.x-k8s.io/node
      value: fake
      effect: NoSchedule
status:
  allocatable:
    cpu: "${FAKE_NODE_CPU}"
    memory: "${FAKE_NODE_MEMORY}"
    pods: "${FAKE_NODE_PODS}"
  capacity:
    cpu: "${FAKE_NODE_CPU}"
    memory: "${FAKE_NODE_MEMORY}"
    pods: "${FAKE_NODE_PODS}"
  conditions:
    - type: Ready
      status: "True"
      reason: KwokReady
      message: "kwok fake node is ready"
      lastHeartbeatTime: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      lastTransitionTime: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
EOF
    echo "  Created ${NODE_NAME}"
done

echo ""
echo "=== KWOK setup complete ==="
echo "Fake nodes:"
kubectl get nodes -l type=kwok --no-headers
echo ""
echo "Stages:"
kubectl get stages --no-headers
echo ""
echo "To teardown: bash $(dirname "$0")/teardown-kwok.sh"
