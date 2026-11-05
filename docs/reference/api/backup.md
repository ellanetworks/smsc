---
description: RESTful API reference for backing up and restoring Ella SMSC.
---

# Backup

A backup holds every message, user and setting: message content, user accounts, and the single sign-on client secret. Store and transfer it encrypted.

## Back Up

This path returns a backup while Ella SMSC keeps running: a gzipped tar archive of a consistent copy of the database, a manifest, and the SHA-256 checksums of both. It takes an API token, so that a scheduled job can call it.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/backup` |

### Response

`200`, with `Content-Type: application/gzip` and the file name in `Content-Disposition`, such as `smsc-backup-20261104T120000Z.tar.gz`. The archive holds:

| File          | Description |
| ------------- | ----------- |
| `manifest.json` | The archive format, the database schema version, its position in the replicated log, the ID sequences, and the time of the backup. |
| `SHA256SUMS`  | The SHA-256 checksums of the database and the manifest. |
| `smsc.db`     | The database. |

## Restore

This path replaces every message, user and setting with those of a backup, while Ella SMSC keeps running. It needs a login, not an API token.

It checks the checksums, and refuses a backup whose schema is newer than this version's. IDs keep increasing: an ID returned before the restore, such as a `message_id` given to an ESME, is never given again. Deliveries stop during the restore and start again after it. SMPP sessions are closed, and ESMEs bind again with the restored accounts.

| Method | Path              |
| ------ | ----------------- |
| POST   | `/api/v1/restore` |

### Request Body

The archive from [Back Up](#back-up), with `Content-Type: application/gzip`.

### Response Codes

| Code  | Description |
| ----- | ----------- |
| `400` | Not a backup, a checksum does not match, or the backup's schema is newer than this version's. |
| `503` | Not ready to restore yet, such as just after a start. Retry. |

### Sample Response

```json
{
    "result": {
        "message": "Backup restored successfully"
    }
}
```
