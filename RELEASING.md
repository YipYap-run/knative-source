# Releasing

The yipyap-source release ships **three container images** and **one Helm
chart**, all at the same version number. The version is the source of
truth for everything: container tags, chart `version`, chart `appVersion`,
and the git tag that triggers the release.

| Artifact | Path | Image / package |
|---|---|---|
| Controller | `cmd/controller` | `ghcr.io/yipyap-run/knative-source-controller` |
| Webhook | `cmd/webhook` | `ghcr.io/yipyap-run/knative-source-webhook` |
| Receive adapter | `cmd/receive-adapter` | `ghcr.io/yipyap-run/knative-source-receive-adapter` |
| Helm chart | `chart/yipyap-source` | gh-pages → `https://yipyap-run.github.io/knative-source` |

The published chart pins all three images by the digests its release
pushed, so a chart version always deploys the same bytes. They all publish
from one tag-driven workflow, so the set is always self-consistent.

## Cutting a release

Pick a version (e.g. `v0.2.0`) and:

```sh
git checkout main && git pull

# Edit chart/yipyap-source/Chart.yaml - bump BOTH:
#   version: 0.2.0
#   appVersion: 0.2.0
# Edit annotations.artifacthub.io/changes if you want a changelog entry.

git add chart/yipyap-source/Chart.yaml
git commit -m "chart: release 0.2.0"
git push origin main

git tag v0.2.0
git push origin v0.2.0
```

The tag push triggers `.github/workflows/release.yaml`, which:

1. **Validates lockstep.** Refuses to release unless the tag's stripped
   version (`0.2.0` from `v0.2.0`) equals both `Chart.yaml` `version:` and
   `appVersion:`. Mismatch fails fast.
2. **Runs CI.** Calls `.github/workflows/ci.yaml` on the tagged commit:
   build, vet, unit and envtest suites, lint, codegen check, chart checks,
   `ko resolve`, and the kind smoke test. Nothing is published unless every
   job passes. To retry after a flaky run, re-run the failed jobs of the
   release workflow.
3. **Refuses to overwrite.** `hack/release-tag-absent.sh` checks that
   none of the three `:0.2.0` tags exist in GHCR, before anything is built
   and again right before each push. An existing tag, or any registry error
   other than "not found", fails the release.
4. **Builds + pushes images.** `ko publish` for `cmd/controller`,
   `cmd/webhook`, and `cmd/receive-adapter` to the matching GHCR
   repositories tagged `0.2.0`, multi-arch (amd64 + arm64), and records
   each pushed digest.
5. **Signs and attests.** The `sign` job, the only one with
   `id-token: write`, signs each digest with cosign keyless and pushes a
   SLSA build provenance attestation next to it, then verifies both.
6. **Releases the chart.** `hack/set-image-digests.sh` writes the three
   digests into `values.yaml`, the job checks the rendered chart deploys
   them, packages it, and `chart-releaser-action` publishes the package to
   gh-pages with `index.yaml` pointing at the tarball.

Total time end-to-end: about 20 minutes, most of it the CI checks.

### Verify

```sh
# Images exist at the new tag.
docker manifest inspect ghcr.io/yipyap-run/knative-source-controller:0.2.0
docker manifest inspect ghcr.io/yipyap-run/knative-source-webhook:0.2.0
docker manifest inspect ghcr.io/yipyap-run/knative-source-receive-adapter:0.2.0

# Chart is published and pins the digests.
helm repo update
helm search repo yipyap-source --versions | head -5
helm show values yipyap-source/yipyap-source --version 0.2.0 | grep digest:

# Signatures and provenance: see README.md "Verifying release images".

# Smoke install on a kind cluster. The NetworkPolicy is on by default and
# needs the kube-apiserver addresses.
hack/apiserver-cidrs-values.sh > /tmp/apiserver-values.yaml
helm install yipyap-source yipyap-source/yipyap-source \
  --version 0.2.0 \
  -n yipyap-sources --create-namespace \
  -f /tmp/apiserver-values.yaml
kubectl -n yipyap-sources get pods
```

If the smoke install hits ImagePullBackOff, one of the three images is
missing at the digest the chart pins. Check which with `kubectl describe pod`.

### A release failed part way

Re-run the failed jobs of the same workflow run. Image jobs that already
pushed are not re-run, and the sign and chart jobs pick up their recorded
digests. If an image job itself fails after its push, its tag now exists
and the tag check refuses to push it again: cut the next patch version
instead of trying to overwrite it.

## Chart-only fixes

Bump only `chart/yipyap-source/Chart.yaml` `version:` (e.g. `0.2.1`) and
leave `appVersion: 0.2.0`. Today the lockstep check is strict equality, so
chart-only revisions need a matching tag and image rebuild (usually fine
because rebuilds are cheap). If chart-only revisions become common, extend
the validate step to allow `version >= appVersion`.

## Pre-release tags

The version regex accepts `v0.2.0-rc.1`, `v0.2.0-alpha.3`, etc. Chart
versions follow the same pattern. Pre-release tags publish to GHCR and
gh-pages identically to GA; no extra channel separation today.

## Base image updates

`.github/workflows/base-image.yaml` runs every Monday (and on demand). It
runs `hack/bump-base-image.sh`, which repins `cgr.dev/chainguard/static`
in `.ko.yaml` to the current digest of `:latest`, builds every image with
it, and opens a PR. PRs opened by the workflow token do not start CI, so
close and reopen the PR to run CI before merging. To bump by hand, run
`hack/bump-base-image.sh` (needs `crane`).

## Rolling back

Published tags are never overwritten; the release refuses to push a tag
that exists.
To roll back:

1. Cut a new patch version that contains the fix (e.g. `v0.2.1` after a
   broken `v0.2.0`).
2. Bump consumers' `--version` flag on `helm upgrade`.

If the bad release is already in production at customer sites, do not
delete the gh-pages chart entry (that breaks `helm rollback`). Mark it as
deprecated via `Chart.yaml`'s `deprecated: true` and ship a new minor.
