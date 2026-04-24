#!/usr/bin/env bash

# Copyright 2026 The YipYap Authors
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

set -o errexit
set -o nounset
set -o pipefail

source "$(dirname "$0")/../vendor/knative.dev/hack/codegen-library.sh"

# If we run with -mod=vendor here, codegen tools write into the vendor tree.
export GOFLAGS=-mod=

echo "=== Update Codegen for ${MODULE_NAME}"

# shellcheck disable=SC1091
source "${CODEGEN_PKG}/kube_codegen.sh"

BOILERPLATE="${REPO_ROOT_DIR}/hack/boilerplate/boilerplate.go.txt"

group "Kubernetes Codegen (deepcopy)"

# The vendored k8s.io/code-generator in this repo (pinned to the same version
# as the rest of the k8s.io/* dependencies) predates validation-gen. The
# kube_codegen.sh gen_helpers function tries to install validation-gen
# unconditionally, so we invoke deepcopy-gen directly instead.
GO111MODULE=on go install k8s.io/code-generator/cmd/deepcopy-gen

# Enumerate API package directories (those containing v<x>alpha|beta-style leaves).
API_PACKAGES=()
while IFS= read -r d; do
  API_PACKAGES+=("$(cd "${d}" && GO111MODULE=on go list -find .)")
done < <(find "${REPO_ROOT_DIR}/pkg/apis" -mindepth 2 -maxdepth 2 -type d -regex '.*/v[0-9]+\(alpha[0-9]+\|beta[0-9]+\)?')

GOBIN="${GOBIN:-$(go env GOPATH)/bin}"
"${GOBIN}/deepcopy-gen" \
  --output-file zz_generated.deepcopy.go \
  --go-header-file "${BOILERPLATE}" \
  "${API_PACKAGES[@]}"

group "Kubernetes Codegen (clientset + lister + informer)"

kube::codegen::gen_client \
  --with-watch \
  --output-dir "${REPO_ROOT_DIR}/pkg/client" \
  --output-pkg "${MODULE_NAME}/pkg/client" \
  --boilerplate "${BOILERPLATE}" \
  "${REPO_ROOT_DIR}/pkg/apis"

group "Knative Codegen (injection)"

# Knative injection layer
"${KNATIVE_CODEGEN_PKG}/hack/generate-knative.sh" "injection" \
  "${MODULE_NAME}/pkg/client" "${MODULE_NAME}/pkg/apis" \
  "sources:v1alpha1" \
  --go-header-file "${BOILERPLATE}"

group "Update deps post-codegen"

# Make sure our dependencies are up-to-date
"${REPO_ROOT_DIR}/hack/update-deps.sh"
