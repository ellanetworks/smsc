---
description: RESTful API reference for the Diameter status and the configured Diameter peers of Ella SMSC.
---

# Diameter

The HSSs, MMEs, SMSFs and IP-SM-GWs of the home network connect to the SMSC, and need no configuration. The configured peers are the Diameter agents that the SMSC connects to, such as the DEA of an IPX provider or a DRA in front of the HSS, with the realms they route to.

Reading requires Read Only, Network Manager or Admin. Creating, updating and deleting peers requires Network Manager or Admin.

## Get the Diameter Status

This path returns the Diameter identity of the SMSC, derived from the operator settings, its peers, and its routes.

| Method | Path               |
| ------ | ------------------ |
| GET    | `/api/v1/diameter` |

### Response Fields

| Field                  | Type    | Description |
| ---------------------- | ------- | ----------- |
| `host`                 | string  | Origin-Host of the SMSC. |
| `realm`                | string  | Origin-Realm of the SMSC. |
| `peers`                | array   | The peers connected to the SMSC, and the configured peers. |
| `peers[].id`           | int     | ID of a configured peer. Absent for a peer that connected to the SMSC. |
| `peers[].configured`   | boolean | Whether the peer is a configured peer. |
| `peers[].host`         | string  | Origin-Host of the peer. |
| `peers[].realm`        | string  | Origin-Realm of the peer. |
| `peers[].address`      | string  | Remote address of the connection. |
| `peers[].state`        | string  | `down`, `connecting`, `open`, `suspect`, `reopen` or `closing`. |
| `peers[].applications` | array   | Applications the peer supports: `s6c`, `sgd`. |
| `peers[].since`        | string  | Time of the last state change. |
| `peers[].error`        | string  | Why the last connection to a configured peer failed or closed. |
| `routes`               | array   | The `s6c` route to the HSS, then the routes of the configured peers, default routes last. |
| `routes[].realm`       | string  | Destination-Realm of the route. Empty for a default route. |
| `routes[].default`     | boolean | Whether the route takes the realms that no other route or connected peer serves, other than the SMSC's own. |
| `routes[].application` | string  | `s6c` or `sgd`. |
| `routes[].peers`       | array   | Hosts of the open peers that serve the route. For the first route, the HSSs: the open peers in the SMSC's realm that support S6c, and the configured peers with an `s6c` route to that realm. |

### Sample Response

```json
{
    "result": {
        "host": "smsc.node.epc.mnc001.mcc001.3gppnetwork.org",
        "realm": "epc.mnc001.mcc001.3gppnetwork.org",
        "peers": [
            {
                "configured": false,
                "host": "core.example.org",
                "realm": "epc.mnc001.mcc001.3gppnetwork.org",
                "address": "127.0.0.1",
                "state": "open",
                "applications": ["sgd", "s6c"],
                "since": "2026-10-27T18:46:07.330Z"
            },
            {
                "id": 1,
                "configured": true,
                "host": "dea1.ipx.example.net",
                "realm": "ipx.example.net",
                "address": "192.0.2.10",
                "state": "open",
                "applications": ["sgd", "s6c"],
                "since": "2026-10-27T18:47:12.004Z"
            }
        ],
        "routes": [
            {
                "realm": "epc.mnc001.mcc001.3gppnetwork.org",
                "default": false,
                "application": "s6c",
                "peers": ["core.example.org"]
            },
            {
                "realm": "",
                "default": true,
                "application": "sgd",
                "peers": ["dea1.ipx.example.net"]
            }
        ]
    }
}
```

## List Peers

| Method | Path                     |
| ------ | ------------------------ |
| GET    | `/api/v1/diameter/peers` |

### Sample Response

```json
{
    "result": {
        "items": [
            {
                "id": 1,
                "host": "dea1.ipx.example.net",
                "addresses": ["192.0.2.10", "198.51.100.10"],
                "port": 3868,
                "routes": [
                    { "default": true, "application": "sgd", "priority": 0 }
                ],
                "status": {
                    "state": "open",
                    "host": "dea1.ipx.example.net",
                    "realm": "ipx.example.net",
                    "since": "2026-10-27T18:47:12.004Z"
                }
            }
        ]
    }
}
```

## Create a Peer

The SMSC connects to the peer over SCTP, from `diameter.address`, keeps the connection up, and reconnects after 1 to 30 seconds when it drops.

| Method | Path                     |
| ------ | ------------------------ |
| POST   | `/api/v1/diameter/peers` |

### Request Body

| Field                  | Type    | Required | Description |
| ---------------------- | ------- | -------- | ----------- |
| `host`                 | string  | No       | Diameter identity of the peer. When set, the peer must give it as its Origin-Host, and connect from one of its `addresses`. Empty accepts the host the peer gives. |
| `addresses`            | array   | Yes      | IP addresses of the peer, more than one for a multi-homed peer. List all of them: a peer with a `host` that connects from another address is refused. |
| `port`                 | int     | No       | Diameter port of the peer. Default `3868`. |
| `routes`               | array   | No       | What the SMSC sends the peer. |
| `routes[].realm`       | string  | Yes, unless `default` | Destination-Realm the route serves, e.g. `epc.mnc002.mcc208.3gppnetwork.org`. |
| `routes[].default`     | boolean | No       | `true` for the default route, which takes every realm that no other route or connected peer serves, other than the SMSC's own. `sgd` only, and without `realm`. |
| `routes[].application` | string  | Yes      | `sgd`: short messages to the MMEs, SMSFs and IP-SM-GWs of the realm. `s6c`: routing requests to the HSS, with the SMSC's own realm. |
| `routes[].priority`    | int     | No       | The peers of a route are tried lowest priority first; peers of equal priority share the requests. Default `0`. |

A peer without an `s6c` route is not asked for routing, even when it is the HSS: a configured peer serves only its routes.

### Sample Request

```json
{
    "host": "dea1.ipx.example.net",
    "addresses": ["192.0.2.10", "198.51.100.10"],
    "routes": [
        { "default": true, "application": "sgd" },
        { "realm": "epc.mnc001.mcc001.3gppnetwork.org", "application": "s6c" }
    ]
}
```

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `201` | The peer, with its `id`. |
| `400` | Invalid peer. |
| `409` | Another peer has the same `host`, two peers without a `host` share an address, or `host` is the SMSC's own. |

## Get a Peer

| Method | Path                          |
| ------ | ----------------------------- |
| GET    | `/api/v1/diameter/peers/{id}` |

The `status` of the peer is absent until the SMSC applies a new peer.

| Field           | Type   | Description |
| --------------- | ------ | ----------- |
| `status.state`  | string | `down`, `connecting`, `open`, `suspect`, `reopen` or `closing`. |
| `status.host`   | string | Origin-Host the peer gave. |
| `status.realm`  | string | Origin-Realm the peer gave. |
| `status.since`  | string | Time of the last state change. |
| `status.error`  | string | Why the last connection failed or closed. |

## Update a Peer

The request body is that of [Create a Peer](#create-a-peer). A change of routes or priorities keeps the connection; a change of `host`, `addresses` or `port` replaces it.

| Method | Path                          |
| ------ | ----------------------------- |
| PUT    | `/api/v1/diameter/peers/{id}` |

## Delete a Peer

The SMSC closes the connection to the peer.

| Method | Path                          |
| ------ | ----------------------------- |
| DELETE | `/api/v1/diameter/peers/{id}` |
