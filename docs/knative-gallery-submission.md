# Knative sources gallery submission

This file captures the artifacts needed to submit `YipYapSource` to the
[Knative sources gallery](https://knative.dev/docs/eventing/sources/) once
Phase 4 ships. The actual submission is a PR against
[`knative/docs`](https://github.com/knative/docs).

**Do not submit before:**
- The `knative-source` repo is public.
- A stable tag (`v0.1.0`) is published.
- The controller + webhook images are available at `ghcr.io/yipyap-run/*`.
- The Helm chart is published to GitHub Pages.
- The conformance suite passes against a kind cluster on CI.

## Submission steps

1. Fork `knative/docs`.
2. Branch from `main`: `knative-docs/add-yipyapsource`.
3. Edit `docs/versioned/eventing/sources/README.md` — add one row to the
   **Third-Party Sources** table (see exact row text below).
4. Submit a pull request with the title and body below.

## Row to add

Locate the "Third-Party Sources" table in `docs/versioned/eventing/sources/README.md`
(directly after the Knative Sources table). Insert this row alphabetically
— for "YipYap" it goes at the bottom:

```markdown
[YipYap](https://github.com/YipYap-run/knative-source) | Alpha | [YipYap](https://yipyap.run) | Brings YipYap monitoring events (alert fired / acknowledged / resolved / escalated, monitor state transitions, maintenance windows) into Knative. The YipYapSource provisions a per-CR adapter Deployment that subscribes to YipYap's event stream and forwards CloudEvents to the configured sink. Supports both poll and stream transports, OIDC-authenticated delivery, per-channel reply-event handling.
```

## PR title

```
docs: add YipYapSource to third-party sources
```

## PR body

```markdown
## What's in this PR

Adds YipYap's Knative event source to the third-party sources table in
`docs/versioned/eventing/sources/README.md`.

**Project**: https://github.com/YipYap-run/knative-source
**License**: Apache-2.0
**Maintainer**: YipYap (https://yipyap.run)
**Status**: Alpha

## What YipYap is

YipYap is a monitoring and alerting platform. The YipYap event source
brings YipYap events — alerts fired/acked/resolved/escalated, monitor
up/down/degraded/heartbeat_missed transitions, maintenance window
start/end — into Knative Eventing so cluster operators can build
automation, incident response, and observability pipelines on top.

## Conformance

- Source duck-type compliance verified via `pkg/apis/sources/v1alpha1`:
  `duckv1.SourceSpec` / `duckv1.SourceStatus` embedded.
- Standard conditions: `SinkProvided`, `Deployed`, `Ready`.
- `sinkUri`, `observedGeneration`, `ceAttributes` populated.
- Knative source conformance suite runs on kind CI per `test/conformance/`.
- Webhook validation + defaulting admission controllers installed via
  Helm chart.

## Installation

```bash
helm repo add yipyap-source https://yipyap-run.github.io/knative-source
helm install yipyap-source yipyap-source/yipyap-source \
  --namespace yipyap-sources --create-namespace
```

Minimum YipYapSource example:

```yaml
apiVersion: sources.yipyap.run/v1alpha1
kind: YipYapSource
metadata:
  name: prod-alerts
spec:
  apiKeyRef:
    name: yipyap-credentials
  filter:
    types: ["run.yipyap.alert.*"]
  sink:
    ref:
      apiVersion: eventing.knative.dev/v1
      kind: Broker
      name: default
```

## Checklist

- [x] Alphabetical placement in Third-Party Sources table.
- [x] Apache-2.0 licensed.
- [x] Stable release published (v0.1.0).
- [x] Conformance suite exists.
- [x] Public documentation at https://docs.yipyap.run/integrations/knative/source-crd/.
- [x] Source duck-type compliance.
```

## Reviewers to request

Based on recent PRs to `knative/docs/versioned/eventing/sources/`:
- From the knative-extensions org CODEOWNERS
- The eventing WG-lead for the current cycle (check
  https://github.com/knative/community/blob/main/working-groups/WORKING-GROUPS.md)

## Response strategy

The common feedback points maintainers raise:

- **License/maintainer clarity** — we're Apache-2.0 on this repo (the
  rest of YipYap is AGPLv3; the `knative-source` repo is deliberately
  Apache-2.0 to match Knative ecosystem norm).
- **Link to docs** — link the SaaS docs page and the in-repo README
  together; reviewers have both.
- **Conformance evidence** — point at the workflow run showing
  `test/conformance/` passed against the PR's submission ref.

If maintainers ask for changes, prefer landing them as follow-up PRs
against our own repo rather than blocking the gallery PR — the gallery
PR is just a docs change, it shouldn't need to wait on us.
