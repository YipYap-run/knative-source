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

# Pinned Kubernetes API-server version for envtest. Matches k8s.io/* module
# versions in go.mod; bump together.
ENVTEST_K8S_VERSION ?= 1.31.0

# Path to the setup-envtest helper. Installed on demand into $GOPATH/bin.
GOBIN ?= $(shell go env GOPATH)/bin
SETUP_ENVTEST ?= $(GOBIN)/setup-envtest

.PHONY: help
help:
	@echo "Targets:"
	@echo "  test           - run the default unit/reconciler suite"
	@echo "  test-envtest   - run the envtest integration suite (real apiserver + etcd)"
	@echo "  setup-envtest  - install the setup-envtest binary"

.PHONY: test
test:
	go test -count=1 ./...

.PHONY: setup-envtest
setup-envtest: $(SETUP_ENVTEST)

$(SETUP_ENVTEST):
	GOFLAGS=-mod=mod go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

# test-envtest runs the envtest-tagged suite against a real kube-apiserver +
# etcd pair. Binaries are fetched once and cached in
# ~/.local/share/kubebuilder-envtest on Linux.
.PHONY: test-envtest
test-envtest: $(SETUP_ENVTEST)
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
		go test -tags envtest -count=1 -v ./test/envtest/...
