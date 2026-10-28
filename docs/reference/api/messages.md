---
description: RESTful API reference for submitting and viewing short messages in Ella SMSC.
---

# Messages

A message is a short message the SMSC received, from a phone, from the API, or from an ESME over [SMPP](../smpp.md), with its delivery status and the status reports the SMSC returned to the phone that sent it. A text longer than one short message is split into parts, one message each.

Responses carry no content. [Get the content of a message](#get-the-content-of-a-message) returns it, and logs an audit event.

## Create a Message

This path submits a text message, or a binary message, to a phone. It returns one message per part.

| Method | Path               |
| ------ | ------------------ |
| POST   | `/api/v1/messages` |

### Request Body

| Field  | Type   | Required | Description |
| ------ | ------ | -------- | ----------- |
| `from` | string | Yes      | Shown to the recipient as the sender. An E.164 number with `+`, or a name of 1 to 11 ASCII letters and digits, with single spaces between words, that is not all digits, such as `EllaSMSC`. A value without `+` is a name, delivered as given. |
| `to`   | string | Yes      | E.164 number with `+` of the recipient. |
| `text` | string | One of `text` and `binary` | Not empty. GSM-7 if every character is in the GSM 7-bit default alphabet or its extension table, else UCS-2. Up to 255 parts. |
| `binary` | object | One of `text` and `binary` | TPDU parameters of one short message, delivered unchanged. |
| `binary.protocol_identifier` | int | No | TP-PID (TS 23.040 §9.2.3.9), `0` to `255`. Default `0`. Not `32` to `62`, telematic interworking. `65` to `71`, replace types 1 to 7, replace the pending message of the same type from the same `from`, submitted by the same user; never a message from a phone or an ESME. `72`, device triggering, needs an application port addressing information element in `user_data_header`. |
| `binary.data_coding_scheme` | int | Yes | TP-DCS (TS 23.038 §4), `0` to `255`. Not a GSM 7-bit, UCS2 or reserved coding. Message class 2 when `protocol_identifier` is `124`, ANSI-136 R-DATA, or `127`, (U)SIM data download. |
| `binary.user_data_header` | string | No | TP-UDH (TS 23.040 §9.2.3.24) in hexadecimal, its length octet included. |
| `binary.user_data` | string | Yes | TP-UD after the header, in hexadecimal. May be empty. At most 140 octets with `user_data_header`. Not split into parts. |
| `validity_seconds` | int | No | How long delivery is attempted, `1` to `315360000` (10 years). Default: the default validity of the [delivery settings](delivery.md). |

A message from the API never asks for status reports.

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `400` | A field is missing or invalid, both or neither of `text` and `binary`, the text needs more than 255 parts, or the binary user data is over 140 octets. |

### Sample Request

```json
{
    "from": "+15550001000",
    "to": "+15551230002",
    "text": "Your code is 1234"
}
```

### Sample Request (Binary)

```json
{
    "from": "+15550001000",
    "to": "+15551230002",
    "binary": {
        "protocol_identifier": 127,
        "data_coding_scheme": 246,
        "user_data_header": "027000",
        "user_data": "0011223344"
    }
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

This path returns the list of messages, most recent first. For a Submitter, only the messages it sent.

| Method | Path               |
| ------ | ------------------ |
| GET    | `/api/v1/messages` |

### Query Parameters

| Name       | In    | Type | Default | Allowed | Description |
| ---------- | ----- | ---- | ------- | ------- | ----------- |
| `to`       | query | str  |         | E.164 with `+` | Recipient. |
| `from`     | query | str  |         | E.164 with `+`, or a name as in [Create a Message](#create-a-message) | Sender. |
| `status`   | query | str  |         | `pending`, `delivered`, `failed`, `expired`, `replaced` | Status. |
| `start`    | query | str  |         | RFC 3339 time | Created at or after. |
| `end`      | query | str  |         | RFC 3339 time, not before `start` | Created before. |
| `page`     | query | int  | `1`     | `>= 1`  | 1-based page index. |
| `per_page` | query | int  | `25`    | `1…100` | Number of items per page. |

### Response Fields

| Field                           | Type    | Description |
| ------------------------------- | ------- | ----------- |
| `id`                            | int     | Message identifier. |
| `from`                          | string  | Sender: the MSISDN of the phone, an E.164 number with `+`, the `from` of the API request, or the `source_addr` of the `submit_sm`, with `+` when international. |
| `to`                            | string  | Recipient, an E.164 number with `+`. |
| `encoding`                      | string  | `gsm7`, `ucs2` or `binary`. |
| `concatenation`                 | object  | Present for a part of a longer message. |
| `concatenation.reference`       | int     | Reference shared by the parts of the message. |
| `concatenation.part`            | int     | Number of this part, from 1. |
| `concatenation.total`           | int     | Number of parts. |
| `status`                        | string  | `pending`; `delivered`; `failed`; `expired`, when its validity period ended before its delivery; or `replaced`, by a later message from the same sender, of the same replace type, before its delivery. |
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
| `submitted_by`                  | string  | Email of the user that sent the message over the API, or of the user of the ESME that sent it over SMPP. Absent for a message from a phone. |
| `system_id`                     | string  | `system_id` of the [ESME](esmes.md) that sent the message over SMPP. Absent for other messages, and once the ESME is deleted. |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "id": 4,
                "from": "+15550001000",
                "to": "+15551230001",
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

This path returns a specific message. For a Submitter, a message it did not send is not found.

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

## Get the Content of a Message

This path returns the content of a message, and logs an audit event `read_message_content` with the email of the caller. For a Submitter, a message it did not send is not found. A Read Only user cannot read content.

| Method | Path                            |
| ------ | ------------------------------- |
| GET    | `/api/v1/messages/{id}/content` |

### Path Parameters

| Name | Type | Description |
| ---- | ---- | ----------- |
| `id` | int  | Message identifier. |

### Response Fields

| Field  | Type   | Description |
| ------ | ------ | ----------- |
| `text` | string | Text of the part. Absent for a binary message. |
| `binary` | object | TPDU parameters of a binary message. |
| `binary.protocol_identifier` | int | TP-PID. |
| `binary.data_coding_scheme` | int | TP-DCS. |
| `binary.user_data_header` | string | TP-UDH in hexadecimal, its length octet included. Absent when there is none. |
| `binary.user_data` | string | TP-UD after the header, in hexadecimal. |
| `deliver_reports` | array | SMS-DELIVER-REPORTs (TS 23.040 §9.2.2.1a) the serving nodes passed on, oldest first. Absent when there are none. |
| `deliver_reports[].attempt_id` | int | [Delivery attempt](#list-delivery-attempts) of the report: in the RP-ACK when its `outcome` is `success`, else in the RP-ERROR. |
| `deliver_reports[].tpdu` | string | The report in hexadecimal. The other `deliver_reports[]` fields are absent when it does not decode. |
| `deliver_reports[].protocol_identifier` | int | TP-PID, when present. |
| `deliver_reports[].data_coding_scheme` | int | TP-DCS, when present. |
| `deliver_reports[].user_data_header` | string | TP-UDH in hexadecimal, when present. |
| `deliver_reports[].user_data` | string | TP-UD after the header, in hexadecimal, when present. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No message has this identifier. |

### Sample Response

```json
{
    "result": {
        "text": "Your code is 1234"
    }
}
```

### Sample Response (Binary)

```json
{
    "result": {
        "binary": {
            "protocol_identifier": 127,
            "data_coding_scheme": 246,
            "user_data_header": "027000",
            "user_data": "0011223344"
        },
        "deliver_reports": [
            {
                "attempt_id": 2,
                "tpdu": "00077ff602cafe",
                "protocol_identifier": 127,
                "data_coding_scheme": 246,
                "user_data": "cafe"
            }
        ]
    }
}
```

## List Delivery Attempts

This path returns the routing and delivery attempts of a message, oldest first. For a Submitter, a message it did not send is not found.

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
