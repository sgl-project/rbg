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

# create target dir if not exits
mkdir -p "$MANIFEST_DIR"

# Clear or create the manifest file
echo "apiVersion: v1
kind: Namespace
metadata:
  labels:
    app.kubernetes.io/name: rbgs
    control-plane: rbgs-controller
  name: rbgs-system"> "$MANIFEST_FILE"

# process crds from config/crd/bases (authoritative source)
CRD_BASES_DIR="config/crd/bases"
if [ -d "$CRD_BASES_DIR" ]; then
    echo "Processing CRDs from $CRD_BASES_DIR..."
    for crd_file in "$CRD_BASES_DIR"/*.yaml; do
        if [ -f "$crd_file" ]; then
            echo "---" >> "$MANIFEST_FILE"
            cat "$crd_file" >> "$MANIFEST_FILE"
        fi
    done
else
    echo "Warning: CRD directory $CRD_BASES_DIR not found, skipping CRDs"
fi

# use helm template to generate manifests
helm template \
  -n rbgs-system \
  --values "$HELM_CHART_PATH/values.yaml" \
  --set crdUpgrade.enabled=false \
  --dry-run \
  rbgs "$HELM_CHART_PATH" >> "$MANIFEST_FILE"
echo "Updated manifests at $MANIFEST_FILE"

echo "Update completed successfully!"
