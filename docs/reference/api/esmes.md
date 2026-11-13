---
description: RESTful API reference for managing the ESME accounts of the SMPP listener of Ella SMSC.
---

# ESMEs

An ESME account is what an External Short Message Entity, such as an OTA platform, binds to the [SMPP listener](../smpp.md) with, with at most 4 sessions at once. It acts as its user: the messages it submits are the user's, and the user's role decides which binds it may make. See [Binds](../smpp.md#binds).

An ESME with routes also receives the short messages sent to the numbers of its routes, as `deliver_sm` on a receiver or transceiver session. See [Routed messages](../smpp.md#routed-messages).

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
| `routes`                    | array   | Destination numbers the ESME receives, sorted by their first number: a number, such as `4000`, or a range, such as `40000-40099` or `+15551230000-+15551230999`. |
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
                "routes": ["4000"],
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
| `user`                | string  | Yes      | Email of an existing user, the user the ESME acts as. |
| `password`            | string  | No       | 1 to 8 printable ASCII characters (SMPP 3.4 §4.1.1). Default: generated. |
| `allowed_networks`    | array   | No       | IP prefixes, such as `10.1.0.0/24` or `2001:db8::/64`, at most 50. Default: none, which allows no address. |
| `routes`              | array   | No       | Destination numbers the ESME receives, at most 64. A route is a number, or a range of two numbers of the same length joined by `-`, first the lower: 1 to 20 digits for a dialled number, such as the short code `4000` or `40000-40099`, or `+` and 1 to 15 digits for an international number, such as `+15551230000-+15551230999`. Routes of the same form must not overlap, on this ESME or another. See [Routed messages](../smpp.md#routed-messages) for how they match. Default: none. |

### Sample Request

```json
{
    "system_id": "ota",
    "user": "ota@example.com",
    "allowed_networks": ["10.1.0.0/24"],
    "routes": ["4000"]
}
```

### Sample Response

```json
{
    "result": {
        "system_id": "ota",
        "user": "ota@example.com",
        "allowed_networks": ["10.1.0.0/24"],
        "routes": ["4000"],
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
| `400` | Invalid field, two of the `routes` overlap, no user with the email, or 50 ESMEs exist. |
| `409` | An ESME has the `system_id`, or one of the `routes` overlaps a route of another ESME. |

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

This path replaces the allowed networks of an ESME, and its routes and its password when they are given. A new password or a change of the allowed networks closes its sessions, which bind again under the new settings. A change of the routes alone keeps them. The `system_id` and the user cannot be changed.

| Method | Path                        |
| ------ | --------------------------- |
| PUT    | `/api/v1/esmes/{system_id}` |

### Request Body

| Field                 | Type    | Required | Description |
| --------------------- | ------- | -------- | ----------- |
| `password`            | string  | No       | 1 to 8 printable ASCII characters. Without it, the password stays. |
| `allowed_networks`    | array   | Yes      | As in [Create an ESME](#create-an-esme). |
| `routes`              | array   | No       | As in [Create an ESME](#create-an-esme). The routes are replaced as a set: `[]` removes them all. Without it, the routes stay. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `200` | The ESME. |
| `400` | Invalid field, or two of the `routes` overlap. |
| `404` | No ESME has this `system_id`. |
| `409` | One of the `routes` overlaps a route of another ESME. |

## Delete an ESME

This path deletes an ESME, closes its sessions, drops the receipts and SIM answers not yet sent to it, and fails the messages waiting on a receiver of it, with a status report to their senders when they asked for one. Its messages stay, as its user's. Deleting the user deletes its ESMEs.

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
