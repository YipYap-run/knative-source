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

# Installs the chart from this checkout into a local kind cluster and checks
# that example.yaml is admitted and its adapter becomes Available and Ready.
# CI runs each step separately; locally, `test/smoke/smoke.sh all` runs the
# whole sequence. Requires kind, kubectl, helm, ko, docker, openssl and yq.
#
# Steps:
#   cluster  create the kind cluster and install Calico, so NetworkPolicy
#            is enforced
#   images   build controller, webhook, adapter and stub images with ko and
#            load them into kind. The adapter image also trusts the smoke CA,
#            so it can reach the stub over https.
#   stub     run the stand-in YipYap API and sink (test/smoke/stub)
#   chart    install chart/yipyap-source with the local images
#   source   check admission rejects bad base URLs, then apply example.yaml
#            pointed at the stub
#   verify   wait for the source to be Ready and the adapter Available, and
#            check the stub saw the event delivered and confirmed
#   dump     print cluster state and logs (CI runs it on failure)
#   delete   delete the kind cluster
#
# Environment:
#   SMOKE_CLUSTER  kind cluster name (default yipyap-smoke)
#   SMOKE_WORKDIR  scratch directory for keys and values files
#                  (default $RUNNER_TEMP/yipyap-smoke or /tmp/yipyap-smoke)

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SMOKE_DIR="${ROOT_DIR}/test/smoke"
CLUSTER="${SMOKE_CLUSTER:-yipyap-smoke}"
CONTEXT="kind-${CLUSTER}"
WORK="${SMOKE_WORKDIR:-${RUNNER_TEMP:-/tmp}/yipyap-smoke}"

# Calico release and the SHA-256 of its manifests/calico.yaml. Bump both
# together after reviewing the new manifest.
CALICO_VERSION="v3.32.3"
CALICO_SHA256="3db4c55514b678be853be49b8cb102bed0a00bc0343eec05ac191652dc7211e0"

IMAGE_PREFIX="smoke.local/knative-source"
IMAGE_TAG="smoke"
RELEASE_NS="yipyap-sources"
STUB_NS="yipyap-smoke"
STUB_HOST="yipyap-stub.${STUB_NS}.svc.cluster.local"
SOURCE_NAME="yipyap-source-example"

log() { printf '==> %s\n' "$*" >&2; }
die() {
  printf 'smoke: %s\n' "$*" >&2
  exit 1
}

need() {
  local tool
  for tool in "$@"; do
    command -v "${tool}" > /dev/null || die "${tool} is required"
  done
}

