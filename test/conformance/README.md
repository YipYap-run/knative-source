# Conformance tests

These tests verify that `YipYapSource` complies with the Knative source
duck-type contract. They run against a real Kubernetes cluster with the
controller and webhook installed via the `yipyap-source` Helm chart; nothing
is mocked.

## Scope

The suite is a locally-authored subset of the contract exercised by
[`knative.dev/eventing/test/conformance/helpers/sources`][upstream]. We do
not import the upstream helpers directly because doing so drags
`knative.dev/eventing/test/lib` (the full e2e harness) into our vendor tree.
Each test in `conformance_test.go` cites the upstream helper it mirrors
inline.

[upstream]: https://github.com/knative/eventing/tree/main/test/conformance/helpers/sources

### What the suite checks

- The `YipYapSource` CR applies and registers with the API server.
- Reconcile reports `SinkProvided=True` within 5 minutes, and the adapter
  reports in (`Connected` is `True` or `False`). The suite does not need a
  YipYap backend (CI uses the smoke stub), so `Ready=False` with reason
  `APIKeyRejected` or `APIUnreachable` is a pass, as long as Ready carries a
  reason and message.
- `Status.SinkURI` is populated after `SinkProvided=True`.
- `Status.CloudEventAttributes` advertises at least one entry, each with a
  non-empty `type`.
- The adapter `Deployment` named by `Status.DeploymentName` exists, and is
  `Available` when the source is Ready.
- Deleting the `YipYapSource` cascades to the adapter `Deployment` via owner
  references.

### What is NOT covered yet

- End-to-end event flow (requires a running `yipyap` backend, covered
  separately).
- OIDC authentication between adapter and sink (covered separately).
- Upstream `SourceCRDMetadataTestHelper` and `SourceCRDRBACTestHelper`:
  these assert the CRD manifest ships specific printer columns and that the
  controller ServiceAccount holds the canonical `sources.knative.dev/source`
  aggregated ClusterRole. Adding them requires importing `testlib`; tracked
  separately.
- High-scale / long-running behavior.

## Running locally

Prerequisites:

- A Kubernetes cluster (kind is fine).
- [Knative Eventing][ke-install] installed in the cluster.
- The `yipyap-source` chart installed, with controller, webhook and adapter
  images the cluster can pull. The smoke script does both for a kind
  cluster: it creates the cluster with Calico, builds the three images from
  this checkout with ko, loads them into kind and installs the chart with
  `networkPolicy.apiServerCIDRs` read from the cluster. It needs kind,
  kubectl, helm, ko, docker and openssl.

  ```bash
  export SMOKE_CLUSTER=yipyap-conformance
  test/smoke/smoke.sh cluster
  test/smoke/smoke.sh images
  test/smoke/smoke.sh stub
  test/smoke/smoke.sh chart
  ```

  `smoke.sh stub` runs a stand-in YipYap API in the cluster. Point the
  suite at it so no request reaches the real console:

  ```bash
  export YIPYAP_CONFORMANCE_BASE_URL=https://yipyap-stub.yipyap-smoke.svc.cluster.local
  export YIPYAP_CONFORMANCE_API_KEY_FILE=/tmp/yipyap-smoke/api-key
  ```

  Without these variables the sources use the default baseURL and a
  placeholder key.

  For another cluster, build and push the images yourself and install the
  chart with `images.*.repository` and `images.*.tag` set to them:

  ```bash
  hack/apiserver-cidrs-values.sh > /tmp/apiserver-values.yaml
  helm install yipyap-source ./chart/yipyap-source \
    -n yipyap-sources --create-namespace \
    -f /tmp/apiserver-values.yaml
  kubectl wait --for=condition=Available deployment \
    -n yipyap-sources --all --timeout=300s
  ```

  Without image overrides the chart installs the published images tagged
  with the chart's `appVersion`.

Then:

```bash
KUBECONFIG=$HOME/.kube/config \
  go test -tags conformance ./test/conformance/... -v -timeout 10m
```

The build tag `conformance` gates the package; a default `go test ./...`
does not compile or execute these tests. If `KUBECONFIG` is not set and no
default kubeconfig is discoverable, the suite exits cleanly with a skip
notice; this keeps `go test -tags conformance ./...` a useful build check
on developer machines.

[ke-install]: https://knative.dev/docs/install/

## CI

A `Conformance` workflow at `.github/workflows/conformance.yaml` runs the
suite against a freshly-provisioned kind cluster. It's gated two ways:

1. Add the `conformance` label to a pull request; the workflow fires on the
   `labeled` event.
2. Trigger manually via the Actions tab (`workflow_dispatch`).

It sets up the cluster, images and chart with `test/smoke/smoke.sh`, so it
tests the images built from the commit under review. Every pull request
also runs the kind smoke job in `.github/workflows/ci.yaml`, which installs
the chart the same way and checks that `example.yaml` becomes Ready against
a stub YipYap API.

## Troubleshooting

- **Tests fail on "source never settled"**: the subtest dumps every
  condition on the source before failing. Check `SinkProvided`: it should be
  `True` if the sink ref resolves, `False` with a reason if the controller
  rejected the ref, or `Unknown` if reconcile hasn't run at all. If
  `Connected` stays `Unknown` with reason `AdapterStarting`, the adapter pod
  never started; with `AdapterStatusUnknown`, the controller could not list
  pods (check its RBAC).
- **"adapter Deployment not Available"**: usually means the adapter image
  can't be pulled, or the adapter is crash-looping on a missing API key.
  `kubectl describe deploy -n <conformance-ns> <deploymentName>`.
- **Namespace leaks after a test crash**: every test registers a cleanup
  that deletes its namespace. If Go panics before cleanup runs, delete
  leftover namespaces with `kubectl delete ns -l yipyap.run/conformance=true`.
