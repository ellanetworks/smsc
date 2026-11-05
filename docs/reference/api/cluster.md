---
description: RESTful API reference for the cluster of Ella SMSC nodes.
---

# Cluster

A cluster is three or more Ella SMSC nodes that share one database, replicated with raft. A write is committed once a majority of the voting nodes have it on disk, so a node can fail without losing a message. Every node accepts Diameter, SMPP and the API. One node, the leader, delivers the messages, and the others forward their writes to it.

Each node needs a `cluster` block in its [configuration file](../config_file.md). A node without one runs alone, and is a cluster of one.

## Get the Cluster

This path returns the node that answered, its cluster, and every member. Health comes from the leader, and is absent when no leader answers.

| Method | Path              |
| ------ | ----------------- |
| GET    | `/api/v1/cluster` |

### Sample Response

```json
{
    "result": {
        "node_id": "0199f6a2-5c1e-7d3a-9b1f-2a4c6e8d0f12",
        "cluster_id": "0199f6a2-4a10-7e55-8c3d-1b2d3e4f5a6b",
        "clustered": true,
        "leader_id": "0199f6a2-5c1e-7d3a-9b1f-2a4c6e8d0f12",
        "term": 4,
        "applied_index": 18342,
        "commit_index": 18342,
        "healthy": true,
        "failure_tolerance": 1,
        "members": [
            {
                "node_id": "0199f6a2-5c1e-7d3a-9b1f-2a4c6e8d0f12",
                "address": "10.0.0.1:7000",
                "diameter_label": "smsc",
                "version": "v0.9.0",
                "joined_at": "2026-11-05T09:12:44Z",
                "suffrage": "voter",
                "leader": true,
                "healthy": true,
                "applied_index": 18342
            },
            {
                "node_id": "0199f6a3-0b77-7c21-a4e0-6f1e2d3c4b5a",
                "address": "10.0.0.2:7000",
                "diameter_label": "smsc2",
                "version": "v0.9.0",
                "joined_at": "2026-11-05T09:14:02Z",
                "suffrage": "voter",
                "leader": false,
                "healthy": true,
                "last_contact": "312ms",
                "applied_index": 18342
            }
        ]
    }
}
```

| Field | Description |
| ----- | ----------- |
| `suffrage` | `voter`: the node counts towards the majority. `nonvoter`: a new node catching up, or a standby kept when the cluster has an even number of nodes or more than 5. `none`: not in the raft configuration. |
| `failure_tolerance` | How many voters can fail without stopping writes. |
| `last_contact` | Time since the leader last heard from the member. |

A new node joins as a non-voter, and becomes a voter after 10 seconds of good health. The cluster keeps an odd number of voters, up to 5. When a voter has been unhealthy for 10 seconds and a healthy standby exists, the standby replaces it.

## Create a Join Token

This path returns a token that lets one new node join the cluster. The token holds the address and the certificate fingerprint of every member, and is signed with a key that only the cluster knows. It needs a login, not an API token, and works once.

| Method | Path                     |
| ------ | ------------------------ |
| POST   | `/api/v1/cluster/tokens` |

### Request Body

| Name          | Type    | Description |
| ------------- | ------- | ----------- |
| `ttl_seconds` | integer | Optional. How long the token is valid, 300 to 86400. Default 1800. |

### Sample Response

```json
{
    "result": {
        "token": "AXsiaWQiOiI0ZjJk...",
        "expires_at": "2026-11-05T09:44:02Z"
    }
}
```

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `400` | The lifetime is out of range. |
| `409` | The node has no `cluster` block. |

## Join a Cluster

This path is called on the new node, with a token from a member. It needs no login, and works only while the new node has no user and no message: initialize the cluster, not the new node. The new node replaces its database with the cluster's, then takes the next free [Diameter label](#update-a-member).

| Method | Path                   |
| ------ | ---------------------- |
| POST   | `/api/v1/cluster/join` |

### Request Body

| Name    | Type   | Description |
| ------- | ------ | ----------- |
| `token` | string | The token from [Create a Join Token](#create-a-join-token). |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `400` | The token is not valid, has expired, or the node runs another version than the cluster. |
| `403` | The node has a user. |
| `409` | The node has a message, is already in a cluster, has no `cluster` block, or the token was used. |
| `502` | No member answered. |

## Update a Member

This path sets a member's Diameter label, the first label of its Diameter host: `<label>.node.epc.mnc<MNC>.mcc<MCC>.3gppnetwork.org`. The member restarts Diameter with the new host, and its peers reconnect. It needs a login, not an API token.

| Method | Path                                  |
| ------ | ------------------------------------- |
| PUT    | `/api/v1/cluster/members/{node_id}`   |

### Request Body

| Name             | Type   | Description |
| ---------------- | ------ | ----------- |
| `diameter_label` | string | 1 to 63 lowercase letters, digits and hyphens, not starting or ending with a hyphen. Unique in the cluster, and not a configured peer's host. |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `400` | The label is not valid. |
| `404` | No such member. |
| `409` | Another member or a peer has the host. |

## Remove a Member

This path removes a member. The removed node closes its Diameter and SMPP listeners, fails [readiness](status.md#check-readiness), and refuses to start again until its data directory is deleted. Removing the leader hands the leadership to another voter first. It needs a login, not an API token.

| Method | Path                                  |
| ------ | ------------------------------------- |
| DELETE | `/api/v1/cluster/members/{node_id}`   |

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `404` | No such member. |
| `503` | No leader. Retry. |

## Recover from the Loss of a Majority

When a majority of the voters are lost, writes stop. To recover, stop every surviving node, and run on each:

```shell
smsc cluster recover --config smsc.yaml
```

It prints the node's last log index and term. On the node with the highest term, then the highest index, run:

```shell
smsc cluster recover --config smsc.yaml --apply
```

and start it: it is now the only voter and the only member, and no longer trusts the others. Delete the data directory of every other survivor, start them, and join them again with new tokens.

Recovering a node other than the most up-to-date one loses the messages that only the others had.
