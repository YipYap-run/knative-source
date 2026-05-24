# YipYap Knative Source

`YipYapSource` is a Knative event source that delivers YipYap CloudEvents to
any Knative sink. It provisions an adapter Deployment per `YipYapSource` CR
that subscribes to YipYap's event stream (via the `yipyap-knative-source`
container image) and forwards events to the configured sink.

Apache-2.0 licensed.

## Install

```bash
helm repo add yipyap-source https://yipyap-run.github.io/knative-source
helm repo update
helm install yipyap-source yipyap-source/yipyap-source \
  --namespace yipyap-sources \
  --create-namespace
```

See the full documentation at <https://docs.yipyap.run/integrations/knative/source-crd/>.

## Status

API version: `sources.yipyap.run/v1alpha1`.
