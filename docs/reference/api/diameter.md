---
description: RESTful API reference for viewing the Diameter peers of Ella SMSC.
---

# Diameter

Diameter peers are the HSSs, MMEs, SMSFs and IP-SM-GWs connected to the SMSC. They connect to it; the SMSC has no peers to configure.

## Get the Diameter Status

This path returns the Diameter identity of the SMSC, derived from the operator settings, its connected peers, and the peers it routes S6c requests to.

| Method | Path               |
| ------ | ------------------ |
| GET    | `/api/v1/diameter` |

### Response Fields

| Field                  | Type   | Description |
| ---------------------- | ------ | ----------- |
| `host`                 | string | Origin-Host of the SMSC. |
| `realm`                | string | Origin-Realm of the SMSC. |
| `peers`                | array  | Connected peers. |
| `peers[].host`         | string | Origin-Host of the peer. |
| `peers[].realm`        | string | Origin-Realm of the peer. |
| `peers[].address`      | string | Remote address of the connection. |
| `peers[].state`        | string | `down`, `connecting`, `open`, `suspect`, `reopen` or `closing`. |
| `peers[].applications` | array  | Applications the peer supports: `s6c`, `sgd`. |
| `peers[].since`        | string | Time of the last state change. |
| `routes`               | array  | The `s6c` route. |
| `routes[].realm`       | string | Destination-Realm of the S6c requests: the SMSC's realm. |
| `routes[].application` | string | `s6c`. |
| `routes[].peers`       | array  | Hosts of the open peers in that realm that support S6c: the HSSs. |

### Sample Response

```json
{
    "result": {
        "host": "smsc.node.epc.mnc001.mcc001.3gppnetwork.org",
        "realm": "epc.mnc001.mcc001.3gppnetwork.org",
        "peers": [
            {
                "host": "core.example.org",
                "realm": "epc.mnc001.mcc001.3gppnetwork.org",
                "address": "127.0.0.1",
                "state": "open",
                "applications": ["sgd", "s6c"],
                "since": "2026-10-27T18:46:07.330Z"
            }
        ],
        "routes": [
            {
                "realm": "epc.mnc001.mcc001.3gppnetwork.org",
                "application": "s6c",
                "peers": ["core.example.org"]
            }
        ]
    }
}
```
