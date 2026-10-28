---
description: RESTful API reference for managing the ESME accounts of the SMPP listener of Ella SMSC.
---

# ESMEs

An ESME account is what an External Short Message Entity, such as an OTA platform, binds to the [SMPP listener](../smpp.md) with. It acts as its user: the messages it submits are the user's, and the user's role decides which binds it may make. See [Binds](../smpp.md#binds).

Reading ESMEs requires Admin, Read Only or Network Manager. Creating, updating and deleting them requires Admin, and an access token: an API token gets `403`.

## List ESMEs

| Method | Path            |
| ------ | --------------- |
| GET    | `/api/v1/esmes` |

### Response Fields

| Field                       | Type    | Description |
| --------------------------- | ------- | ----------- |
| `system_id`                 | string  | The `system_id` the ESME binds with. |
| `user`                      | string  | Email of the user the ESME acts as. |
| `allowed_networks`          | array   | IP prefixes the ESME may bind from. |
| `max_sessions`              | int     | Most sessions bound at once. |
| `forward_sim_answers`       | boolean | Whether the SIM's answer to a (U)SIM data download message is forwarded without a request in `registered_delivery`. |
| `default_alphabet`          | string  | What the ESME means by `data_coding` `0x00`: `gsm7` or `latin1`. |
| `created_at`                | string  | Time the ESME was created. |
| `sessions`                  | array   | Bound sessions, oldest first. |
| `sessions[].bind_type`      | string  | `transmitter`, `receiver` or `transceiver`. |
| `sessions[].remote_address` | string  | Address and port of the client. |
| `sessions[].bound_at`       | string  | Time of the bind. |

The password is never returned.

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "system_id": "ota",
                "user": "ota@example.com",
                "allowed_networks": ["10.1.0.0/24"],
                "max_sessions": 4,
                "forward_sim_answers": true,
                "default_alphabet": "gsm7",
                "created_at": "2026-10-27T18:40:02.118Z",
                "sessions": [
                    {
                        "bind_type": "transceiver",
                        "remote_address": "10.1.0.5:40312",
                        "bound_at": "2026-10-27T18:41:55.906Z"
                    }
                ]
            }
        ]
    }
}
```

## Create an ESME

At most 50 ESMEs. Without a `password`, the SMSC generates one of 8 letters and digits, and returns it as `password` in this response only.

| Method | Path            |
| ------ | --------------- |
| POST   | `/api/v1/esmes` |

### Request Body

| Field                 | Type    | Required | Description |
| --------------------- | ------- | -------- | ----------- |
| `system_id`           | string  | Yes      | 1 to 15 printable ASCII characters (SMPP 3.4 §4.1.1). |
| `user`                | string  | Yes      | Email of an existing user. A dedicated Submitter user is recommended. |
| `password`            | string  | No       | 1 to 8 printable ASCII characters (SMPP 3.4 §4.1.1). Default: generated. |
| `allowed_networks`    | array   | No       | IP prefixes, such as `10.1.0.0/24` or `2001:db8::/64`, at most 50. Default: none, which allows no address. |
| `max_sessions`        | int     | No       | `1` to `16`. Default `4`. |
| `forward_sim_answers` | boolean | No       | Forward the SIM's answer to a message with `protocol_id` `0x7F` even when `registered_delivery` does not ask for an SME Manual/User Acknowledgement. Default `false`. See [SIM answers](../smpp.md#sim-answers). |
| `default_alphabet`    | string  | No       | What the ESME means by `data_coding` `0x00`: `gsm7`, GSM 7-bit default alphabet septets one per octet, or `latin1`. Default `gsm7`. |

### Sample Request

```json
{
    "system_id": "ota",
    "user": "ota@example.com",
    "allowed_networks": ["10.1.0.0/24"],
    "forward_sim_answers": true
}
```

### Sample Response

```json
{
    "result": {
        "system_id": "ota",
        "user": "ota@example.com",
        "allowed_networks": ["10.1.0.0/24"],
        "max_sessions": 4,
        "forward_sim_answers": true,
        "default_alphabet": "gsm7",
        "created_at": "2026-10-27T18:40:02.118Z",
        "sessions": [],
        "password": "q7Rk2LxW"
    }
}
```

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `201` | The ESME, with the generated `password` when none was given. |
| `400` | Invalid field, no user with the email, or 50 ESMEs exist. |
| `409` | An ESME has the `system_id`. |

## Get an ESME

| Method | Path                        |
| ------ | --------------------------- |
| GET    | `/api/v1/esmes/{system_id}` |

### Path Parameters

| Name        | Type   | Description |
| ----------- | ------ | ----------- |
| `system_id` | string | The `system_id` of the ESME, URL-encoded. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No ESME has this `system_id`. |

## Update an ESME

This path replaces the settings of an ESME, and closes its sessions, which bind again under the new settings. The `system_id` and the user cannot be changed.

| Method | Path                        |
| ------ | --------------------------- |
| PUT    | `/api/v1/esmes/{system_id}` |

### Request Body

| Field                 | Type    | Required | Description |
| --------------------- | ------- | -------- | ----------- |
| `password`            | string  | No       | 1 to 8 printable ASCII characters. Without it, the password stays. |
| `allowed_networks`    | array   | Yes      | As in [Create an ESME](#create-an-esme). |
| `max_sessions`        | int     | Yes      | `1` to `16`. |
| `forward_sim_answers` | boolean | Yes      | As in [Create an ESME](#create-an-esme). |
| `default_alphabet`    | string  | Yes      | `gsm7` or `latin1`. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `200` | The ESME. |
| `400` | Invalid field. |
| `404` | No ESME has this `system_id`. |

## Delete an ESME

This path deletes an ESME, closes its sessions, and drops the receipts and SIM answers not yet sent to it. Its messages stay, as its user's. Deleting the user deletes its ESMEs.

| Method | Path                        |
| ------ | --------------------------- |
| DELETE | `/api/v1/esmes/{system_id}` |

### Sample Response

```json
{
    "result": {
        "message": "ESME deleted successfully"
    }
}
```
