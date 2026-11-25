# yipyap-source

Helm chart for the YipYap Knative event source. Installs the `YipYapSource`
CRD, the reconciling controller, and the admission webhook.

## TL;DR

```bash
helm repo add yipyap-source https://yipyap-run.github.io/knative-source
helm repo update
helm install yipyap-source yipyap-source/yipyap-source \
  --namespace yipyap-sources --create-namespace \
  --set 'networkPolicy.apiServerCIDRs={172.18.0.2/32}'
```

Replace the CIDR with your kube-apiserver address range (see
[NetworkPolicy](#networkpolicy)).

## Prerequisites

- Kubernetes 1.27+ (CRD conversion webhooks, admission v1).
- Knative Eventing installed in the cluster (for sinks: Broker, Channel, or Service).
- Outbound network access from cluster to your YipYap control plane
  (either SaaS `https://*.yipyap.run` or your self-hosted install).

## Installation

### Default install

```bash
helm install yipyap-source yipyap-source/yipyap-source \
  --namespace yipyap-sources --create-namespace \
  --set 'networkPolicy.apiServerCIDRs={172.18.0.2/32}'
```

The other examples below omit `networkPolicy.apiServerCIDRs` for brevity;
every install with the NetworkPolicy enabled needs it.

The chart is the only install path that renders NetworkPolicies. The static
manifests under `config/` in the source repository are for development and
do not include them or the webhook PodDisruptionBudget, so they are not a
hardened production install.

### Image digests

Published charts pin every image by digest (`images.*.digest`), so a chart
version always deploys the images its release built and signed. To verify
them, see [Verifying release images](../../README.md#verifying-release-images).

To deploy a tag instead (for example a locally built image), clear the
digest too. Setting both `tag` and `digest` fails the install, since the tag
would be ignored.

```bash
helm install yipyap-source yipyap-source/yipyap-source \
  --namespace yipyap-sources --create-namespace \
  --set images.controller.tag=0.1.0 --set images.controller.digest="" \
  --set images.webhook.tag=0.1.0 --set images.webhook.digest="" \
  --set images.adapter.tag=0.1.0 --set images.adapter.digest=""
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
| `namespace.create`          | `true`                                             | Deploy into `namespace.name`. The chart creates it only when it is not the release namespace (use `--create-namespace` for that). |
| `images.controller.repository` | `ghcr.io/yipyap-run/knative-source-controller`  | Controller container image.                                  |
| `images.webhook.repository` | `ghcr.io/yipyap-run/knative-source-webhook`        | Webhook container image.                                     |
| `images.adapter.repository` | `ghcr.io/yipyap-run/knative-source-receive-adapter` | Receive-adapter image the controller deploys per source.     |
| `images.*.tag`              | `""`                                               | Falls back to `.Chart.AppVersion` when empty.                |
| `images.*.digest`           | `""` in git, set in published charts               | `sha256:<64 hex>`. When set, the chart deploys `repository@digest`. |
| `controller.replicas`       | `1`                                                | Controller replicas.                                         |
| `controller.scopedNamespaces` | `[]`                                             | If non-empty, grant Deployment writes (and Pod list) only through a per-namespace `Role`+`RoleBinding` in those namespaces. Reads stay cluster-wide (see Security model). |
| `webhook.replicas`          | `2`                                                | Webhook replicas (HA + PDB minAvailable=1).                  |
| `webhook.failurePolicy`     | `Fail`                                             | Admission webhook failure policy (`Fail` / `Ignore`).        |
| `webhook.port`              | `8443`                                             | HTTPS port the webhook listens on.                           |
| `webhook.allowedBaseURLHosts` | `["console.yipyap.run"]`                         | Hosts a `YipYapSource` `spec.baseURL` may use (see Base URL policy). Must not be empty. |
| `metrics.enabled`           | `true`                                             | Expose Prometheus metrics on `:9090`.                        |
| `installCRD`                | `true`                                             | Whether Helm installs the `YipYapSource` CRD.                |
| `networkPolicy.enabled`     | `true`                                             | Render NetworkPolicies for the controller and webhook pods.  |
| `networkPolicy.apiServerCIDRs` | `[]`                                            | kube-apiserver address ranges. Required when `networkPolicy.enabled` is true, unless `networkPolicy.cilium.useEntities` is true. |
| `networkPolicy.apiServerPorts` | `[443, 6443]`                                   | TCP ports the API server endpoints listen on.                |
| `networkPolicy.webhookIngressCIDRs` | `[]`                                       | Sources allowed to call the webhook port. Empty means `apiServerCIDRs`. |
| `networkPolicy.cilium.useEntities` | `false`                                     | Also render CiliumNetworkPolicies for the `kube-apiserver` entity. |

## NetworkPolicy

The chart renders a NetworkPolicy for the controller and for the webhook by
default. Both allow egress only to DNS and to the kube-apiserver, and the
webhook allows ingress on its admission port only from the kube-apiserver.
The controller may also reach receive-adapter pods (label
`sources.yipyap.run/source`) on TCP 8080, where it reads each adapter's
status for the source's `Connected` condition (see
[Source status](../../docs/status.md)).

The kube-apiserver runs on a host or external IP (a kubeadm static pod on
the host network, the k3s server process, a managed control plane), which
pod and namespace selectors never match on Calico, Cilium, kube-router and
other enforcing CNIs. The chart therefore matches it by IP range, and
`helm install` fails with an error until you set
`networkPolicy.apiServerCIDRs`:

```
networkPolicy.apiServerCIDRs is required when networkPolicy.enabled is true.
```

Find the addresses:

```bash
kubectl get endpoints kubernetes -n default
```

List each address as `/32` (IPv4) or `/128` (IPv6), or give the control
plane subnet. Policies are usually evaluated after the `kubernetes` Service
ClusterIP is translated to these endpoints; adding the ClusterIP as well
covers CNIs that do not. `hack/apiserver-cidrs-values.sh` in the source
repository prints a values file with both for the current kubectl context,
plus `apiServerPorts` set to the endpoint and Service ports (useful when the
API server listens on a port other than 443 or 6443, such as 16443 on
MicroK8s).

On managed control planes (EKS, AKS, GKE and similar) the endpoint
addresses can change during control plane upgrades or scaling. There, set
the control plane subnet instead of single addresses, or the controller and
webhook lose API server access when the addresses move.

```yaml
networkPolicy:
  apiServerCIDRs:
    - 172.18.0.2/32
  # apiServerPorts: [443, 6443]
```

If the API server calls webhooks from a different range than it serves on
(some managed control planes connect from a separate subnet or through a
proxy), set `networkPolicy.webhookIngressCIDRs` to that range. If admission
requests time out after install, this is the first thing to check. A set
`webhookIngressCIDRs` replaces `apiServerCIDRs` for webhook ingress rather
than adding to it, so list every range that must reach the webhook.

The webhook readiness and liveness probes also use the admission port, sent
by the kubelet from the node address. Most CNIs always allow traffic from a
node to its own pods. If yours does not and the webhook pod never becomes
Ready, add the node addresses to `webhookIngressCIDRs` together with the API
server range.

### Cilium

On Cilium you can match the API server by identity instead of IP:

```yaml
networkPolicy:
  cilium:
    useEntities: true
```

This also renders a `CiliumNetworkPolicy` (`cilium.io/v2`) for each pod
that allows egress to the `kube-apiserver` entity on `apiServerPorts` and
webhook ingress from it. Cilium adds these allows to the NetworkPolicy, so
`apiServerCIDRs` may be left empty in this mode. The Cilium CRDs must be
installed.

### Turning it off

Set `networkPolicy.enabled=false` when the cluster has no NetworkPolicy
enforcement or you manage policies yourself.

## Security model

### Controller does NOT read Secrets cluster-wide

The controller ClusterRole does not grant any Secret access. The
reconciler does not call `coreV1().Secrets().Get()`
anywhere. The per-source API key Secret is resolved Pod-side at adapter
startup via a `SecretKeyRef` envvar, scoped by kubelet to the
YipYapSource's own namespace.

This means a compromised controller SA cannot exfiltrate Secrets from
unrelated tenants. The blast radius of an SA token compromise is bounded
to writing `events`, CRUD on `YipYapSources` and on
adapter `Deployments`. ConfigMaps and leader-election Leases are granted
only in the release namespace.

### Tightening blast radius further: `controller.scopedNamespaces`

By default the controller can create, update, patch and delete
`apps/deployments` in every namespace, so it can reconcile receive-adapter
Deployments wherever a `YipYapSource` is created. To narrow the write
access, set `controller.scopedNamespaces` to the list of namespaces where
`YipYapSource` resources are allowed to live:

```yaml
controller:
  scopedNamespaces:
    - team-a
    - team-b
```

With the list set:

- Deployment create/update/patch/delete and Pod list are granted only
  through a `Role` + `RoleBinding` in each listed namespace.
- Deployment and `YipYapSource` get/list/watch stay on the ClusterRole.
  The controller's informers list and watch every namespace, and a
  namespaced `Role` cannot satisfy a cluster-wide list, so without these
  reads the controller never starts reconciling. The controller can still
  read every Deployment in the cluster.
- Event writes and `YipYapSource` status writes stay cluster-wide.
- The release namespace always gets configmaps read and leader-election
  leases through its own `Role`, whether or not it is listed. List it only
  if sources live there.

A `YipYapSource` outside the listed namespaces is still reconciled, but its
adapter Deployment create is denied and the source stays NotReady. Pair the
setting with an admission policy (Kyverno, OPA Gatekeeper or a
`ValidatingAdmissionPolicy`) that rejects `YipYapSource` creation outside
the listed namespaces if you want the failure at create time.

### Base URL policy

The adapter sends the API key from `spec.apiKeyRef` as a bearer token to
`spec.baseURL`, so the URL decides where a Secret value goes. Two checks
keep a user from sending a Secret to a host of their choosing:

- **Allowed hosts.** `spec.baseURL` must be `https`, must not contain user
  info, and its host must match `webhook.allowedBaseURLHosts` (default
  `console.yipyap.run`). Matching is case-insensitive. An entry without a
  port matches the default https port only; `host:8443` matches that port.
  `*.example.com` matches any subdomain of `example.com` but not the apex.
  The webhook rejects other URLs with a `spec.baseURL` field error. The
  controller applies the same list, so a source created before this check
  (or while the webhook was bypassed) gets `BaseURLAllowed=False`, reason
  `BaseURLNotAllowed`, and its adapter Deployment is deleted until the URL
  is fixed.
- **Secret access.** On create, and on any update that changes
  `spec.apiKeyRef` or `spec.baseURL`, the webhook runs a
  SubjectAccessReview for the requesting user (with groups, UID and extra
  fields) for `get` on `secrets/<name>` in the source namespace. If the
  user cannot read that Secret the request fails with `you are not allowed
  to read Secret <name> in <namespace>`. Users who create sources therefore
  need `get` on the API key Secret. Status updates and unrelated spec or
  metadata changes (including the controller's finalizer updates) are not
  reviewed.

The CRD now only accepts `https://` in `spec.baseURL`. On Kubernetes 1.30
and later, CRD validation ratcheting lets existing `http://` sources keep
taking status and metadata updates, so they show `BaseURLAllowed=False`.
On 1.27 to 1.29 the API server rejects every write to such a source until
`spec.baseURL` is changed to an allowed `https` URL, so the controller
cannot record the condition. It still deletes the adapter Deployment first,
so the API key is not sent either way. Find these sources before upgrading:

```sh
kubectl get yipyapsources -A -o jsonpath='{range .items[?(@.spec.baseURL)]}{.metadata.namespace}/{.metadata.name} {.spec.baseURL}{"\n"}{end}' | grep ' http://'
```

For a self-hosted YipYap server:

```yaml
webhook:
  allowedBaseURLHosts:
    - yipyap.example.internal
```

### Webhook cert rotation

The webhook self-signs a TLS CA at first startup via
`knative.dev/pkg/webhook/certificates`. The CA bundle is patched into the
admission webhook configurations on each pod startup; the in-Secret cert
itself rotates via the same controller loop on a 30-day cadence by default.

For operators who want managed rotation:

- **cert-manager**: annotate the `yipyap-source-webhook-certs` Secret and
  point a `cert-manager.io/Issuer` at it; disable the in-process rotation
  by overriding `webhook.failurePolicy` and supplying your own bundle.
- **Manual rollout**: `kubectl rollout restart deployment/yipyap-source-webhook
  -n yipyap-sources` will trigger a fresh in-process rotation; webhook HA
  (`replicas: 2` + PDB `minAvailable: 1`) keeps admission responsive
  during the rollover.

## Upgrade notes

- `networkPolicy.apiServerCIDRs` is now required while `networkPolicy.enabled`
  is true (the default). The previous policy allowed API server traffic only
  to pod addresses, which blocked the controller and webhook on enforcing
  CNIs. Upgrades fail with an error until the value is set; see
  [NetworkPolicy](#networkpolicy).
- Webhook ingress is now limited to the API server range instead of any
  source.

- The webhook self-signs a CA at startup; there is no cert-manager dependency
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
