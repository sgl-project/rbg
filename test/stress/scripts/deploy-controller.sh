#!/bin/bash
# Deploy RBG controller to cluster via Helm with stress-test configuration.
# Configures resources, pprof, max-concurrent-reconciles, and kube-api QPS/burst.
#
# Image settings:
#   IMAGE_TAG    tag of the images. When the script builds them, the default is a tag unique to
#                the build, stress-<commit>-<UTC time>: no node can run an older image cached under
#                that tag (the chart pulls IfNotPresent), and Helm always rolls the controller.
#                Without a build the default is latest.
#   IMAGE_REPO   image name prefix, as IMG_REPO in the Makefile (e.g. registry.example.com/team).
#                When set, the controller and the CRD upgrader use
#                ${IMAGE_REPO}/rbgs-controller:${IMAGE_TAG} and ${IMAGE_REPO}/rbgs-upgrade-crd:${IMAGE_TAG}.
#                When unset, the chart's repositories are kept and only the controller tag is set.
#   BUILD_IMAGE  build the images from this checkout first (default true)
#   PUSH_IMAGE   true builds both images for linux/amd64 and linux/arm64 and pushes them to
#                IMAGE_REPO (make docker-buildx-push), which a remote cluster's nodes need. It
#                refuses IMAGE_TAG=latest.
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"

# Controller resource configuration
CONTROLLER_CPU="${CONTROLLER_CPU:-8}"
CONTROLLER_MEMORY="${CONTROLLER_MEMORY:-16Gi}"
CONTROLLER_CPU_REQUEST="${CONTROLLER_CPU_REQUEST:-${CONTROLLER_CPU}}"
CONTROLLER_MEMORY_REQUEST="${CONTROLLER_MEMORY_REQUEST:-${CONTROLLER_MEMORY}}"

# Controller runtime parameters
MAX_RECONCILES="${MAX_RECONCILES:-10}"
KUBE_API_QPS="${KUBE_API_QPS:-50}"
KUBE_API_BURST="${KUBE_API_BURST:-100}"

# Pprof configuration
PPROF_ENABLED="${PPROF_ENABLED:-true}"
PPROF_PORT="${PPROF_PORT:-6060}"

# Deployment settings
NAMESPACE="${NAMESPACE:-rbgs-system}"
RELEASE_NAME="${RELEASE_NAME:-rbgs}"
BUILD_IMAGE="${BUILD_IMAGE:-true}"
if [ -z "${IMAGE_TAG:-}" ]; then
    if [ "${BUILD_IMAGE}" = "true" ]; then
        IMAGE_TAG="stress-$(git -C "${PROJECT_ROOT}" rev-parse --short HEAD 2>/dev/null || echo local)-$(date -u +%Y%m%d%H%M%S)"
    else
        IMAGE_TAG=latest
    fi
fi
IMAGE_REPO="${IMAGE_REPO:-}"
PUSH_IMAGE="${PUSH_IMAGE:-false}"
REPLICAS="${REPLICAS:-1}"
CONTROLLER_IMAGE="${IMAGE_REPO:-rolebasedgroup}/rbgs-controller:${IMAGE_TAG}"
CRD_UPGRADE_IMAGE="${IMAGE_REPO}/rbgs-upgrade-crd:${IMAGE_TAG}"

echo "=== RBG Stress Test: Deploy Controller ==="
echo "Started: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "Image: ${CONTROLLER_IMAGE}"
echo "Resources: CPU=${CONTROLLER_CPU}, Memory=${CONTROLLER_MEMORY}"
echo "Params: max-reconciles=${MAX_RECONCILES}, qps=${KUBE_API_QPS}, burst=${KUBE_API_BURST}"
echo "Pprof: enabled=${PPROF_ENABLED}, port=${PPROF_PORT}"
echo ""

