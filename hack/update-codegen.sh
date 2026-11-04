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

# applyconfiguration-gen needs an OpenAPI schema of the RBG types to embed a
# structured-merge-diff schema in client-go/applyconfiguration/internal and to
# generate Extract<Kind> helpers. Without one, fake.NewClientset() rejects every
# RBG object. RBG types embed core/v1 types such as PodTemplateSpec, so those
# packages are generated as well to keep every reference resolvable.
OPENAPI_TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${OPENAPI_TMP_DIR}"' EXIT

# API rule violations are not enforced, so the report goes to the temp dir.
GOFLAGS=-mod=mod kube::codegen::gen_openapi ${REPO_ROOT}/api \
    --extra-pkgs k8s.io/api/core/v1 \
    --extra-pkgs k8s.io/apimachinery/pkg/api/resource \
    --extra-pkgs k8s.io/apimachinery/pkg/util/intstr \
    --output-dir "${REPO_ROOT}/pkg/generated/openapi" \
    --output-pkg sigs.k8s.io/rbgs/pkg/generated/openapi \
    --report-filename "${OPENAPI_TMP_DIR}/api_violations.report" \
    --update-report \
    --boilerplate "${REPO_ROOT}/hack/boilerplate.go.txt"

GOFLAGS=-mod=mod "${GO_CMD}" run ./hack/models-schema > "${OPENAPI_TMP_DIR}/swagger.json"

GOFLAGS=-mod=mod kube::codegen::gen_client ${REPO_ROOT}/api \
    --with-watch \
    --with-applyconfig \
    --applyconfig-externals "k8s.io/api/core/v1.PodTemplateSpec:k8s.io/client-go/applyconfigurations/core/v1" \
    --applyconfig-openapi-schema "${OPENAPI_TMP_DIR}/swagger.json" \
    --output-dir "$REPO_ROOT"/client-go \
    --output-pkg sigs.k8s.io/rbgs/client-go \
    --boilerplate "${REPO_ROOT}/hack/boilerplate.go.txt"