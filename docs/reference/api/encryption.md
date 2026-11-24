---
description: RESTful API reference for the encryption keys of Ella SMSC.
---

# Encryption

The path requires Admin and an access token: an API token gets `403`.

## Rotate the Keys

This path rotates the encryption keys.

| Method | Path                         |
| ------ | ---------------------------- |
| POST   | `/api/v1/encryption/rotate`  |

### Sample Response

```json
{
    "result": {
        "message": "Encryption keys rotated"
    }
}
```

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `409` | The [encryption key is changing](../../how_to/change_the_encryption_key.md), or the keys changed meanwhile. Retry. |
| `421` | The node is not the leader. See [Leader-Only Paths](cluster.md#leader-only-paths). |
| `503` | The node's encryption keys are locked. |
