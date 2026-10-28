---
description: Reference of the metrics and logs of Ella SMSC.
---

# Observability

## Metrics

Ella SMSC exposes Prometheus metrics at [`GET /api/v1/metrics`](api/metrics.md).

| Metric | Type | Description |
| --- | --- | --- |
| `ellasmsc_build_info` | Gauge | Always 1. The version and revision of the running build are in its labels. |
| `ellasmsc_messages_received_total` | Counter | Short messages received, one per part, by origin (`mobile`, `api`) and result (`accepted`, `rejected`, `error`). |
| `ellasmsc_messages_completed_total` | Counter | Short messages and status reports that reached a final status, by type (`message`, `status_report`) and status (`delivered`, `failed`, `expired`, and `replaced` for a message). |
| `ellasmsc_messages_purged_total` | Counter | Short messages deleted once their [retention](api/retention.md) passed, with their status reports and delivery attempts. |
| `ellasmsc_messages_pending` | Gauge | Short messages and status reports waiting for delivery, by type and state: `due`, to be delivered now or being delivered; `waiting`, for a retry or for the phone to be reachable. |
| `ellasmsc_message_delivery_duration_seconds` | Histogram | Time from submission to delivery of the short messages delivered, the time the phone was unreachable included. |
| `ellasmsc_hss_peers` | Gauge | Open peers in the operator's realm that support S6c. At 0, messages to phones wait. |
| `ellasmsc_peer_requests_total` | Counter | Requests to the HSS and to the MMEs, SMSFs and IP-SM-GWs, by interface (`s6c`, `sgd`) and result (`success`, `failure`, `absent_user`, `error`, `timeout`). |
| `ellasmsc_peer_request_duration_seconds` | Histogram | Duration of requests to the HSS and to the MMEs, SMSFs and IP-SM-GWs, by interface. |
| `ellasmsc_database_query_duration_seconds` | Histogram | Duration of database calls, the wait for the connection included. |
| `ellasmsc_database_query_errors_total` | Counter | Failed database calls. |
| `ellasmsc_database_storage_bytes` | Gauge | Size of the database on disk, by file: `main`, `wal`. |
| `go_*` | Gauge, Counter, Summary | Go runtime: goroutines, heap, garbage-collection pauses. |
| `process_*` | Gauge, Counter | Process: memory, CPU, open file descriptors, start time. |

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

| Action | Logged when |
| --- | --- |
| `initialize` | The first user is created. |
| `auth_login` | A user logs in, or fails to. |
| `auth_logout` | A user logs out. |
| `user_authentication` | A request with an invalid or expired token is refused. |
| `create_user` | A user is created. |
| `update_user` | A user's role is changed. |
| `update_user_password` | A user's password is changed. |
| `delete_user` | A user is deleted. |
| `create_api_token` | An API token is created. |
| `delete_api_token` | An API token is deleted. |
| `read_message_content` | The content of a message is read. |