# Build controller image if needed
if [ "${BUILD_IMAGE}" = "true" ]; then
    cd "${PROJECT_ROOT}"
    if [ "${PUSH_IMAGE}" = "true" ]; then
        if [ -z "${IMAGE_REPO}" ]; then
            echo "ERROR: PUSH_IMAGE=true needs IMAGE_REPO, a registry the cluster's nodes can pull from" >&2
            exit 1
        fi
        if [ "${IMAGE_TAG}" = "latest" ]; then
            echo "ERROR: PUSH_IMAGE=true refuses IMAGE_TAG=latest: nodes that cached an older latest keep" >&2
            echo "ERROR: running it, and the push can replace an image others use. Leave IMAGE_TAG unset." >&2
            exit 1
        fi
        echo "[1/4] Building and pushing the controller and CRD upgrader images to ${IMAGE_REPO}..."
        make docker-buildx-push IMG_REPO="${IMAGE_REPO}" TAG="${IMAGE_TAG}" 2>&1 | tail -5
    else
        echo "[1/4] Building controller image..."
        make docker-build IMG_REPO="${IMAGE_REPO:-rolebasedgroup}" TAG="${IMAGE_TAG}" 2>&1 | tail -5

        # Load image into kwok cluster if using kwok
        if kwokctl get clusters 2>/dev/null | grep -q "${CLUSTER_NAME:-rbg-stress}"; then
            echo "  Loading images into kwok cluster..."
            kwokctl --name="${CLUSTER_NAME:-rbg-stress}" load docker-image "${CONTROLLER_IMAGE}" 2>/dev/null || true
            # The chart runs the CRD upgrader built here only when IMAGE_REPO is set.
            if [ -n "${IMAGE_REPO}" ]; then
                kwokctl --name="${CLUSTER_NAME:-rbg-stress}" load docker-image "${CRD_UPGRADE_IMAGE}" 2>/dev/null || true
            fi
        else
            echo "  WARNING: the image was built only locally and not pushed. The nodes of a remote cluster" >&2
            echo "  WARNING: pull ${CONTROLLER_IMAGE} from its registry instead: an older image with that tag," >&2
            echo "  WARNING: or ImagePullBackOff. To test this build, set PUSH_IMAGE=true and IMAGE_REPO." >&2
        fi
    fi
else
    echo "[1/4] Skipping image build (BUILD_IMAGE=false)"
fi

image_sets=(--set controller.image.tag="${IMAGE_TAG}")
if [ -n "${IMAGE_REPO}" ]; then
    image_sets+=(--set controller.image.repository="${IMAGE_REPO}/rbgs-controller"
        --set crdUpgrade.image.repository="${IMAGE_REPO}/rbgs-upgrade-crd"
        --set crdUpgrade.image.tag="${IMAGE_TAG}")
fi

# Deploy via Helm
echo "[2/4] Deploying controller via Helm..."
helm upgrade --install "${RELEASE_NAME}" "${PROJECT_ROOT}/deploy/helm/rbgs" \
    --create-namespace \
    --namespace "${NAMESPACE}" \
    "${image_sets[@]}" \
    --set controller.replicaCount="${REPLICAS}" \
    --set controller.resources.limits.cpu="${CONTROLLER_CPU}" \
    --set controller.resources.limits.memory="${CONTROLLER_MEMORY}" \
    --set controller.resources.requests.cpu="${CONTROLLER_CPU_REQUEST}" \
    --set controller.resources.requests.memory="${CONTROLLER_MEMORY_REQUEST}" \
    --set controller.tuning.maxConcurrentReconciles="${MAX_RECONCILES}" \
    --set controller.tuning.kubeApiQPS="${KUBE_API_QPS}" \
    --set controller.tuning.kubeApiBurst="${KUBE_API_BURST}" \
    --set controller.pprof.enabled="${PPROF_ENABLED}" \
    --set controller.pprof.bindAddress=":${PPROF_PORT}" \
    --set controller.pprof.containerPort="${PPROF_PORT}" \
    --wait --timeout=120s

# Wait for rollout
echo "[3/4] Waiting for controller rollout..."
kubectl rollout status deploy/rbgs-controller-manager -n "${NAMESPACE}" --timeout=120s

# Setup port-forward for pprof
if [ "${PPROF_ENABLED}" = "true" ]; then
    echo "[4/4] Setting up pprof port-forward..."
    # Kill existing port-forward if any
    pkill -f "port-forward.*rbgs-controller-manager.*${PPROF_PORT}" 2>/dev/null || true
    sleep 1
    kubectl port-forward -n "${NAMESPACE}" deploy/rbgs-controller-manager "${PPROF_PORT}:${PPROF_PORT}" &
    PF_PID=$!
    echo "${PF_PID}" > /tmp/rbg-stress-pprof-pf.pid
    sleep 2

    # Verify pprof is accessible
    if curl -s "http://localhost:${PPROF_PORT}/debug/pprof/" >/dev/null 2>&1; then
        echo "  Pprof accessible at http://localhost:${PPROF_PORT}/debug/pprof/"
    else
        echo "  WARNING: Pprof not accessible yet, may need a moment"
    fi
else
    echo "[4/4] Pprof disabled, skipping port-forward"
fi

echo ""
echo "=== Controller deployed successfully ==="
echo "Namespace: ${NAMESPACE}"
echo "Pod:"
kubectl get po -n "${NAMESPACE}" -l control-plane=rbgs-controller -o wide --no-headers
# What the Pods run: each one must be newer than "Started" above and run the image printed there.
kubectl get po -n "${NAMESPACE}" -l control-plane=rbgs-controller \
    -o custom-columns='POD:.metadata.name,CREATED:.metadata.creationTimestamp,IMAGE:.status.containerStatuses[*].image,IMAGE_ID:.status.containerStatuses[*].imageID'
