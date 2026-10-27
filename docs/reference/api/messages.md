---
description: RESTful API reference for submitting and viewing short messages in Ella SMSC.
---

# Messages

A message is a short message the SMSC received, from a phone or from the API, with its delivery status and the status reports the SMSC returned to the phone that sent it. A text longer than one short message is split into parts, one message each.

## Create a Message

This path submits a text message to a phone. It returns one message per part.

| Method | Path               |
| ------ | ------------------ |
| POST   | `/api/v1/messages` |

### Request Body

| Field  | Type   | Required | Description |
| ------ | ------ | -------- | ----------- |
| `from` | string | Yes      | E.164 number with `+`, shown to the recipient as the sender. |
| `to`   | string | Yes      | E.164 number with `+` of the recipient. |
| `text` | string | Yes      | Not empty. GSM-7 if every character is in the GSM 7-bit default alphabet or its extension table, else UCS-2. Up to 255 parts. |

A message from the API never asks for status reports, and is kept for the default validity of the [delivery settings](delivery.md).

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `400` | A field is missing or invalid, or the text needs more than 255 parts. |

### Sample Request

```json
{
    "from": "+15550001000",
    "to": "+15551230002",
    "text": "Your code is 1234"
}
```

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "id": 1,
                "from": "+15550001000",
                "to": "+15551230002",
                "text": "Your code is 1234",
                "encoding": "gsm7",
                "status": "pending",
                "created_at": "2026-10-27T18:44:19.436Z",
                "updated_at": "2026-10-27T18:44:19.436Z",
                "expires_at": "2026-11-03T18:44:19.436Z",
                "next_attempt_at": "2026-10-27T18:44:19.436Z",
                "status_report": {
                    "requested": false,
                    "reports": []
                }
            }
        ]
    }
}
```

## List Messages

This path returns the list of messages, most recent first.

| Method | Path               |
| ------ | ------------------ |
| GET    | `/api/v1/messages` |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description |
| ---------- | ----- | ---- | ------- | ------- | ----------- |
| `to`       | query | str  |         | E.164 with `+` | Recipient. |
| `from`     | query | str  |         | E.164 with `+` | Sender. |
| `status`   | query | str  |         | `pending`, `delivered`, `failed`, `expired`, `replaced` | Status. |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index. |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Response Fields

| Field                           | Type    | Description |
| ------------------------------- | ------- | ----------- |
| `id`                            | int     | Message identifier. |
| `from`                          | string  | Sender, an E.164 number with `+`: the MSISDN of the phone, or the `from` of the API request. |
| `to`                            | string  | Recipient, an E.164 number with `+`. |
| `text`                          | string  | Text of the part. Absent for a binary message. |
| `encoding`                      | string  | `gsm7`, `ucs2` or `binary`. |
| `concatenation`                 | object  | Present for a part of a longer message. |
| `concatenation.reference`       | int     | Reference shared by the parts of the message. |
| `concatenation.part`            | int     | Number of this part, from 1. |
| `concatenation.total`           | int     | Number of parts. |
| `status`                        | string  | `pending`; `delivered`; `failed`; `expired`, when its validity period ended before its delivery; or `replaced`, by a later message from the same phone, of the same replace type, before its delivery. |
| `created_at`                    | string  | Time the SMSC received the message. |
| `updated_at`                    | string  | Time of the last change. |
| `expires_at`                    | string  | End of the validity period. |
| `next_attempt_at`               | string  | Time of the next delivery attempt. Present while `pending`. |
| `status_report`                 | object  | Status reports on the message. |
| `status_report.requested`       | boolean | Whether the phone that sent the message asked for status reports. |
| `status_report.reports`         | array   | Status reports the SMSC returned to that phone, oldest first. |
| `status_report.reports[].id`             | int    | Status report identifier. |
| `status_report.reports[].tp_status`      | int    | TP-Status the report gives (TS 23.040 §9.2.3.15). |
| `status_report.reports[].tp_status_name` | string | Meaning of `tp_status`, e.g. `received`, `replaced`, `no_response`, `validity_period_expired`. |
| `status_report.reports[].status`         | string | Delivery of the report to the phone: `pending`, `delivered`, `failed` or `expired`. |
| `status_report.reports[].created_at`     | string | Time the SMSC made the report. |
| `status_report.reports[].updated_at`     | string | Time of the last change. |
| `status_report.reports[].expires_at`     | string | End of the report's validity period. |
| `status_report.reports[].next_attempt_at` | string | Time of the next delivery attempt of the report. Present while `pending`. |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "id": 4,
                "from": "+15550001000",
                "to": "+15551230001",
                "text": "an carry, so the SMSC splits it into parts that the phone joins back together. ",
                "encoding": "gsm7",
                "concatenation": {
                    "reference": 74,
                    "part": 2,
                    "total": 2
                },
                "status": "pending",
                "created_at": "2026-10-27T18:53:37.120Z",
                "updated_at": "2026-10-27T18:54:07.151Z",
                "expires_at": "2026-11-03T18:53:37.120Z",
                "next_attempt_at": "2026-10-27T18:55:07.151Z",
                "status_report": {
                    "requested": false,
                    "reports": []
                }
            }
        ],
        "page": 1,
        "per_page": 1,
        "total_count": 3
    }
}
```

