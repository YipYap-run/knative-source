# Development

This repo hosts the `YipYapSource` CRD + controller for the YipYap Knative
integration. It follows Knative's `sample-source` template patterns (module
layout, `hack/update-codegen.sh`, `knative.dev/pkg` injection framework,
`ko`-based image builds); we track upstream sample-source changes where they
are applicable.

## Requirements

- [Go](https://go.dev/doc/install) at the version pinned in `go.mod`.
- [ko](https://ko.build/) for building and publishing controller/webhook images.
- A working Kubernetes cluster with Knative Eventing installed.

## Common tasks

```bash
# Build everything.
go build ./...

# Run the unit tests.
go test ./...

# Regenerate clientsets, listers, informers, and deepcopy helpers after
# changing API types under pkg/apis/.
./hack/update-codegen.sh

# Verify that generated code is up-to-date (CI does this too).
./hack/verify-codegen.sh
```

## Image builds

Controller and webhook images are published via `ko` to
`ghcr.io/yipyap-run/knative-source`. Overrides are in [`.ko.yaml`](./.ko.yaml).

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) and the project security policy in
[SECURITY.md](./SECURITY.md).
