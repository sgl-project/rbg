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
# Images without an Aliyun mirror keep their upstream name in both modes.
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

# e2e workload images. Format: <name referenced by e2e pods>|<aliyun source or ->
# The docker.io mode always pulls the referenced name itself.
IMAGES=(
  "nginx:latest|registry.cn-hangzhou.aliyuncs.com/acs-sample/nginx:latest"
  "nginx:1.25|registry.cn-hangzhou.aliyuncs.com/acs-sample/nginx:latest"
  "busybox:1.36|-"
  "python:3.11-slim|-"
  "registry.k8s.io/pause:3.10|registry.cn-hangzhou.aliyuncs.com/google_containers/pause:3.10"
)

# Images only used by China-side environments; no docker.io counterpart exists,
# so they are skipped entirely in docker.io mode.
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

if [ "${REGISTRY_MODE}" = "aliyun" ]; then
  for image in "${ALIYUN_ONLY_IMAGES[@]}"; do
    pull_with_retry "${image}"
    load_into_kind "${image}"
  done
fi

echo "All e2e test images loaded into Kind cluster '${CLUSTER_NAME}' (registry mode: ${REGISTRY_MODE})"
