# Status

## Get the Status

This path returns the version of the SMSC, whether it is initialized, and the identity of the node that answered.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/status` |

### Response Fields

| Field            | Type    | Description                                                                                                                                   |
| ---------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `version`        | string  | Version.                                                                                                                                      |
| `revision`       | string  | Git commit of the build. Empty in development builds.                                                                                         |
| `initialized`    | boolean | Whether the first user exists. See [Initialize](https://docs.ellanetworks.com/smsc/reference/api/initialize/index.md).                        |
| `clustered`      | boolean | Whether the configuration file has a `cluster` block.                                                                                         |
| `node_id`        | string  | ID of the node that answered.                                                                                                                 |
| `display_name`   | string  | Display name of the node. Empty when unset. See [Update a Member](https://docs.ellanetworks.com/smsc/reference/api/cluster/#update-a-member). |
| `diameter_label` | string  | First label of the node's Diameter host: `smsc-` and the last 12 hex characters of `node_id`.                                                 |

### Sample Response

```
{
    "result": {
        "version": "v0.0.1",
        "revision": "79194832364de9583e8e659ba700c2fa9ba1e351",
        "initialized": true,
        "clustered": false,
        "node_id": "01a1ac75-e354-762b-b38f-013c0a7ca824",
        "display_name": "",
        "diameter_label": "smsc-013c0a7ca824"
    }
}
```

## Check Liveness

This path answers while the process serves HTTP.

| Method | Path            |
| ------ | --------------- |
| GET    | `/api/v1/livez` |

It answers `204 No Content`.

## Check Readiness

This path returns whether the SMSC can serve requests: its database answers, it is not shutting down, and, in a cluster, it is not draining and is in contact with the leader.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/readyz` |

It answers `204 No Content` when ready, and `503 Service Unavailable` otherwise:

```
{
    "error": "Not ready"
}
```
