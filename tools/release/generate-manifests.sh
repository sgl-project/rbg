#!/bin/bash
set -ex

# Get repository root directory (where Makefile is located)
REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "Error: Failed to change to repo root"; exit 1; }

# Set paths relative to manifest root
MANIFEST_DIR="deploy/kubectl"

# Set paths relative to repo root
CHARTS_DIR="deploy/helm"
HELM_CHART_PATH="$REPO_ROOT/$CHARTS_DIR/rbgs"
MANIFEST_FILE="$REPO_ROOT/deploy/kubectl/manifests.yaml"

# Check for kustomize
KUSTOMIZE="${REPO_ROOT}/bin/kustomize"
if [ ! -f "$KUSTOMIZE" ]; then
    # Fall back to system kustomize if available
    KUSTOMIZE="kustomize"
fi

# create target dir if it exits
mkdir -p "$MANIFEST_DIR"

# Clear or create the manifest file
echo "apiVersion: v1
kind: Namespace
metadata:
  labels:
    app.kubernetes.io/name: rbgs
    control-plane: rbgs-controller
  name: rbgs-system"> "$MANIFEST_FILE"

# Build CRDs using kustomize to apply conversion webhook patches
# This ensures v1alpha1->v1alpha2 conversion webhook is properly configured
echo "Building CRDs with kustomize to apply conversion webhook patches..."
$KUSTOMIZE build config/crd >> "$MANIFEST_FILE"

# use helm template to generate manifests
helm template \
  -n rbgs-system \
  --values "$HELM_CHART_PATH/values.yaml" \
  --set crdUpgrade.enabled=false \
  --dry-run \
  rbgs "$HELM_CHART_PATH" >> "$MANIFEST_FILE"
echo "Updated manifests at $MANIFEST_FILE"

echo "Update completed successfully!"
