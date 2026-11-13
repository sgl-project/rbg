#!/usr/bin/env bash
# Copyright 2026 The RBGS Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Pre-load the images referenced by the e2e suites into a Kind cluster, so the
# cluster's own egress is not needed while tests run.
#
# The registry each image is pulled from depends on the environment:
#
#   E2E_IMAGE_REGISTRY=docker.io (default)  pull from docker.io / registry.k8s.io
#                                           (GitHub Actions and other overseas runners)
#   E2E_IMAGE_REGISTRY=aliyun               prefer registry.cn-hangzhou.aliyuncs.com
#                                           mirrors and re-tag them to the names the
#                                           e2e pods reference
#
# The main workload image (test/utils.DefaultImage) follows E2E_DEFAULT_IMAGE:
# nginx:latest in docker.io mode, the Aliyun nginx mirror in aliyun mode, or the
# value E2E_DEFAULT_IMAGE is already set to. It is loaded under that exact name,
# and in GitHub Actions E2E_DEFAULT_IMAGE is exported to $GITHUB_ENV so the e2e
# steps that follow reference the same image. Elsewhere, export the printed
# E2E_DEFAULT_IMAGE before running the e2e suites.
#
# Images without an Aliyun mirror keep their upstream name in both modes, and
# images published only on Aliyun are pulled from there in both modes.
# Every pull is retried with a backoff, so a transient registry failure does
# not fail the job on the first attempt.

set -euo pipefail

CLUSTER_NAME="${KIND_CLUSTER_NAME:-rbgs-e2e}"
REGISTRY_MODE="${E2E_IMAGE_REGISTRY:-docker.io}"
PLATFORM="${KIND_IMAGE_PLATFORM:-linux/amd64}"
PULL_ATTEMPTS="${E2E_IMAGE_PULL_ATTEMPTS:-5}"
PULL_BACKOFF="${E2E_IMAGE_PULL_BACKOFF:-10}"
IMAGE_ARCHIVE="$(mktemp "${RUNNER_TEMP:-/tmp}/e2e-preload.XXXXXX")"
trap 'rm -f "${IMAGE_ARCHIVE}"' EXIT

if [ "${REGISTRY_MODE}" != "docker.io" ] && [ "${REGISTRY_MODE}" != "aliyun" ]; then
  echo "::error::E2E_IMAGE_REGISTRY must be 'docker.io' or 'aliyun' (got '${REGISTRY_MODE}')"
  exit 1
fi

# Must match the fallback of test/utils.DefaultImage.
UPSTREAM_DEFAULT_IMAGE="nginx:latest"
ALIYUN_DEFAULT_IMAGE="registry.cn-hangzhou.aliyuncs.com/acs-sample/nginx:latest"
if [ -n "${E2E_DEFAULT_IMAGE:-}" ]; then
  DEFAULT_IMAGE="${E2E_DEFAULT_IMAGE}"
elif [ "${REGISTRY_MODE}" = "aliyun" ]; then
  DEFAULT_IMAGE="${ALIYUN_DEFAULT_IMAGE}"
else
  DEFAULT_IMAGE="${UPSTREAM_DEFAULT_IMAGE}"
fi

# Other images hardcoded by e2e specs. Format: <name referenced by e2e pods>|<aliyun source or ->
# The docker.io mode always pulls the referenced name itself.
IMAGES=(
  "nginx:1.25|registry.cn-hangzhou.aliyuncs.com/acs-sample/nginx:latest"
  "busybox:1.36|-"
  "python:3.11-slim|-"
  "registry.k8s.io/pause:3.10|registry.cn-hangzhou.aliyuncs.com/google_containers/pause:3.10"
)

# Images published only on Aliyun (no docker.io counterpart), so they are pulled
# from there in both modes, e.g. the patio-runtime sidecar injected through the
# ClusterEngineRuntimeProfile that the engine-runtime specs create.
ALIYUN_ONLY_IMAGES=(
  "registry-cn-hangzhou.ack.aliyuncs.com/dev/patio-runtime:v0.2.0"
)

pull_with_retry() {
  local image="$1" attempt
  for attempt in $(seq 1 "${PULL_ATTEMPTS}"); do
    if docker pull "${image}"; then
      return 0
    fi
    echo "pull attempt ${attempt}/${PULL_ATTEMPTS} failed for ${image}; retrying in ${PULL_BACKOFF}s"
    sleep "${PULL_BACKOFF}"
  done
  echo "::error::failed to pull ${image} after ${PULL_ATTEMPTS} attempts"
  return 1
}

load_into_kind() {
  local image="$1"
  docker image save --platform "${PLATFORM}" --output "${IMAGE_ARCHIVE}" "${image}"
  kind load image-archive "${IMAGE_ARCHIVE}" --name "${CLUSTER_NAME}"
}

# Most e2e pods run DefaultImage, so it is loaded under the exact name they
# reference; a missing copy makes the kubelet pull it while the specs run.
pull_with_retry "${DEFAULT_IMAGE}"
load_into_kind "${DEFAULT_IMAGE}"

for entry in "${IMAGES[@]}"; do
  target="${entry%%|*}"
  aliyun_source="${entry##*|}"

  if [ "${REGISTRY_MODE}" = "aliyun" ] && [ "${aliyun_source}" != "-" ]; then
    pull_with_retry "${aliyun_source}"
    # Re-tag so the name matches what the e2e pods reference; otherwise the
    # kubelet resolves the short name to docker.io and pulls it again.
    docker tag "${aliyun_source}" "${target}"
  else
    if [ "${REGISTRY_MODE}" = "aliyun" ]; then
      echo "no Aliyun mirror for ${target}; pulling it from its upstream registry"
    fi
    pull_with_retry "${target}"
  fi
  load_into_kind "${target}"
done

for image in "${ALIYUN_ONLY_IMAGES[@]}"; do
  pull_with_retry "${image}"
  load_into_kind "${image}"
done

if [ -n "${GITHUB_ENV:-}" ]; then
  echo "E2E_DEFAULT_IMAGE=${DEFAULT_IMAGE}" >> "${GITHUB_ENV}"
elif [ -z "${E2E_DEFAULT_IMAGE:-}" ] && [ "${DEFAULT_IMAGE}" != "${UPSTREAM_DEFAULT_IMAGE}" ]; then
  echo "Run the e2e suites with: export E2E_DEFAULT_IMAGE=${DEFAULT_IMAGE}"
fi

echo "All e2e test images loaded into Kind cluster '${CLUSTER_NAME}' (registry mode: ${REGISTRY_MODE}, default image: ${DEFAULT_IMAGE})"
