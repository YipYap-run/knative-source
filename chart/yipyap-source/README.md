# yipyap-source

Helm chart for the YipYap Knative event source — installs the `YipYapSource`
CRD, the reconciling controller, and the admission webhook.

## TL;DR

```bash
helm repo add yipyap-source https://yipyap-run.github.io/knative-source
helm repo update
helm install yipyap-source yipyap-source/yipyap-source \
  --namespace yipyap-sources --create-namespace
```

## Prerequisites

- Kubernetes 1.27+ (CRD conversion webhooks, admission v1).
- Knative Eventing installed in the cluster (for sinks — Broker/Channel/Service).
- Outbound network access from cluster to your YipYap control plane
  (either SaaS `https://*.yipyap.run` or your self-hosted install).

## Installation

### Default install

```bash
helm install yipyap-source yipyap-source/yipyap-source \
  --namespace yipyap-sources --create-namespace
```

### Pin image tags

```bash
helm install yipyap-source yipyap-source/yipyap-source \
  --namespace yipyap-sources --create-namespace \
  --set images.controller.tag=0.1.0 \
  --set images.webhook.tag=0.1.0 \
  --set images.adapter.tag=0.1.0
```

### Skip CRD management

If you manage CRDs out-of-band (Argo, Flux, cluster operator), disable the
bundled CRD:

```bash
helm install yipyap-source yipyap-source/yipyap-source \
  --namespace yipyap-sources --create-namespace \
  --set installCRD=false
```

## Values

See [`values.yaml`](./values.yaml) for the authoritative list. Highlights:

| Key                         | Default                                            | Description                                                  |
|-----------------------------|----------------------------------------------------|--------------------------------------------------------------|
| `namespace.name`            | `yipyap-sources`                                   | Namespace for controller + webhook.                          |
| `namespace.create`          | `true`                                             | Whether Helm should create the namespace.                    |
| `images.controller.repository` | `ghcr.io/yipyap-run/knative-source-controller`  | Controller container image.                                  |
| `images.webhook.repository` | `ghcr.io/yipyap-run/knative-source-webhook`        | Webhook container image.                                     |
| `images.adapter.repository` | `ghcr.io/yipyap-run/knative-source`                | Receive-adapter image the controller deploys per source.     |
| `images.*.tag`              | `""`                                               | Falls back to `.Chart.AppVersion` when empty.                |
| `controller.replicas`       | `1`                                                | Controller replicas.                                         |
| `controller.scopedNamespaces` | `[]`                                             | If non-empty, render a per-namespace `Role`+`RoleBinding` granting the controller SA Deployment/ConfigMap access only in those namespaces (see Security model). |
| `webhook.replicas`          | `2`                                                | Webhook replicas (HA + PDB minAvailable=1).                  |
| `webhook.failurePolicy`     | `Fail`                                             | Admission webhook failure policy (`Fail` / `Ignore`).        |
| `webhook.port`              | `8443`                                             | HTTPS port the webhook listens on.                           |
| `metrics.enabled`           | `true`                                             | Expose Prometheus metrics on `:9090`.                        |
| `installCRD`                | `true`                                             | Whether Helm installs the `YipYapSource` CRD.                |

## Security model

### Controller does NOT read Secrets cluster-wide

The controller ClusterRole does not grant any Secret access. The
reconciler does not call `coreV1().Secrets().Get()`
anywhere — the per-source API key Secret is resolved Pod-side at adapter
startup via a `SecretKeyRef` envvar, scoped by kubelet to the
YipYapSource's own namespace.

This means a compromised controller SA cannot exfiltrate Secrets from
unrelated tenants — the blast radius of an SA token compromise is bounded
to the cluster-wide read of `configmaps`, `events`, and the
namespace-scoped CRUD on adapter `Deployments`.

### Tightening blast radius further: `controller.scopedNamespaces`

By default the controller has cluster-wide CRUD on `apps/deployments` and
read on `""/configmaps` so it can reconcile receive-adapter Deployments in
any namespace where a `YipYapSource` is created. To narrow this further,
set `controller.scopedNamespaces` to the list of namespaces where
`YipYapSource` resources are allowed to live:

```yaml
controller:
  scopedNamespaces:
    - team-a
    - team-b
```

This currently emits per-namespace `Role` + `RoleBinding` pairs in addition
to the cluster-wide ClusterRole; pair with a Kyverno/OPA admission policy
that rejects `YipYapSource` creation outside the listed namespaces, and
remove the cluster-wide deployment/configmap rules in your overlay if you
require strict per-namespace scoping.

### Webhook cert rotation

The webhook self-signs a TLS CA at first startup via
`knative.dev/pkg/webhook/certificates`. The CA bundle is patched into the
admission webhook configurations on each pod startup; the in-Secret cert
itself rotates via the same controller loop on a 30-day cadence by default.

For operators who want managed rotation:

- **cert-manager** — annotate the `yipyap-source-webhook-certs` Secret and
  point a `cert-manager.io/Issuer` at it; disable the in-process rotation
  by overriding `webhook.failurePolicy` and supplying your own bundle.
- **Manual rollout** — `kubectl rollout restart deployment/yipyap-source-webhook
  -n yipyap-sources` will trigger a fresh in-process rotation; webhook HA
  (`replicas: 2` + PDB `minAvailable: 1`) keeps admission responsive
  during the rollover.

## Upgrade notes

- The webhook self-signs a CA at startup — there is no cert-manager dependency
  and no chart-side TLS material to rotate by default. See "Webhook cert
  rotation" above for managed-rotation options.
- CRD updates are applied in-place when `installCRD=true`. If you need
  CRD conversion between minor versions, gate the CRD with `installCRD=false`
  and manage it separately.

## Uninstall

```bash
helm uninstall yipyap-source --namespace yipyap-sources
```

Helm will refuse to delete the CRD if any `YipYapSource` resources still
exist; delete those first:

```bash
kubectl delete yipyapsources --all --all-namespaces
```

## Documentation

- User guide: <https://docs.yipyap.run/integrations/knative/source-crd/>
- Source: <https://github.com/YipYap-run/knative-source>
