# Contribution guidelines

So you want to hack on `knative-source`? Yay! Please refer to Knative's
overall [contribution guidelines](https://www.knative.dev/contributing/) to find
out how you can help.

## CI

All workflows in this repo (`release.yaml`, `conformance.yaml`) pin every
third-party action by 40-character commit SHA. Bump pins deliberately,
with a comment in the PR explaining what changed and why.

## Synced CRD validation manifests

The static `config/300-yipyapsource.yaml` CRD and the chart's
`templates/crd.yaml` carry the same OpenAPI v3 schema floor. When you add
or remove a field in `pkg/apis/sources/v1alpha1/yipyapsource_types.go`,
update **both** copies of the CRD schema.
