---
description: RESTful API reference for the Prometheus metrics of Ella SMSC.
---

# Metrics

## Get Metrics

This path returns the metrics of the SMSC in the [Prometheus exposition format](https://prometheus.io/docs/instrumenting/exposition_formats/#text-format-details), negotiated from the request's `Accept` header. See [Metrics](../metrics.md) for the list.

| Method | Path              |
| ------ | ----------------- |
| GET    | `/api/v1/metrics` |

### Sample Response

```
# HELP ellasmsc_hss_peers Open Diameter peers in the operator's realm that serve S6c, which the SMSC asks to route messages. At 0, messages to phones wait.
# TYPE ellasmsc_hss_peers gauge
ellasmsc_hss_peers 0
# HELP ellasmsc_messages_pending Short messages and status reports waiting for delivery, by type (message, status_report) and state: due (to be delivered now, or being delivered) or waiting (for a retry or for the phone to be reachable). Due messages that stay due mean the deliverer is falling behind.
# TYPE ellasmsc_messages_pending gauge
ellasmsc_messages_pending{state="due",type="message"} 0
ellasmsc_messages_pending{state="due",type="status_report"} 0
ellasmsc_messages_pending{state="waiting",type="message"} 2
ellasmsc_messages_pending{state="waiting",type="status_report"} 0
# HELP ellasmsc_messages_received_total Short messages received, one per TPDU, by origin (mobile, api) and result (accepted, rejected, error).
# TYPE ellasmsc_messages_received_total counter
ellasmsc_messages_received_total{origin="api",result="accepted"} 2
ellasmsc_messages_received_total{origin="api",result="error"} 0
ellasmsc_messages_received_total{origin="mobile",result="accepted"} 0
ellasmsc_messages_received_total{origin="mobile",result="error"} 0
ellasmsc_messages_received_total{origin="mobile",result="rejected"} 0
```
