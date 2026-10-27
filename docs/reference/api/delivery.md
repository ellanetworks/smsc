---
description: RESTful API reference for managing the delivery settings of Ella SMSC.
---

# Delivery

The delivery settings set how long the SMSC keeps a message for delivery, and when it retries a failed delivery.

## Get the Delivery Settings

This path returns the delivery settings.

| Method | Path               |
| ------ | ------------------ |
| GET    | `/api/v1/delivery` |

### Response Fields

| Field                      | Type  | Description |
| -------------------------- | ----- | ----------- |
| `default_validity_seconds` | int   | How long a message without its own validity period is kept for delivery, in seconds. Status reports are kept as long. |
| `retry_intervals_seconds`  | array | Wait before each retry, in seconds. The last interval repeats. Empty: a failed delivery is not retried. |

### Sample Response

```json
{
    "result": {
        "default_validity_seconds": 604800,
        "retry_intervals_seconds": [60, 300, 900, 3600]
    }
}
```

## Update the Delivery Settings

This path replaces the delivery settings. The new validity applies to the messages received afterwards; the new intervals, to the next retries.

| Method | Path               |
| ------ | ------------------ |
| PUT    | `/api/v1/delivery` |

### Request Body

| Field                      | Type  | Required | Description |
| -------------------------- | ----- | -------- | ----------- |
| `default_validity_seconds` | int   | Yes      | At least 1. |
| `retry_intervals_seconds`  | array | Yes      | Each at least 1. |

### Sample Request

```json
{
    "default_validity_seconds": 86400,
    "retry_intervals_seconds": [60, 300]
}
```

### Sample Response

```json
{
    "result": {
        "default_validity_seconds": 86400,
        "retry_intervals_seconds": [60, 300]
    }
}
```
