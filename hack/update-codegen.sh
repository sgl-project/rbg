#!/usr/bin/env bash

# Copyright 2025 The RBG Authors.
# 
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
# 
# 	http://www.apache.org/licenses/LICENSE-2.0
# 
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# set -o errexit
# set -o nounset
# set -o pipefail

cd "$(dirname "${0}")/.."
GO_CMD=${1:-go}
CODEGEN_PKG=${2:-bin}
REPO_ROOT="$(git rev-parse --show-toplevel)"


source "${CODEGEN_PKG}/kube_codegen.sh"

GOFLAGS=-mod=mod kube::codegen::gen_helpers ${REPO_ROOT}/api \
    --boilerplate "${REPO_ROOT}/hack/boilerplate.go.txt"

# applyconfiguration-gen embeds a structured-merge-diff schema in
# client-go/applyconfiguration/internal, which Extract<Kind> and
# fake.NewClientset() use to track field ownership. It must match the schema
# kube-apiserver uses for server-side apply on the RBG custom resources, so
# hack/models-schema derives it from the CRDs in config/crd/bases (written by
# `make manifests`) instead of from the Go types.
OPENAPI_TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${OPENAPI_TMP_DIR}"' EXIT

GOFLAGS=-mod=mod "${GO_CMD}" run ./hack/models-schema \
    --crd-dir "${REPO_ROOT}/config/crd/bases" > "${OPENAPI_TMP_DIR}/swagger.json"

GOFLAGS=-mod=mod kube::codegen::gen_client ${REPO_ROOT}/api \
    --with-watch \
    --with-applyconfig \
    --applyconfig-externals "k8s.io/api/core/v1.PodTemplateSpec:k8s.io/client-go/applyconfigurations/core/v1" \
    --applyconfig-openapi-schema "${OPENAPI_TMP_DIR}/swagger.json" \
    --output-dir "$REPO_ROOT"/client-go \
    --output-pkg sigs.k8s.io/rbgs/client-go \
    --boilerplate "${REPO_ROOT}/hack/boilerplate.go.txt"