## Get a Message

This path returns a specific message.

| Method | Path                    |
| ------ | ----------------------- |
| GET    | `/api/v1/messages/{id}` |

### Path Parameters

| Name | Type | Description |
| ---- | ---- | ----------- |
| `id` | int  | Message identifier. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No message has this identifier. |

### Sample Response

```json
{
    "result": {
        "id": 1,
        "from": "+15551230001",
        "to": "+15551230002",
        "text": "hello",
        "encoding": "gsm7",
        "status": "delivered",
        "created_at": "2026-10-27T18:46:07.350Z",
        "updated_at": "2026-10-27T18:46:10.355Z",
        "expires_at": "2026-11-03T18:46:07.350Z",
        "status_report": {
            "requested": true,
            "reports": [
                {
                    "id": 2,
                    "tp_status": 0,
                    "tp_status_name": "received",
                    "status": "delivered",
                    "created_at": "2026-10-27T18:46:10.355Z",
                    "updated_at": "2026-10-27T18:46:10.356Z",
                    "expires_at": "2026-11-03T18:46:10.355Z"
                }
            ]
        }
    }
}
```

## List Delivery Attempts

This path returns the routing and delivery attempts of a message, oldest first.

| Method | Path                             |
| ------ | -------------------------------- |
| GET    | `/api/v1/messages/{id}/attempts` |

### Path Parameters

| Name | Type | Description |
| ---- | ---- | ----------- |
| `id` | int  | Message identifier. |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description |
| ---------- | ----- | ---- | ------- | ------- | ----------- |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index. |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Response Fields

| Field                     | Type   | Description |
| ------------------------- | ------ | ----------- |
| `id`                      | int    | Attempt identifier. |
| `started_at`              | string | Start of the attempt. |
| `completed_at`            | string | End of the attempt. |
| `step`                    | string | `routing`: a routing lookup at the HSS, over S6c. `delivery`: a delivery to a serving node, over SGd. |
| `node`                    | string | Host of the HSS that answered the routing lookup, or of the serving node. |
| `node_type`               | string | Type of the serving node: `mme`, `sgsn`, `ip_sm_gw`, `smsf_3gpp` or `smsf_non_3gpp`. |
| `outcome`                 | string | `success`; `no_hss`, no HSS connected; `unreachable`, the serving node not connected; `timeout`, no answer in time; the error the node gave: `user_unknown`, `absent_user`, `user_busy_for_mt_sms`, `facility_not_supported`, `illegal_user`, `illegal_equipment`, `sm_delivery_failure`, `service_not_subscribed`, `service_barred`, `mwd_list_full`; `diameter_error`, another error answer; or `error`. |
| `result_code`             | int    | Result-Code or Experimental-Result-Code of the answer. |
| `vendor_id`               | int    | Vendor-Id of an Experimental-Result-Code. |
| `failure_cause`           | string | SM-Delivery-Failure-Cause of an `sm_delivery_failure`. |
| `tp_failure_cause`        | string | TP-Failure-Cause the phone gave. |
| `absent_user_diagnostics` | object | Why the phone is unreachable, by node: `mme`, `msc`, `sgsn`, `ip_sm_gw`, `smsf_3gpp`, `smsf_non_3gpp`. |

A value the SMSC did not observe is absent.

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No message has this identifier. |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "id": 1,
                "started_at": "2026-10-27T18:46:07.351Z",
                "completed_at": "2026-10-27T18:46:07.351Z",
                "step": "routing",
                "node": "core.example.org",
                "outcome": "absent_user",
                "result_code": 5550,
                "vendor_id": 10415,
                "absent_user_diagnostics": {
                    "mme": "imsi_detached"
                }
            },
            {
                "id": 2,
                "started_at": "2026-10-27T18:46:10.354Z",
                "completed_at": "2026-10-27T18:46:10.355Z",
                "step": "routing",
                "node": "core.example.org",
                "outcome": "success",
                "result_code": 2001
            },
            {
                "id": 3,
                "started_at": "2026-10-27T18:46:10.355Z",
                "completed_at": "2026-10-27T18:46:10.355Z",
                "step": "delivery",
                "node": "core.example.org",
                "node_type": "mme",
                "outcome": "success",
                "result_code": 2001
            }
        ],
        "page": 1,
        "per_page": 25,
        "total_count": 3
    }
}
```
