# Contribution guidelines

So you want to hack on `knative-source`? Yay! Please refer to Knative's
overall [contribution guidelines](https://www.knative.dev/contributing/) to find
out how you can help.

## Auto-synced upstream files

A small set of repository files are **auto-synced** from upstream Knative
infrastructure (the `knative-extensions/knobots` bot) and will be overwritten
on the next sync cycle. Do not hand-edit them — open an issue against
`knative/actions` upstream instead.

The auto-synced files are:

- `.github/workflows/knative-go-build.yaml`
- `.github/workflows/knative-go-test.yaml`
- `.github/workflows/knative-security.yaml`
- `.github/workflows/knative-stale.yaml`
- `.github/workflows/knative-style.yaml`
- `.github/workflows/knative-verify.yaml`

Each delegates to a reusable workflow at
`knative/actions/.github/workflows/reusable-*.yaml@main`. We deliberately
**cannot SHA-pin or tag-pin these references locally** because:

1. The knobots sync rewrites the file to `@main` on every cycle, so any
   local pin would be reverted within hours.
2. Upstream `knative/actions` does not currently publish stable tags for
   the reusable workflows it ships; `@main` is the supported pinning
   posture for downstream Knative-ecosystem repos.

This is a documented residual risk (security-review A5-NEW-3 / H-9
partial). The trust relationship is "upstream Knative org is in our supply
chain"; if the `knative/actions` repo were compromised, the next CI run on
this repo would replay the compromise. Mitigations:

- All non-reusable workflows in this repo (`release.yaml`, `conformance.yaml`)
  use `@<40-char-sha>` SHA pins.
- The reusable workflows only run read-only checks (build, test, style,
  verify) and do not have access to the release credentials (`GHCR_PAT`,
  `GORELEASER_KEY`); credentials live solely on `release.yaml`.
- A push to `knative/actions:main` is reviewed by the upstream Knative
  release team before merging.

If upstream ever publishes tagged releases for these reusable workflows,
update the references here at the same time the knobots sync template is
updated upstream.

## Auto-synced CRD validation manifests

The static `config/300-yipyapsource.yaml` CRD and the chart's
`templates/crd.yaml` carry the same OpenAPI v3 schema floor (security-review
A5-NEW-1). When you add or remove a field in
`pkg/apis/sources/v1alpha1/yipyapsource_types.go`, update **both** copies
of the CRD schema.
