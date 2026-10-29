---
description: Reference for observability features in Ella SMSC.
---

# Observability

Ella SMSC supports four observability pillars: Metrics, Logs, Traces, and Profiles.

## Metrics

Ella SMSC exposes Prometheus metrics at [`GET /api/v1/metrics`](api/metrics.md).

| Metric | Type | Description |
| --- | --- | --- |
| `ellasmsc_build_info` | Gauge | Always 1. The version and revision of the running build are in its labels. |
| `ellasmsc_messages_received_total` | Counter | Short messages received, one per part, by origin (`mobile`, `api`, `smpp`) and result (`accepted`, `rejected`, `error`). One per `submit_sm` for `smpp`. |
| `ellasmsc_messages_completed_total` | Counter | Short messages and status reports that reached a final status, by type (`message`, `status_report`) and status (`delivered`, `failed`, `expired`, and `replaced` for a message). |
| `ellasmsc_messages_purged_total` | Counter | Short messages deleted once their [retention](api/retention.md) passed, with their status reports and delivery attempts. |
| `ellasmsc_messages_pending` | Gauge | Short messages and status reports waiting for delivery, by type and state: `due`, to be delivered now or being delivered; `waiting`, for a retry or for the phone to be reachable. |
| `ellasmsc_message_delivery_duration_seconds` | Histogram | Time from submission to delivery of the short messages delivered, the time the phone was unreachable included. |
| `ellasmsc_hss_peers` | Gauge | Open peers that serve S6c in the operator's realm: HSSs, or agents in front of them. At 0, messages to phones wait. |
| `ellasmsc_peer_requests_total` | Counter | Requests to the HSS and to the MMEs, SMSFs and IP-SM-GWs, by interface (`s6c`, `sgd`) and result (`success`, `failure`, `absent_user`, `error`, `timeout`). |
| `ellasmsc_peer_request_duration_seconds` | Histogram | Duration of requests to the HSS and to the MMEs, SMSFs and IP-SM-GWs, by interface. |
| `ellasmsc_smpp_sessions` | Gauge | Bound SMPP sessions, by bind type (`transmitter`, `receiver`, `transceiver`). |
| `ellasmsc_smpp_binds_total` | Counter | SMPP bind attempts, by result (`accepted`, `rejected`). |
| `ellasmsc_smpp_notifications_pending` | Gauge | Delivery receipts and SIM answers not yet sent to an ESME and not expired, by type (`receipt`, `sim_answer`). A rise means no receiver of the ESME is bound, or it does not acknowledge them. |
| `ellasmsc_smpp_notifications_sent_total` | Counter | Delivery receipts and SIM answers that reached a final outcome, by type and result (`acknowledged`, `rejected`, `expired`, `no_user_data`). |
| `ellasmsc_smpp_submit_duration_seconds` | Histogram | Time from a `submit_sm` read to its `submit_sm_resp` written. |
| `ellasmsc_database_query_duration_seconds` | Histogram | Duration of database calls, the wait for the connection included. |
| `ellasmsc_database_query_errors_total` | Counter | Failed database calls. |
| `ellasmsc_database_storage_bytes` | Gauge | Size of the database on disk, by file: `main`, `wal`. |
| `go_*` | Gauge, Counter, Summary | Go runtime: goroutines, heap, garbage-collection pauses. |
| `process_*` | Gauge, Counter | Process: memory, CPU, open file descriptors, start time. |

## Traces

Ella SMSC exports [OpenTelemetry](https://opentelemetry.io/) traces when `telemetry.enabled` is set in the [configuration file](config_file.md).

| Property | Value |
| --- | --- |
| Export | OTLP over gRPC, without TLS |
| Sampling | Parent-based, always on for a trace the SMSC starts |
| Propagation | W3C Trace Context |
| Left out | MSISDN, IMSI, message content |

### Spans

| Span | Kind | Description | `error.type` |
| --- | --- | --- | --- |
| `{method} {route}` | Server | A request to the API, except `/api/v1/livez`, `/api/v1/readyz`, `/api/v1/metrics` and `/api/v1/pprof/`. Links to the caller's `traceparent`. | None. A 5xx response has the Error status. |
| `submit_sm` | Server | A `submit_sm` from an ESME. | `ESME_RSYSERR`, `_OTHER` |
| `deliver` | Consumer | A delivery attempt of a short message or status report. Root span, linked to the span that created the message. | `failed`, `expired`, `_OTHER` |
| `deliver_sm` | Client | A delivery receipt or SIM answer to an ESME. Root span, linked to the span that created the message. | A `command_status` that SMPP 3.4 names, `session_closed`, `timeout`, `canceled`, `_OTHER` |
| `S6c/{command}`, `SGd/{command}` | Server, Client | A Diameter request. Child of the peer's trace context. | Client: a Result-Code from 3000, `closed`, `not_connected`, `unknown_peer`, `application_unsupported`, `unable_to_deliver`, `loop_detected`, `not_proxiable`, `timeout`, `canceled`, `_OTHER`. Server: `3001`, `3002`, `3004`, `5012`, `overloaded`, `shutting_down`. |

### Attributes

| Attribute | Span | Values |
| --- | --- | --- |
| `ella.sms.delivery.outcome` | `deliver` | `delivered`, `failed`, `expired`, `replaced`, `retry`, `interrupted` |

## Profiles

Ella SMSC exposes the [net/http/pprof](https://pkg.go.dev/net/http/pprof) API at [`/api/v1/pprof/`](api/pprof.md).

## Logs

Ella SMSC writes its logs to standard output, one JSON object per line, at the level of `logging.level` in the [configuration file](config_file.md).

| Field | Description |
| --- | --- |
| `ts` | Time of the event. |
| `level` | `debug`, `info`, `warn` or `error`. |
| `msg` | What happened. |

Other fields depend on the event.

### Audit events

Audit events are logs at level `info` with `msg` set to `audit event`.

| Field | Description |
| --- | --- |
| `action` | What was done, below. |
| `actor` | Email of the user who did it. Empty when unknown. |
| `ip` | Address the request came from. |
| `details` | What was done, in words. |
