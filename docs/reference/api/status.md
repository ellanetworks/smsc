---
description: RESTful API reference for viewing the status and health of Ella SMSC.
---

# Status

## Get the Status

This path returns the version of the SMSC, and whether it is initialized.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/status` |

### Response Fields

| Field      | Type   | Description |
| ---------- | ------ | ----------- |
| `version`  | string | Version. |
| `revision` | string | Git commit of the build. Empty in development builds. |
| `initialized` | boolean | Whether the first user exists. See [Initialize](initialize.md). |

### Sample Response

```json
{
    "result": {
        "version": "v0.0.1",
        "revision": "79194832364de9583e8e659ba700c2fa9ba1e351",
        "initialized": true
    }
}
```

## Check Liveness

This path answers while the process serves HTTP.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/livez`  |

It answers `204 No Content`.

## Check Readiness

This path returns whether the SMSC can serve requests: its database answers, it is not shutting down, and in a [cluster](cluster.md) it is a member, it heard from the leader within two election timeouts, and its database is fewer than 1,000 log entries behind.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/readyz` |

It answers `204 No Content` when ready, and `503 Service Unavailable` otherwise:

```json
{
    "error": "Not ready"
}
```