sha256_of() {
  if command -v sha256sum > /dev/null; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# require_context refuses to touch any cluster but the smoke kind cluster.
require_context() {
  local current
  current="$(kubectl config current-context 2> /dev/null || true)"
  [[ "${current}" == "${CONTEXT}" ]] ||
    die "kubectl context is '${current}', expected '${CONTEXT}'; run the cluster step first"
}

step_cluster() {
  need kind kubectl curl
  mkdir -p "${WORK}"
  if kind get clusters | grep -qx "${CLUSTER}"; then
    log "kind cluster ${CLUSTER} exists"
    kubectl config use-context "${CONTEXT}" > /dev/null
  else
    log "creating kind cluster ${CLUSTER}"
    kind create cluster --name "${CLUSTER}" --config "${SMOKE_DIR}/kind-config.yaml" --wait 0s
  fi
  require_context

  log "installing Calico ${CALICO_VERSION}"
  local manifest="${WORK}/calico.yaml"
  curl -fsSL --retry 3 -o "${manifest}" \
    "https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests/calico.yaml"
  local got
  got="$(sha256_of "${manifest}")"
  [[ "${got}" == "${CALICO_SHA256}" ]] ||
    die "calico.yaml checksum mismatch: got ${got}, want ${CALICO_SHA256}"
  # Server-side apply: the Calico CRDs are too large for the annotation
  # client-side apply writes.
  kubectl apply --server-side -f "${manifest}" > /dev/null
  kubectl -n kube-system rollout status daemonset/calico-node --timeout=300s
  kubectl -n kube-system rollout status deployment/calico-kube-controllers --timeout=300s
  kubectl wait --for=condition=Ready node --all --timeout=300s
  kubectl -n kube-system rollout status deployment/coredns --timeout=300s
}

step_certs() {
  need openssl
  mkdir -p "${WORK}"
  if [[ -s "${WORK}/ca.crt" && -s "${WORK}/tls.crt" && -s "${WORK}/api-key" ]]; then
    return
  fi
  log "creating the smoke CA, the stub certificate and an API key"
  (
    cd "${WORK}"
    umask 077
    openssl req -x509 -newkey rsa:2048 -nodes -days 2 -sha256 \
      -subj "/CN=yipyap smoke CA" \
      -addext "basicConstraints=critical,CA:TRUE" \
      -addext "keyUsage=critical,keyCertSign,cRLSign" \
      -keyout ca.key -out ca.crt 2> /dev/null
    openssl req -newkey rsa:2048 -nodes -sha256 \
      -subj "/CN=${STUB_HOST}" -keyout tls.key -out tls.csr 2> /dev/null
    printf 'subjectAltName=DNS:%s\nbasicConstraints=CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n' \
      "${STUB_HOST}" > tls.ext
    openssl x509 -req -sha256 -days 2 -in tls.csr -CA ca.crt -CAkey ca.key \
      -CAcreateserial -extfile tls.ext -out tls.crt 2> /dev/null
    # A throwaway key the stub accepts. It is never printed.
    openssl rand -hex 24 | tr -d '\n' > api-key
    # The stub pod runs as a non-root user and reads the key from a Secret
    # volume, which keeps its own file mode.
    chmod 0644 ca.crt tls.crt
  )
}

step_images() {
  need ko docker kind
  step_certs
  cd "${ROOT_DIR}"
  local platform bin ref
  platform="linux/$(go env GOARCH)"
  local images=()
  for bin in controller webhook receive-adapter; do
    log "building ${bin}"
    ref="$(KO_DOCKER_REPO=ko.local CGO_ENABLED=0 ko build --platform="${platform}" --sbom=none "./cmd/${bin}")"
    local name="${bin}"
    [[ "${bin}" == "receive-adapter" ]] && name="adapter"
    if [[ "${name}" == "adapter" ]]; then
      add_smoke_ca "${ref}" "${IMAGE_PREFIX}-adapter:${IMAGE_TAG}"
    else
      docker tag "${ref}" "${IMAGE_PREFIX}-${name}:${IMAGE_TAG}"
    fi
    images+=("${IMAGE_PREFIX}-${name}:${IMAGE_TAG}")
  done
  log "building stub"
  ref="$(KO_DOCKER_REPO=ko.local CGO_ENABLED=0 ko build --platform="${platform}" --sbom=none ./test/smoke/stub)"
  docker tag "${ref}" "${IMAGE_PREFIX}-stub:${IMAGE_TAG}"
  images+=("${IMAGE_PREFIX}-stub:${IMAGE_TAG}")

  log "loading images into kind"
  kind load docker-image --name "${CLUSTER}" "${images[@]}"
}

# add_smoke_ca copies image $1 to $2 with the smoke CA appended to the
# system trust bundle, which is where the Go TLS stack in the adapter looks.
# The shipped adapter image is unchanged; only this test copy trusts the
# stub's certificate.
add_smoke_ca() {
  local src="$1" dst="$2" bundle="${WORK}/adapter-ca-bundle.crt" cid
  cid="$(docker create "${src}")"
  docker cp -L "${cid}:/etc/ssl/certs/ca-certificates.crt" "${bundle}"
  cat "${WORK}/ca.crt" >> "${bundle}"
  chmod 0644 "${bundle}"
  docker cp "${bundle}" "${cid}:/etc/ssl/certs/ca-certificates.crt"
  docker commit "${cid}" "${dst}" > /dev/null
  docker rm "${cid}" > /dev/null
}

step_stub() {
  need kubectl
  require_context
  step_certs
  log "starting the stub API in ${STUB_NS}"
  kubectl create namespace "${STUB_NS}" --dry-run=client -o yaml | kubectl apply -f - > /dev/null
  kubectl -n "${STUB_NS}" create secret tls stub-tls \
    --cert="${WORK}/tls.crt" --key="${WORK}/tls.key" --dry-run=client -o yaml |
    kubectl apply -f - > /dev/null
  kubectl -n "${STUB_NS}" create secret generic stub-api-key \
    --from-file=api-key="${WORK}/api-key" --dry-run=client -o yaml |
    kubectl apply -f - > /dev/null
  kubectl apply -f "${SMOKE_DIR}/stub.yaml"
  kubectl -n "${STUB_NS}" rollout status deployment/yipyap-stub --timeout=180s
}

step_chart() {
  need kubectl helm docker
  require_context
  mkdir -p "${WORK}"
  log "reading the kube-apiserver addresses"
  "${ROOT_DIR}/hack/apiserver-cidrs-values.sh" > "${WORK}/apiserver-values.yaml"
  cat "${WORK}/apiserver-values.yaml"

  # The API server runs in the host network of the kind node. With Calico
  # it can reach the webhook pod from any address on the node (the node IP
  # or the IPIP tunnel address), so allow all of them as webhook clients.
  local node_cidrs
  node_cidrs="$(docker exec "${CLUSTER}-control-plane" ip -o -4 addr show scope global |
    awk '{split($4, a, "/"); printf "    - \"%s/32\"\n", a[1]}')"
  [[ -n "${node_cidrs}" ]] || die "no addresses found on ${CLUSTER}-control-plane"

  cat > "${WORK}/smoke-values.yaml" << EOF
images:
  controller:
    repository: ${IMAGE_PREFIX}-controller
    tag: ${IMAGE_TAG}
    pullPolicy: Never
  webhook:
    repository: ${IMAGE_PREFIX}-webhook
    tag: ${IMAGE_TAG}
    pullPolicy: Never
  adapter:
    repository: ${IMAGE_PREFIX}-adapter
    tag: ${IMAGE_TAG}
webhook:
  allowedBaseURLHosts:
    - console.yipyap.run
    - ${STUB_HOST}
networkPolicy:
  webhookIngressCIDRs:
${node_cidrs}
EOF
  cat "${WORK}/smoke-values.yaml"

  # The same command the chart README documents, plus the values above.
  log "installing chart/yipyap-source"
  helm upgrade --install yipyap-source "${ROOT_DIR}/chart/yipyap-source" \
    --namespace "${RELEASE_NS}" --create-namespace \
    -f "${WORK}/apiserver-values.yaml" -f "${WORK}/smoke-values.yaml" \
    --wait --timeout 5m
  kubectl -n "${RELEASE_NS}" wait --for=condition=Available deployment --all --timeout=300s
}

