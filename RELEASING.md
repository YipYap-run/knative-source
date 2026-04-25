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

The chart references all three images at `appVersion`. They all publish
from one tag-driven workflow, so the set is always self-consistent.

## Cutting a release

Pick a version (e.g. `v0.2.0`) and:

```sh
git checkout main && git pull

# Edit chart/yipyap-source/Chart.yaml — bump BOTH:
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
2. **Builds + pushes images.** `ko publish` for `cmd/controller`,
   `cmd/webhook`, and `cmd/receive-adapter` to the matching GHCR
   repositories tagged `0.2.0`, multi-arch (amd64 + arm64).
3. **Releases the chart.** `chart-releaser-action` packages the chart and
   publishes to gh-pages with `index.yaml` pointing at the tarball.

Total time end-to-end: ~6–8 minutes.

### Verify

```sh
# Images exist at the new tag.
docker manifest inspect ghcr.io/yipyap-run/knative-source-controller:0.2.0
docker manifest inspect ghcr.io/yipyap-run/knative-source-webhook:0.2.0
docker manifest inspect ghcr.io/yipyap-run/knative-source-receive-adapter:0.2.0

# Chart is published.
helm repo update
helm search repo yipyap-source --versions | head -5

# Smoke install on a kind cluster.
helm install yipyap-source yipyap-source/yipyap-source \
  --version 0.2.0 \
  -n yipyap-source-system --create-namespace
kubectl -n yipyap-source-system get pods
```

If the smoke install hits ImagePullBackOff, one of the three images is
missing at the chart's `appVersion`. Check which with `kubectl describe pod`.

## Chart-only fixes

Bump only `chart/yipyap-source/Chart.yaml` `version:` (e.g. `0.2.1`) and
leave `appVersion: 0.2.0`. Today the lockstep check is strict equality, so
chart-only revisions need a matching tag and image rebuild — usually fine
because rebuilds are cheap. If chart-only revisions become common, extend
the validate step to allow `version >= appVersion`.

## Pre-release tags

The version regex accepts `v0.2.0-rc.1`, `v0.2.0-alpha.3`, etc. Chart
versions follow the same pattern. Pre-release tags publish to GHCR and
gh-pages identically to GA — no extra channel separation today.

## Rolling back

GHCR images are immutable once pushed; you can't "untag" a bad release.
To roll back:

1. Cut a new patch version that contains the fix (e.g. `v0.2.1` after a
   broken `v0.2.0`).
2. Bump consumers' `--version` flag on `helm upgrade`.

If the bad release is already in production at customer sites, do not
delete the gh-pages chart entry — that breaks `helm rollback`. Mark it as
deprecated via `Chart.yaml`'s `deprecated: true` and ship a new minor.
