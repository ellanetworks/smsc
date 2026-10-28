---
description: RESTful API reference for managing how long Ella SMSC keeps messages.
---

# Retention

The retention sets how long the SMSC keeps a message once it has its final status: `delivered`, `failed`, `expired` or `replaced`. The SMSC then deletes it, with its status reports and delivery attempts, within the hour. It keeps a message until its status reports, too, have had their final status for that long.

## Get the Retention

This path returns the retention.

| Method | Path                         |
| ------ | ---------------------------- |
| GET    | `/api/v1/messages/retention` |

### Response Fields

| Field  | Type | Description |
| ------ | ---- | ----------- |
| `days` | int  | Days a message is kept once it has its final status. Default: `30`. |

### Sample Response

```json
{
    "result": {
        "days": 30
    }
}
```

## Update the Retention

This path replaces the retention. It applies from the next deletion, within the hour.

| Method | Path                         |
| ------ | ---------------------------- |
| PUT    | `/api/v1/messages/retention` |

### Request Body

| Field  | Type | Required | Description |
| ------ | ---- | -------- | ----------- |
| `days` | int  | Yes      | At least 1. |

### Sample Request

```json
{
    "days": 7
}
```

### Sample Response

```json
{
    "result": {
        "days": 7
    }
}
```
