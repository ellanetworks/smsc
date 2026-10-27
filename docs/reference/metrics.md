---
description: Reference of the Prometheus metrics exposed by Ella SMSC.
---

# Metrics

Ella SMSC exposes Prometheus metrics at `GET /api/v1/metrics`.

| Metric | Type | Description |
| --- | --- | --- |
| `ellasmsc_build_info` | Gauge | Always 1. The version and revision of the running build are in its labels. |
| `ellasmsc_messages_received_total` | Counter | Short messages received, one per part, by origin (`mobile`, `api`) and result (`accepted`, `rejected`, `error`). |
| `ellasmsc_messages_completed_total` | Counter | Short messages and status reports that reached a final status, by type (`message`, `status_report`) and status (`delivered`, `failed`, `expired`, and `replaced` for a message). |
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
