# Sink TLS and OIDC

The controller resolves `spec.sink` to a full Knative address, not only a
URI. Besides `status.sinkUri` it records:

| Status field | Source |
| --- | --- |
| `status.sinkCACerts` | `spec.sink.CACerts`, or the CA bundle the sink's Addressable publishes (for example with Knative transport encryption). |
| `status.sinkAudience` | `spec.sink.audience`, or the OIDC audience the sink's Addressable publishes. |
| `status.auth.serviceAccountName` | The ServiceAccount the adapter authenticates as, when the sink has an audience. |

## TLS

The CA bundle reaches the adapter as `K_CA_CERTS`. The adapter verifies the
sink against the system roots plus that bundle, with TLS 1.2 or later.

A bundle that does not parse (no `CERTIFICATE` block, a bad certificate, a
non-certificate block or stray text) sets `SinkProvided=False` with reason
`InvalidCACerts`, and the controller does not change the adapter. If a bad
bundle reaches the adapter anyway, it exits at start with a `K_CA_CERTS`
config error instead of running with a CA it would ignore.

## OIDC

When the sink has an audience, the adapter sends
`Authorization: Bearer <token>` on every sink request. The token is a
Kubernetes ServiceAccount token for that audience, from a projected volume
mounted at `/var/run/secrets/yipyap/oidc/token` (env `K_AUDIENCE` and
`YIPYAP_SINK_TOKEN_PATH`). The kubelet rotates it before it expires (one hour
lifetime) and the adapter reads it on each send. The pod still has
`automountServiceAccountToken: false`, so the adapter has no Kubernetes API
token of its own.

The audience comes from `spec.sink.audience` or from the sink's Addressable,
so whoever writes the source picks it, and the token goes to the sink URL.
An audience the API server also accepts would make that token usable against
the API. For that reason the adapter always authenticates as a ServiceAccount
the controller creates for the source, never as `spec.serviceAccountName`:

- The account is created in the source's namespace and named after the
  Knative eventing convention `<source>-oidc-sources.yipyap.run-yipyapsource`
  (shortened with a hash for long names). It has no RBAC bindings, is owned
  by the source and is deleted with it.
- If a ServiceAccount of that name exists and the source does not own it, the
  controller refuses to use it and sets `Deployed=False` with reason
  `ServiceAccountFailed`.
- When `spec.serviceAccountName` is set, the pod takes that account's
  `imagePullSecrets` (copied onto the pod spec when the source reconciles,
  so a change to them lands at the next resync), and private registry pulls
  keep working. That account must exist; otherwise the source
  gets `Deployed=False` with reason `ServiceAccountFailed`.

Do not bind roles to the generated account. Any token it gets can reach the
sink's owner.

To authorize the source on a sink that enforces Knative EventPolicies, name
the source (the policy resolves `status.auth.serviceAccountName`) or the
ServiceAccount itself in the policy's `from` list.

The controller needs `get` and `create` on ServiceAccounts for this, in
every namespace it manages sources in (cluster-wide by default, or the
`controller.scopedNamespaces` Roles).
