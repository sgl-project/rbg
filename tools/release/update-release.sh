#!/bin/bash
set -ex

# Get repository root directory (where Makefile is located)
REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT" || { echo "Error: Failed to change to repo root"; exit 1; }

# Set paths relative to repo root
CHARTS_DIR="deploy/helm"

# Set paths relative to manifest root
MANIFEST_DIR="deploy/kubectl"

# Function to extract variable values from Makefile
extract_make_var() {
    local var_name=$1
    grep -E "^\s*${var_name}\s*[\?:]?=" Makefile | sed -E 's/.*=\s*(.*)/\1/' | tail -1
}

# Get current branch name
branch=$(git symbolic-ref --short HEAD 2>/dev/null || echo "")
if [ -z "$branch" ]; then
    echo "Error: Not on a branch. Please checkout a branch."
    exit 1
fi

# Extract version from branch name (supports v prefix and semantic versioning)
version=$(echo "$branch" | sed -E 's/^v?([0-9]+\.[0-9]+\.[0-9]+.*)/\1/')

# Validate the version format
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]; then
    echo "Error: Branch '$branch' is not a valid version. Required format: X.Y.Z[-prerelease]"
    echo "Examples: 0.5.0, 1.2.3-alpha.1, v2.0.0-rc.2"
    exit 1
fi

# Use the validated branch version for the chart
CLEAN_VERSION="$version"
echo "Using branch version: $CLEAN_VERSION"

# Extract variables from Makefile for image tag
VERSION=$(extract_make_var VERSION | tr -d '[:space:]')
GIT_SHA=$(git rev-parse --short HEAD || echo "HEAD")
TAG="${VERSION}-${GIT_SHA}"

# Validate Makefile variables
if [[ -z "$VERSION" || -z "$GIT_SHA" ]]; then
    echo "Error: Failed to extract required variables from Makefile"
    exit 1
fi

# Create appVersion by removing leading 'v' from TAG
APP_VERSION=$(echo "$TAG" | sed 's/^v//')
echo "Detected Makefile version: $VERSION"
echo "Detected Git SHA: $GIT_SHA"
echo "Generated image TAG: $TAG"
echo "Generated appVersion: $APP_VERSION"

# Update Chart.yaml
CHART_FILE="${CHARTS_DIR}/rbgs/Chart.yaml"
if [[ -f "$CHART_FILE" ]]; then
    # Update version field
    sed -i.bak -E "s/^(version:[[:space:]]+).*/\1${CLEAN_VERSION}/" "$CHART_FILE"
    
    # Update appVersion field without quotes
    sed -i.bak -E "s/^(appVersion:[[:space:]]+).*/\1${APP_VERSION}/" "$CHART_FILE"
    
    rm -f "${CHART_FILE}.bak"
    echo "Updated $CHART_FILE:"
    echo "  version: $CLEAN_VERSION"
    echo "  appVersion: $APP_VERSION"
else
    echo "Error: $CHART_FILE not found at ${CHART_FILE}!"
    exit 1
fi

# Update values.yaml
VALUES_FILE="${CHARTS_DIR}/rbgs/values.yaml"
if [[ -f "$VALUES_FILE" ]]; then
    # Update tag field without quotes
    sed -i.bak -E "s/^(  tag:[[:space:]]+).*/\1$TAG/" "$VALUES_FILE"
    rm -f "${VALUES_FILE}.bak"
    echo "Updated $VALUES_FILE tag to $TAG"
else
    echo "Error: $VALUES_FILE not found at ${VALUES_FILE}!"
    exit 1
fi

# Update manifest.yaml used by kubectl
HELM_CHART_PATH="${CHARTS_DIR}/rbgs"
MANIFEST_FILE="${MANIFEST_DIR}/manifests.yaml"

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

# process crds
if [ -d "$HELM_CHART_PATH/crds" ]; then
    echo "Processing CRDs..."
    for crd_file in "$HELM_CHART_PATH/crds"/*.yaml; do
        if [ -f "$crd_file" ]; then
            echo "---" >> "$MANIFEST_FILE"
            cat "$crd_file" >> "$MANIFEST_FILE"
        fi
    done
fi

# use helm template to generate manifests
helm template \
  -n rbgs-system \
  --values "$HELM_CHART_PATH/values.yaml" \
  --show-only templates/clusterrole.yaml \
  --show-only templates/manager.yaml \
  --show-only templates/rolebinding.yaml \
  --show-only templates/serviceaccount.yaml \
  --dry-run \
  rbgs "$HELM_CHART_PATH" >> "$MANIFEST_FILE"
echo "Updated manifests at $MANIFEST_FILE"

echo "Update completed successfully!"
