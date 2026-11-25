# Delivery contract

A `YipYapSource` delivers events to its sink **at least once**. An event can
reach the sink more than once (after a sink outage, an adapter restart or a
lost confirmation), so sinks should dedupe on the CloudEvent `id`
(`ce-id`). Events are never dropped silently: an event is only skipped when
the sink rejects it permanently, and every skip is logged with its `ce_id`
and counted.

This page is the contract between the receive adapter (this repo) and the
YipYap server. The adapter side is implemented in
`cmd/receive-adapter/source`. The guarantee holds end to end only when the
server implements its half, described under [Server
requirements](#server-requirements).

## Source identity

Every adapter request to the server (poll, stream, ack and the
`/api/v1/knative/source-config` mode hint) carries:

```
Authorization: Bearer <api key>
X-YipYap-Source-ID: <YipYapSource metadata.uid>
```

The controller passes the UID to the adapter as `YIPYAP_SOURCE_ID`. The
server keys its consumer, and the confirmed position, on this value. Two
sources in the same org with the same filters are separate consumers and
each receive every event. If the header is absent (an adapter run by hand),
the server may fall back to its older per org and filter key, except on the
ack endpoint, which requires the header.

Some deployments gate poll, stream, ack and the source-config hint on the
org's plan. A refusal is reported as `APIForbidden` on the source.

## Sink delivery

For each event, in order, the adapter POSTs to the sink in CloudEvents
binary mode and retries in place until the event is accepted or rejected.
It does not fetch or confirm later events while an earlier one is pending.

| Sink response | Adapter action |
| --- | --- |
| 2xx | Accepted. Confirm to the server. |
| 400, 404, 410, 413, 415, 422 | Rejected permanently. Log with `ce_id`, count in `yipyap_source_events_rejected_total`, confirm so the server stops redelivering it, continue. |
| 429, 503 with `Retry-After` | Wait at least the `Retry-After` value (capped at 5 minutes), then retry. |
| Any other status, network error or timeout | Retry. |

Attribute values are sent as `ce-*` headers unchanged, except that control
characters (0x00 to 0x1F and 0x7F, for example a newline in a subject) are
percent-encoded as the CloudEvents HTTP binding requires. Without that the
HTTP client refuses the request and the event could never be delivered.

Retries use exponential backoff from 250ms, doubling up to 30s, with each
wait jittered to between half and all of the current step. Retry stops as
soon as delivery is cancelled (see [Shutdown](#shutdown)). Other 4xx codes
such as 401, 403 and 409 are retried rather than skipped, because they
usually point at a sink or mesh misconfiguration, and skipping would lose
every event until an operator noticed.

The adapter exposes counters on `:8080/metrics` (Prometheus text format):
`yipyap_source_events_delivered_total`,
`yipyap_source_delivery_retries_total`,
`yipyap_source_events_rejected_total`, `yipyap_source_ack_errors_total` and
`yipyap_source_events_filtered_total`.

## Poll mode

```
GET {baseURL}/api/v1/cloudevents/poll?filter=...&severity=...&monitor_id=...&since=<cursor>
```

Response: `{"events": [...], "next_cursor": "<opaque>"}`.

- `since` is the last `next_cursor` whose whole batch the sink accepted or
  permanently rejected. Sending it confirms that batch and everything
  before it.
- The adapter advances its cursor, and writes it to
  `/var/run/yipyap/cursor`, only after every event in the batch is accepted
  or rejected. The cursor is also kept in memory for the life of the
  process, so a failed write does not rewind it at the next mode refresh. If delivery of a batch is interrupted, the next poll sends the
  old cursor and the server must return that batch again.
- On shutdown, if the adapter holds a cursor it has not yet sent as `since`,
  it sends it to the ack endpoint (`{"cursor": "<cursor>"}`) so the server
  does not wait for a poll that will not come.

## Stream mode

```
GET {baseURL}/api/v1/cloudevents/stream?filter=...&severity=...&monitor_id=...&since=<cursor>
```

The response is NDJSON, one CloudEvent per line. Each event should carry its
position as the extension attribute `yipyapcursor` (an opaque string). The
adapter removes that attribute before forwarding the event to the sink.

After the sink accepts or permanently rejects an event, the adapter confirms
it:

```
POST {baseURL}/api/v1/cloudevents/ack
Content-Type: application/json
X-YipYap-Source-ID: <uid>

{"cursor": "<yipyapcursor of the event>"}
```

- `X-YipYap-Source-ID` is required (the server answers 400 without it).
  An adapter started by hand without `YIPYAP_SOURCE_ID` cannot confirm
  events; it still resumes from `since`.
- The body carries only `cursor`. The server rejects unknown fields with
  400, so an extra field would make every confirmation fail. An event
  without a `yipyapcursor` is not confirmed, and the server redelivers it.
- The server answers 204 on success. The adapter retries a 5xx, 408, 429 or
  network failure up to 3 times (5s per attempt, 10s in total), then logs
  and moves on. An unconfirmed
  event is redelivered, never lost.
- Any other 4xx (for example 401 or 403) is not retried. The adapter logs
  it and counts it in `yipyap_source_ack_errors_total`.
- A 404, 405 or 501 means the server has no ack endpoint yet. The adapter
  logs one warning, stops sending confirmations for 5 minutes, then tries
  again, so it does not send a failing request per event and starts
  confirming soon after the server adds the endpoint. Delivery is meanwhile
  only as strong as the server's own acking.
- On reconnect the adapter sends `since=<cursor of the last confirmed
  event>`, and the server resumes after it. The adapter writes that cursor
  to `/var/run/yipyap/cursor.stream` (separate from the poll cursor), so a
  restarted container resumes from it too. A new pod starts without a
  cursor (the volume is an `emptyDir`), and the server resumes from the
  source's confirmed position, keyed by `X-YipYap-Source-ID`.

The poll-mode shutdown confirmation uses the same endpoint with only
`cursor` set.

## Filters

`spec.filter` narrows what a source delivers. The controller passes it to
the adapter as environment variables, each a comma-separated list:

| Spec field | Env var | Query parameter |
| --- | --- | --- |
| `filter.types` | `YIPYAP_EVENT_FILTER` | `filter` |
| `filter.severities` | `YIPYAP_FILTER_SEVERITIES` | `severity` |
| `filter.monitorIDs` | `YIPYAP_FILTER_MONITOR_IDS` | `monitor_id` |

The adapter sends one query parameter per value on every poll and stream
request (for example `severity=critical&severity=warning`). Values of one
parameter are OR-matched; different parameters are AND-matched.

Because the filters travel in the query string of every request, admission
rejects a filter whose `filter`, `severity` and `monitor_id` parameters
encode to more than 6144 bytes (about 100 monitor IDs in UUID form, after
any types). Common proxies reject request lines over 8 KiB, so a longer
filter would make every request fail. Split a larger set across several
sources.

The server also caps `filter` at 50 values and `monitor_id` at 100 per
request, and answers 400 past either cap. Admission counts only the encoded
length, not the values, so a filter with many short globs can pass
admission and then be rejected by every request. A `severity` outside the
three values, or a `monitor_id` that is not in the key's org, is also 400.
The adapter treats a 400 as a failed poll or stream and retries on its
schedule, so a filter the server rejects stops delivery until the source's
filter is fixed.

The adapter also applies `severities` and `monitorIDs` itself before
sending an event to the sink, so they hold against a server that ignores the
parameters:

- **Severity** applies only to alert events (type `run.yipyap.alert.*`).
  The adapter reads the extension attribute `yipyapseverity`, and when it is
  absent the `severity` field of the JSON data. Values are compared case
  insensitively against `info`, `warning` and `critical`. An alert event
  whose severity is not listed is dropped. An alert event with neither
  attribute (today `acknowledged`, `resolved` and `escalated`) is delivered.
  Monitor and maintenance events are never dropped by severity.
- **Monitor ID** applies to every event type. The adapter reads the
  extension attribute `yipyapmonitorid`, and when it is absent the
  `monitor_id` field of the JSON data. An event whose monitor is not listed
  is dropped. An event with neither (for example a maintenance event) is
  delivered by the adapter's local filter. The server's filter is stricter:
  when `monitor_id` is set it drops events that carry no monitor
  (`maintenance.started` is matched on its `monitors` list).

A dropped event counts as handled: it is confirmed like a delivered one (in
the next `since` for poll, through the ack endpoint for stream), so the
server does not send it again. Dropped events are counted in
`yipyap_source_events_filtered_total`.

## Shutdown

The adapter Deployment uses the `Recreate` strategy, so an old and a new
adapter for one source never run at the same time. On SIGTERM the adapter:

1. Stops fetching (no new poll, stream connection closed after the current
   event).
2. Keeps delivering the in-flight event, and in poll mode the rest of the
   current batch, for up to `YIPYAP_SHUTDOWN_GRACE` (default and maximum
   25s; a larger value is a config error).
3. Confirms what the sink accepted (at most 10s for the confirmation), then
   exits.

The pod's `terminationGracePeriodSeconds` is 40, which covers both steps
with 5s to spare. Events already read from the stream but not yet started
are not delivered after SIGTERM; the server redelivers them.

The adapter also re-checks its mode every few minutes. That refresh ends the
current poll loop or stream connection, but not delivery: an event the sink
is still retrying keeps retrying until it is accepted or rejected, and only
then does the adapter fetch again. The `YIPYAP_SHUTDOWN_GRACE` limit applies
only to SIGTERM.
Anything not confirmed by then is redelivered to the next adapter.

## Server requirements

The YipYap server must provide the following for the guarantee to hold end
to end. Against a server that does not implement them, the adapter still
retries every event in place, so sink outages do not drop events that
reached the adapter, but events the server has already handed out can be
lost if the adapter restarts.

1. Honor `since` on `/api/v1/cloudevents/poll` and `/stream`: redeliver
   every event after the given cursor until a later `since` or ack confirms
   it.
2. Provide `POST /api/v1/cloudevents/ack` as specified above, and ack bus
   messages only after this confirmation (not when an event is copied into
   a buffer).
3. Emit `yipyapcursor` on each streamed event.
4. Key durable consumers on `X-YipYap-Source-ID` instead of the org and
   filter set.
5. Honor the `severity` and `monitor_id` query parameters on `/poll` and
   `/stream`, so filtered events are not sent at all. Unknown parameter
   names must keep being ignored, not rejected. The server enforces the
   values as described under [Filters](#filters): an unknown `severity` or
   a `monitor_id` outside the org is 400, and at most 50 `filter` values and
   100 `monitor_id` values are accepted per request.
6. Set `yipyapseverity` on every alert event and `yipyapmonitorid` on every
   event tied to one monitor. The server does not send either extension
   today. The adapter then reads the `severity` and `monitor_id` fields of
   the JSON data instead, and delivers an alert event whose severity is
   unknown (`acknowledged`, `resolved` and `escalated` have no `severity`
   field), so a severity filter on those types relies on the server-side
   filter.
