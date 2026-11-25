# YipYap Knative Source

`YipYapSource` is a Knative event source that delivers YipYap CloudEvents to
any Knative sink. It provisions an adapter Deployment per `YipYapSource` CR
that subscribes to YipYap's event stream (via the `yipyap-knative-source`
container image) and forwards events to the configured sink.

Delivery to the sink is at least once, so sinks should dedupe on the
CloudEvent `id`. See [docs/delivery.md](./docs/delivery.md) for the full
contract.

Apache-2.0 licensed.

## Install

```bash
helm repo add yipyap-source https://yipyap-run.github.io/knative-source
helm repo update
helm install yipyap-source yipyap-source/yipyap-source \
  --namespace yipyap-sources \
  --create-namespace \
  --set 'networkPolicy.apiServerCIDRs={172.18.0.2/32}'
```

The chart's NetworkPolicy is on by default and needs your kube-apiserver
address range in `networkPolicy.apiServerCIDRs`; install fails with an error
until it is set. See the [chart README](./chart/yipyap-source/README.md#networkpolicy).

The Helm chart is the supported install path and the only one that applies
this repository's NetworkPolicy hardening. The static manifests under
`config/` are for development with `ko apply -f config/`; they do not render
the chart's NetworkPolicies or the webhook PodDisruptionBudget, so do not use
them as a hardened production install. See
[CONTRIBUTING.md](./CONTRIBUTING.md#static-manifests-under-config).

`kubectl get yipyapsources` shows `READY=False` with a plain reason such as
`APIKeyRejected` when the adapter cannot use its API key, reach the API or
deliver to the sink. See [Source status](./docs/status.md).

Sinks that use TLS with a private CA or require OIDC tokens (Knative
transport encryption and authentication) work without extra setup. See
[Sink TLS and OIDC](./docs/sink-tls-oidc.md).

See the full documentation at <https://docs.yipyap.run/integrations/knative/source-crd/>.

## Verifying release images

Every release image is signed with [cosign](https://docs.sigstore.dev/)
keyless signing and carries a SLSA build provenance attestation, both made
by this repository's release workflow. Published charts deploy each image by
digest, and tags are never overwritten.

The release signs with cosign v3, so verify with cosign v3 or later.
Get the digests a chart version deploys, then verify each image:

```bash
helm show values yipyap-source/yipyap-source --version 0.2.0 | grep digest:

IMAGE=ghcr.io/yipyap-run/knative-source-controller@sha256:<digest>

# Signature: made by release.yaml in YipYap-run/knative-source, from a
# version tag or from main (manual re-runs of a tag start from main).
cosign verify "$IMAGE" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/YipYap-run/knative-source/\.github/workflows/release\.yaml@refs/(tags/v[0-9].*|heads/main)$'

# Build provenance.
gh attestation verify "oci://$IMAGE" --repo YipYap-run/knative-source --bundle-from-oci
```

Repeat for `knative-source-webhook` and `knative-source-receive-adapter`.
Admission policies such as the Sigstore policy-controller or Kyverno
`verifyImages` can enforce the same identity in the cluster.

## Status

API version: `sources.yipyap.run/v1alpha1`.
