# Source status

`kubectl get yipyapsources` shows each source's `Ready` condition and its
reason. `Ready` is `True` only when all of these are `True`:

| Condition | Meaning |
| --- | --- |
| `BaseURLAllowed` | `spec.baseURL` passes the operator policy (https, allowlisted host). |
| `SinkProvided` | The sink resolved to a URI, and its CA bundle, if any, parses (reason `InvalidCACerts` otherwise). See [Sink TLS and OIDC](./sink-tls-oidc.md). |
| `Deployed` | The adapter Deployment is `Available` (reason `ServiceAccountFailed` when the OIDC ServiceAccount cannot be made, or `spec.serviceAccountName` is missing for an OIDC sink). |
| `Connected` | The adapter can use the YipYap API with its API key and deliver to the sink. |

When a condition is not `True`, `Ready` carries its reason and message, so
the most common setup mistakes show up directly:

```
NAME   READY   REASON           SINK                                   AGE
web    False   APIKeyRejected   http://broker-ingress.knative-eventing...   5m
```

An API key failure wins over the pod state: once the adapter has reported
`APIKeyRejected`, that reason stays on `Ready` until the adapter itself
reports a different verdict. A later `Deployed=False`, and every fallback
the controller takes from the pod instead of the adapter's status endpoint
(pod not found, not running, not ready, a ready pod whose status endpoint
cannot be read, or a failed pod list), leave the rejected-key reason in
place.

`kubectl get yipyapsources -o wide` adds a `MESSAGE` column, or read the
message directly:

```
kubectl get yipyapsource web -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'
API key rejected (401)
```

## Connected reasons

| Reason | Message | What to check |
| --- | --- | --- |
| `APIKeyRejected` | `API key rejected (401)` | The Secret named by `spec.apiKeyRef` holds a wrong, revoked or expired key, or the key belongs to another YipYap instance than `spec.baseURL`. |
| `APIForbidden` | `API key forbidden (403)` | The key is valid but lacks access to events, or the org's plan does not include the CloudEvents transport. Use a key with event read access on a plan that includes it. |
| `APIUnreachable` | `cannot reach the YipYap API` or `YipYap API returned an error (NNN)` | No successful API round trip for 5 minutes (or three poll intervals, if longer). Check egress from the source namespace and `spec.baseURL`. |
| `SinkUnavailable` | `sink is not accepting events (NNN)` or `sink is unreachable` | One event has failed at the sink for 2 minutes. The adapter keeps retrying it; nothing is skipped. |
| `AdapterStarting` | `waiting for the adapter pod to start` | Shown while the adapter pod is created or scheduled. |
| `AdapterNotReady` | generic | The adapter is not ready and the controller could not read its status endpoint. See below. |
| `AdapterStatusUnknown` | `could not look up the adapter pod` | The controller could not list pods. Check its RBAC. |

The adapter needs 3 consecutive 401 (or 403) answers before it reports a key
problem, so one stray answer during a key rotation does not flip the
source. In poll mode with an interval longer than 10 seconds, the adapter
retries after a 401 or 403 every 10 seconds until it reaches that count,
then returns to its normal interval. Recovery is automatic: after the next successful API call the
adapter turns ready again and the source returns to `Ready=True` within
about a minute. A changed Secret is read only when the adapter pod starts,
so after fixing the key run
`kubectl rollout restart deployment <status.deploymentName>`.

## How it works

The adapter pod has no Kubernetes credentials, so it cannot write the
source's status itself. Instead:

1. The adapter tracks its API round trips and sink deliveries. Its
   readiness probe (`/readyz` on port 8080) answers 503 while one of the
   problems above lasts. Its liveness probe (`/healthz`) never looks at
   these errors, so a bad key never causes a restart loop.
2. The adapter serves the same verdict as JSON on `/status` (port 8080):

   ```json
   {"ready":false,"reason":"APIKeyRejected","message":"API key rejected (401)","lastSuccess":"2026-11-24T10:00:00Z"}
   ```

3. The controller lists the source's adapter pod (it needs `list` on pods)
   and reads `/status` over the pod network. It copies the reason and
   message onto `Connected`, and re-checks every 30 seconds while
   `Connected` is not `True`. Only reasons from the table above are passed
   through, and messages are cut to 256 characters.
4. If `/status` cannot be reached, the controller falls back to the pod's
   readiness: a ready pod counts as connected, an unready one gives
   `AdapterNotReady` with a generic message. The exception is a source whose
   last verdict was `APIKeyRejected`: that reason is kept until the adapter
   reports again, so a failed probe or a restart does not hide the key
   problem.

The chart's controller NetworkPolicy allows egress to TCP 8080 on pods
labeled `sources.yipyap.run/source`. If a source namespace has a
default-deny ingress policy, allow ingress to its adapter pods on port 8080
from the controller's namespace to get the specific reason instead of
`AdapterNotReady`:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-yipyap-source-status
spec:
  podSelector:
    matchExpressions:
      - key: sources.yipyap.run/source
        operator: Exists
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: yipyap-sources
          podSelector:
            matchLabels:
              app: yipyap-source-controller
      ports:
        - protocol: TCP
          port: 8080
```
