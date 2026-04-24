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
| `webhook.replicas`          | `1`                                                | Webhook replicas.                                            |
| `webhook.failurePolicy`     | `Fail`                                             | Admission webhook failure policy (`Fail` / `Ignore`).        |
| `webhook.port`              | `8443`                                             | HTTPS port the webhook listens on.                           |
| `metrics.enabled`           | `true`                                             | Expose Prometheus metrics on `:9090`.                        |
| `installCRD`                | `true`                                             | Whether Helm installs the `YipYapSource` CRD.                |

## Security model

### Controller cluster-wide Secret access (by design)

The controller ClusterRole grants `get`/`list`/`watch` on `secrets` across the
whole cluster. This is the canonical Knative source pattern: a `YipYapSource`
can live in any namespace, and its `spec.apiKeyRef.name` points to a Secret in
that same namespace which the controller must read at reconcile time to stamp
the receive-adapter Deployment with the API key as an env var.

Kubernetes RBAC cannot scope-by-resource-name on `secrets` in a ClusterRole
when the names are not known up front, so the permission model is binary:
either the controller watches all namespaces (current default, convenient) or
operators pre-declare a scoped namespace list.

**If you need tighter blast radius**, plan for (not yet implemented) a
`controller.scopedNamespaces` Helm value that will generate a Role +
RoleBinding per listed namespace instead of a cluster-wide ClusterRole.
Operators who cannot tolerate cluster-wide secret read should pin
`images.controller.tag` and run with `scopedNamespaces` once landed, or apply
a sidecar admission policy (Kyverno / OPA Gatekeeper) restricting where
`YipYapSource` resources may be created.

Tracked as security-review finding H-6.

## Upgrade notes

- The webhook self-signs a CA at startup — there is no cert-manager dependency
  and no chart-side TLS material to rotate.
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
