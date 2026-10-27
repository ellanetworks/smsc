---
description: RESTful API reference for viewing the status of Ella SMSC.
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
