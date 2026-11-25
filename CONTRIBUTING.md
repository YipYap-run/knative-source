# Contribution guidelines

So you want to hack on `knative-source`? Yay! Please refer to Knative's
overall [contribution guidelines](https://www.knative.dev/contributing/) to find
out how you can help.

## CI

`.github/workflows/ci.yaml` runs on every pull request and every push to
`main`:

- `go build`, `go vet` (also with the `envtest` and `conformance` tags),
  gofmt and `go test ./...` with helm on PATH, so the chart tests run.
- The envtest suite (`make test-envtest`), with setup-envtest and the
  Kubernetes version pinned in the Makefile.
- golangci-lint and `hack/verify-codegen.sh`.
- `hack/verify-text.sh` (see Writing style below).
- `helm lint` and `helm template`, a check that the chart refuses to render
  without `networkPolicy.apiServerCIDRs`, `ko resolve -f config/`, and
  shellcheck on the scripts CI runs.
- A kind smoke test (`test/smoke/smoke.sh`): a cluster with Calico so the
  chart's NetworkPolicy is enforced, images built with ko from the commit,
  the chart installed, and `example.yaml` applied against a stub YipYap API
  (`test/smoke/stub`). It passes when admission rejects bad base URLs, the
  source is Ready, its adapter Deployment is Available, and the stub saw
  the event delivered and confirmed. Run it locally with
  `test/smoke/smoke.sh all` (needs kind, kubectl, helm, ko, docker,
  openssl and yq).

`release.yaml` calls the same workflow on the tagged commit and publishes
nothing unless it passes.

All workflows pin every third-party action by 40-character commit SHA and
every downloaded manifest by SHA-256. Bump pins deliberately, with a
comment in the PR explaining what changed and why.

## Synced CRD validation manifests

The static `config/300-yipyapsource.yaml` CRD and the chart's
`templates/crd.yaml` carry the same OpenAPI v3 schema floor. When you add
or remove a field in `pkg/apis/sources/v1alpha1/yipyapsource_types.go`,
update **both** copies of the CRD schema.

`go test ./test/crd/` fails when the two specs differ, and runs both CRDs
through the apiserver's CRD validation (structural schema rules included),
so it catches a CRD the apiserver would refuse to register. The envtest
suite (`make test-envtest`) installs the chart CRD, not a separate copy.
`go test ./test/config/` checks that every `ko://` image in `config/`
names a main package in this repo.

## Static manifests under config/

`config/` holds the static manifests used by `ko apply -f config/` and by
some tests. It is a development install path, not a hardened production one:
it does not include the Helm chart's NetworkPolicies or its webhook
PodDisruptionBudget. Install production clusters with the chart, and update
`config/` when you change a resource that both paths ship.

## Writing style

Comments, docs, chart values and workflow files use plain, short
sentences. Use a comma, colon, period or parentheses instead of an em or
en dash. Describe the code as it is now; history belongs in commit
messages. New files carry the Apache 2.0 header with
`Copyright <year> The YipYap Authors`; files that came from the Knative
sample source keep their upstream header. `hack/verify-text.sh` checks
these rules (and a short list of overused words) on every pull request;
`hack/verify-text-test.sh` tests the script itself.
