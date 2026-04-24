# Conformance tests

These tests verify that `YipYapSource` complies with the Knative source
duck-type contract. They run against a real Kubernetes cluster with the
controller and webhook installed via the `yipyap-source` Helm chart; nothing
is mocked.

## Scope

The suite is a locally-authored subset of the contract exercised by
[`knative.dev/eventing/test/conformance/helpers/sources`][upstream]. We do
not import the upstream helpers directly because doing so drags
`knative.dev/eventing/test/lib` (the full e2e harness) into our vendor tree,
which is out of scope for Phase 4. Each test in `conformance_test.go` cites
the upstream helper it mirrors inline.

[upstream]: https://github.com/knative/eventing/tree/main/test/conformance/helpers/sources

### What the suite checks

- The `YipYapSource` CR applies and registers with the API server.
- Reconcile reports `SinkProvided=True` and `Ready=True` within 5 minutes.
- `Status.SinkURI` is populated after `Ready=True`.
- `Status.CloudEventAttributes` advertises at least one entry, each with a
  non-empty `type`.
- The adapter `Deployment` named by `Status.DeploymentName` exists and is
  `Available`.
- Deleting the `YipYapSource` cascades to the adapter `Deployment` via owner
  references.

### What is NOT covered yet

- End-to-end event flow (requires a running `yipyap` backend; covered by
  `test/integration/knative/` in the main yipyap repository).
- OIDC authentication between adapter and sink (Phase 2 concern, exercised
  in the yipyap repository).
- Upstream `SourceCRDMetadataTestHelper` and `SourceCRDRBACTestHelper` —
  these assert the CRD manifest ships specific printer columns and that the
  controller ServiceAccount holds the canonical `sources.knative.dev/source`
  aggregated ClusterRole. Adding them requires importing `testlib`; tracked
  separately.
- High-scale / long-running behavior.

## Running locally

Prerequisites:

- A Kubernetes cluster (kind is fine).
- [Knative Eventing][ke-install] installed in the cluster.
- The `yipyap-source` chart installed:

  ```bash
  helm install yipyap-source ./chart/yipyap-source \
    -n yipyap-sources --create-namespace
  kubectl wait --for=condition=Available deployment \
    -n yipyap-sources --all --timeout=300s
  ```

- The adapter image (`ghcr.io/yipyap-run/knative-source:latest`) reachable
  from the cluster's node(s). When using kind, `kind load docker-image` the
  three images (controller, webhook, adapter) after building them locally.

Then:

```bash
KUBECONFIG=$HOME/.kube/config \
  go test -tags conformance ./test/conformance/... -v -timeout 10m
```

The build tag `conformance` gates the package; a default `go test ./...`
does not compile or execute these tests. If `KUBECONFIG` is not set and no
default kubeconfig is discoverable, the suite exits cleanly with a skip
notice — this keeps `go test -tags conformance ./...` a useful build check
on developer machines.

[ke-install]: https://knative.dev/docs/install/

## CI

A `Conformance` workflow at `.github/workflows/conformance.yaml` runs the
suite against a freshly-provisioned kind cluster. It's gated two ways:

1. Add the `conformance` label to a pull request — the workflow fires on the
   `labeled` event.
2. Trigger manually via the Actions tab (`workflow_dispatch`).

The workflow is marked `continue-on-error: true` while the harness matures;
once it's proven stable across multiple runs we'll flip the flag. The
workflow currently expects the controller/webhook/adapter images to be
available as published artifacts; building them inside CI is a follow-up.

## Troubleshooting

- **Tests hang on "Ready never reached True"** — the subtest dumps every
  condition on the source before failing. Check `SinkProvided`: it should be
  `True` if the sink ref resolves, `False` with a reason if the controller
  rejected the ref, or `Unknown` if reconcile hasn't run at all.
- **"adapter Deployment not Available"** — usually means the adapter image
  can't be pulled, or the adapter is crash-looping on a missing API key.
  `kubectl describe deploy -n <conformance-ns> <deploymentName>`.
- **Namespace leaks after a test crash** — every test registers a cleanup
  that deletes its namespace. If Go panics before cleanup runs, delete
  leftover namespaces with `kubectl delete ns -l yipyap.run/conformance=true`.