wait_for_webhook() {
  for _ in $(seq 1 90); do
    if kubectl -n "${RELEASE_NS}" get endpoints yipyap-source-webhook \
      -o jsonpath='{.subsets[*].addresses[*].ip}' 2>/dev/null | grep -q .; then
      return 0
    fi
    sleep 2
  done
  die "webhook service has no ready endpoints"
}

# source_manifest prints example.yaml with spec.baseURL set to $1 and the
# sink pointed at the stub. A Broker sink needs a broker implementation in
# the cluster, which this job does not install; the conformance workflow
# covers Knative Eventing.
source_manifest() {
  BASE_URL="$1" SINK_URI="http://${STUB_HOST}" yq '
    .spec.baseURL = strenv(BASE_URL) |
    .spec.sink = {"uri": strenv(SINK_URI)}
  ' "${ROOT_DIR}/example.yaml"
}

# expect_rejected applies the manifest on stdin as a server-side dry run and
# fails unless the API server rejected it with output matching pattern $2.
expect_rejected() {
  local what="$1" pattern="$2" out
  if out="$(kubectl apply --dry-run=server -f - 2>&1)"; then
    die "the API server accepted ${what}; it must be rejected"
  fi
  if ! grep -Eq "${pattern}" <<< "${out}"; then
    die "applying ${what} failed, but not as expected (${pattern}): ${out}"
  fi
  log "rejected ${what}: ${out}"
}

