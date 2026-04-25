# Releasing

The yipyap-source release ships **three container images** and **one Helm
chart**, all at the same version number. The version is the source of
truth for everything: container tags, chart `version`, chart `appVersion`,
and the git tag that triggers the release.

| Artifact | Built by | Repo |
|---|---|---|
| `ghcr.io/yipyap-run/knative-source-controller` | this repo (`cmd/controller`) | knative-source |
| `ghcr.io/yipyap-run/knative-source-webhook` | this repo (`cmd/webhook`) | knative-source |
| `ghcr.io/yipyap-run/knative-source` (receive-adapter) | the **alerts** repo (`cmd/yipyap-knative-source`) | YipYap |
| `yipyap-source` Helm chart | this repo (`chart/yipyap-source`) | knative-source → gh-pages |

The chart references all three images at `appVersion`. If they don't all
exist at the same tag, `helm install` ImagePullBackOffs.

## Cutting a release

The flow is two repos, one tag string. Pick a version (e.g. `v0.2.0`) and
do the following in order:

### 1. Release the receive-adapter from the alerts repo

In the alerts repo:

```sh
# Verify a clean state and a passing CI run on main.
git checkout main && git pull
git status # tree must be clean

# Tag and push.
git tag v0.2.0
git push origin v0.2.0
```

The alerts repo's GoReleaser workflow runs on tag push and publishes:

- `ghcr.io/yipyap-run/knative-source:0.2.0` (the receive-adapter)
- All other yipyap binaries at the same tag

Wait for the workflow to finish (~5 minutes). Verify the image landed:

```sh
docker manifest inspect ghcr.io/yipyap-run/knative-source:0.2.0
```

### 2. Bump and tag this repo

In the knative-source repo:

```sh
git checkout main && git pull

# Edit chart/yipyap-source/Chart.yaml — bump BOTH:
#   version: 0.2.0
#   appVersion: 0.2.0
# Edit annotations.artifacthub.io/changes if you want a changelog entry.

git add chart/yipyap-source/Chart.yaml
git commit -m "chart: release 0.2.0"
git push origin main

# Tag the same commit and push.
git tag v0.2.0
git push origin v0.2.0
```

The tag push triggers `.github/workflows/release.yaml` which:

1. **Validates lockstep.** Refuses to release unless the tag's stripped
   version (`0.2.0` from `v0.2.0`) equals both `Chart.yaml` `version:` AND
   `appVersion:`. Mismatch = workflow fails fast.
2. **Builds + pushes images.** `ko publish` for `cmd/controller` and
   `cmd/webhook` to `ghcr.io/yipyap-run/knative-source-{controller,webhook}`
   tagged `0.2.0`, multi-arch (amd64 + arm64).
3. **Releases the chart.** `chart-releaser-action` packages the chart
   from `chart/yipyap-source` and publishes to gh-pages with the
   index.yaml pointing at the tarball.

Total time end-to-end: ~6-8 minutes.

### 3. Verify

```sh
# Images exist at the new tag.
docker manifest inspect ghcr.io/yipyap-run/knative-source-controller:0.2.0
docker manifest inspect ghcr.io/yipyap-run/knative-source-webhook:0.2.0
docker manifest inspect ghcr.io/yipyap-run/knative-source:0.2.0  # adapter, from alerts release

# Chart is published.
helm repo update
helm search repo yipyap-source --versions | head -5
# yipyap-source/yipyap-source  0.2.0  0.2.0  YipYapSource CRD + controller …

# Smoke install on a kind cluster.
helm install yipyap-source yipyap-source/yipyap-source \
  --version 0.2.0 \
  -n yipyap-source-system --create-namespace
kubectl -n yipyap-source-system get pods
```

If the smoke install ImagePullBackOffs, one of the three images is
missing at the chart's `appVersion`. Check which one with
`kubectl describe pod`.

## What if the alerts release runs after the knative-source release?

The `release.yaml` workflow on this repo doesn't currently verify the
adapter image exists at the chart's `appVersion`. So if you tag this repo
without having tagged alerts first, the workflow succeeds and publishes
a chart whose adapter image is missing. End-users hit ImagePullBackOff
on `kubectl describe pod`.

Mitigation: just follow the order. If you slipped, tag alerts at the
same version after the fact — once the adapter image exists at the
chart's `appVersion`, the chart starts working without a re-release.

## What if I need to ship a chart-only fix?

Bump only `chart/yipyap-source/Chart.yaml` `version:` (e.g. `0.2.1`),
leave `appVersion: 0.2.0`. Then tag `v0.2.1`. The validation step
permits version != appVersion only when the version is a strict superset
(`0.2.1 > 0.2.0`); the workflow will need a small extension to honour
this.

**Today, the lockstep check is strict equality** — version-only chart
fixes are not yet supported. The pragmatic workaround is to also tag a
matching alerts release at the new version (even if no alerts code
changed) so all three images publish at the new tag. Chart appVersion
gets bumped to match.

## Pre-release tags

The version regex accepts `v0.2.0-rc.1`, `v0.2.0-alpha.3`, etc. Chart
versions follow the same pattern. Pre-release tags publish to GHCR and
gh-pages identically to GA — no extra channel separation today.

## Rolling back

GHCR images are immutable once pushed; you can't "untag" a bad release.
To roll back:

1. Cut a new patch version that contains the fix (e.g. `v0.2.1` after a
   broken `v0.2.0`).
2. Update `foss/overlay/deploy/knative/containersource.yaml` in the
   alerts repo to pin the new version.
3. Tell users to bump their `--version` flag on `helm upgrade`.

If the bad release is ALREADY in production at customer sites, do not
delete the gh-pages chart entry — that breaks `helm rollback`. Mark it
as deprecated via Chart.yaml `deprecated: true` and ship a new minor.
