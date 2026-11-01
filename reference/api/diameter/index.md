# Diameter

Configured peers are the Diameter agents that the SMSC connects to, and their routes. Creating, updating and deleting them requires Network Manager or Admin.

## Get the Diameter Status

This path returns the Diameter identity of the SMSC, its peers, and its routes.

| Method | Path               |
| ------ | ------------------ |
| GET    | `/api/v1/diameter` |

### Response Fields

| Field                  | Type    | Description                                                                               |
| ---------------------- | ------- | ----------------------------------------------------------------------------------------- |
| `host`                 | string  | Origin-Host of the SMSC.                                                                  |
| `realm`                | string  | Origin-Realm of the SMSC.                                                                 |
| `peers`                | array   | Connected and configured peers.                                                           |
| `peers[].id`           | int     | ID of a configured peer.                                                                  |
| `peers[].configured`   | boolean | Whether the peer is configured.                                                           |
| `peers[].host`         | string  | Origin-Host of the peer.                                                                  |
| `peers[].realm`        | string  | Origin-Realm of the peer.                                                                 |
| `peers[].address`      | string  | Remote address of the connection.                                                         |
| `peers[].state`        | string  | `down`, `connecting`, `open`, `suspect`, `reopen` or `closing`.                           |
| `peers[].applications` | array   | Applications the peer supports: `s6c`, `sgd`.                                             |
| `peers[].since`        | string  | Time of the last state change.                                                            |
| `peers[].error`        | string  | Last connection error of a configured peer.                                               |
| `routes`               | array   | The `s6c` route to the HSS, then the routes of the configured peers, default routes last. |
| `routes[].realm`       | string  | Destination-Realm. Empty for a default route.                                             |
| `routes[].default`     | boolean | Whether the route is a default route.                                                     |
| `routes[].application` | string  | `s6c` or `sgd`.                                                                           |
| `routes[].peers`       | array   | Hosts of the open peers of the route.                                                     |

### Sample Response

```
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

```
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

| Method | Path                     |
| ------ | ------------------------ |
| POST   | `/api/v1/diameter/peers` |

### Request Body

| Field                  | Type    | Required         | Description                                                                                           |
| ---------------------- | ------- | ---------------- | ----------------------------------------------------------------------------------------------------- |
| `host`                 | string  | No               | Origin-Host the peer must give. Empty accepts any.                                                    |
| `addresses`            | array   | Yes              | IP addresses of the peer. A peer with a `host` must connect from one of them.                         |
| `port`                 | int     | No               | Default `3868`.                                                                                       |
| `routes`               | array   | No               | Routes of the peer.                                                                                   |
| `routes[].realm`       | string  | Unless `default` | Destination-Realm, e.g. `epc.mnc002.mcc208.3gppnetwork.org`.                                          |
| `routes[].default`     | boolean | No               | Default route: every realm without another route, except the SMSC's own. `sgd` only, without `realm`. |
| `routes[].application` | string  | Yes              | `sgd` or `s6c`. An `s6c` route takes the SMSC's own realm.                                            |
| `routes[].priority`    | int     | No               | Lowest first; equal priorities share the load. Default `0`.                                           |

### Sample Request

```
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

| Code  | Description                                                                             |
| ----- | --------------------------------------------------------------------------------------- |
| `201` | The peer, with its `id`.                                                                |
| `400` | Invalid peer.                                                                           |
| `409` | `host` is another peer's or the SMSC's, or two peers without a `host` share an address. |

## Get a Peer

| Method | Path                          |
| ------ | ----------------------------- |
| GET    | `/api/v1/diameter/peers/{id}` |

### Response Fields

The fields of the request body, plus:

| Field          | Type   | Description                                                                                                  |
| -------------- | ------ | ------------------------------------------------------------------------------------------------------------ |
| `id`           | int    | ID of the peer.                                                                                              |
| `status`       | object | Connection to the peer. Absent until the SMSC applies the peer, and from the responses to create and update. |
| `status.state` | string | `down`, `connecting`, `open`, `suspect`, `reopen` or `closing`.                                              |
| `status.host`  | string | Origin-Host the peer gave.                                                                                   |
| `status.realm` | string | Origin-Realm the peer gave.                                                                                  |
| `status.since` | string | Time of the last state change.                                                                               |
| `status.error` | string | Last connection error.                                                                                       |

## Update a Peer

The request body is that of [Create a Peer](#create-a-peer). A change of `host`, `addresses` or `port` reconnects.

| Method | Path                          |
| ------ | ----------------------------- |
| PUT    | `/api/v1/diameter/peers/{id}` |

## Delete a Peer

| Method | Path                          |
| ------ | ----------------------------- |
| DELETE | `/api/v1/diameter/peers/{id}` |
