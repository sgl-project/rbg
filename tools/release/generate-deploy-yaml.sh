#!/bin/bash
set -ex

# Get repository root directory (where Makefile is located)
REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "Error: Failed to change to repo root"; exit 1; }

# Set paths relative to repo root
CHARTS_DIR="deploy/helm"

# use helm template to generate manifests
helm template \
  -n rbgs-system \
  --values "$HELM_CHART_PATH/values.yaml" \
  --dry-run \
  rbgs "$HELM_CHART_PATH" >> "$MANIFEST_FILE"
echo "Updated manifests at $MANIFEST_FILE"