step_source() {
  need kubectl yq
  require_context
  step_certs
  kubectl -n "${RELEASE_NS}" create secret generic yipyap-creds \
    --from-file=api-key="${WORK}/api-key" --dry-run=client -o yaml |
    kubectl apply -f - > /dev/null

  wait_for_webhook

  log "checking that base URLs outside the policy are rejected"
  # The webhook enforces the host allowlist.
  source_manifest "https://evil.example.com" |
    expect_rejected "a host outside the allowlist" "denied the request"
  # The CRD schema requires https before the webhook is called.
  source_manifest "http://${STUB_HOST}" |
    expect_rejected "a plain http base URL" "spec.baseURL"

  log "applying example.yaml"
  source_manifest "https://${STUB_HOST}" | kubectl apply -f -
}

step_verify() {
  need kubectl
  require_context
  log "waiting for ${SOURCE_NAME} to become Ready"
  kubectl -n "${RELEASE_NS}" wait "yipyapsource/${SOURCE_NAME}" \
    --for=condition=Ready --timeout=300s

  local deploy uid
  deploy="$(kubectl -n "${RELEASE_NS}" get "yipyapsource/${SOURCE_NAME}" -o jsonpath='{.status.deploymentName}')"
  [[ -n "${deploy}" ]] || die "status.deploymentName is empty"
  log "waiting for adapter deployment ${deploy} to become Available"
  kubectl -n "${RELEASE_NS}" wait "deployment/${deploy}" --for=condition=Available --timeout=300s

  # Readiness starts out true, so also check the adapter really talked to
  # the stub: the event reached the sink, and a later poll confirmed it.
  uid="$(kubectl -n "${RELEASE_NS}" get "yipyapsource/${SOURCE_NAME}" -o jsonpath='{.metadata.uid}')"
  local deadline=$((SECONDS + 120)) logs=""
  while ((SECONDS < deadline)); do
    logs="$(kubectl -n "${STUB_NS}" logs deployment/yipyap-stub)"
    if grep -q '"msg":"stub sink received".*"ce_id":"smoke-1"' <<< "${logs}" &&
      grep '"msg":"stub poll"' <<< "${logs}" | grep -q "\"since\":\"c1\".*\"source_id\":\"${uid}\""; then
      break
    fi
    sleep 3
  done
  grep -q '"msg":"stub sink received".*"ce_id":"smoke-1"' <<< "${logs}" ||
    die "the sink never received event smoke-1"
  grep '"msg":"stub poll"' <<< "${logs}" | grep -q "\"since\":\"c1\".*\"source_id\":\"${uid}\"" ||
    die "the adapter never confirmed event smoke-1 with since=c1 and its source ID"
  if grep -q '"msg":"stub rejected credentials"' <<< "${logs}"; then
    die "the stub rejected the adapter's API key"
  fi
  grep -q '"msg":"stub ack"' <<< "${logs}" ||
    log "no stub ack yet (the adapter sends one on shutdown or after a stream batch)"
  log "smoke test passed"
}

step_dump() {
  set +e
  kubectl config current-context
  kubectl get nodes -o wide
  kubectl get pods -A -o wide
  kubectl -n "${RELEASE_NS}" get yipyapsources -o yaml
  kubectl -n "${RELEASE_NS}" get deployments,networkpolicies -o wide
  kubectl get events -A --sort-by=.lastTimestamp | tail -n 100
  kubectl -n "${RELEASE_NS}" logs -l app.kubernetes.io/component=controller --tail=300 --all-containers
  kubectl -n "${RELEASE_NS}" logs -l app.kubernetes.io/component=webhook --tail=300 --all-containers
  kubectl -n "${RELEASE_NS}" logs -l sources.yipyap.run/source --tail=300 --all-containers
  kubectl -n "${STUB_NS}" logs deployment/yipyap-stub --tail=300
  return 0
}

step_delete() {
  need kind
  kind delete cluster --name "${CLUSTER}"
}

main() {
  [[ $# -ge 1 ]] || die "usage: $0 cluster|images|stub|chart|source|verify|dump|delete|all"
  case "$1" in
    cluster) step_cluster ;;
    certs) step_certs ;;
    images) step_images ;;
    stub) step_stub ;;
    chart) step_chart ;;
    source) step_source ;;
    verify) step_verify ;;
    dump) step_dump ;;
    delete) step_delete ;;
    all)
      step_cluster
      step_images
      step_stub
      step_chart
      step_source
      step_verify
      ;;
    *) die "unknown step: $1" ;;
  esac
}

main "$@"
