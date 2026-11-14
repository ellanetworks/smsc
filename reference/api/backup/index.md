# Backup

A backup holds every message, user and setting, message content and the single sign-on client secret included. It leaves out the cluster's secrets: the key that signs access tokens, the single sign-on cookie key, and the key and tokens to join a node.

Both paths require Admin. Restoring requires an access token: an API token gets `403`.

## Back Up

This path returns a backup of the database.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/backup` |

### Response

A gzipped tar archive, with `Content-Type: application/gzip` and its file name in `Content-Disposition`, such as `smsc-backup-20261104T120000Z.tar.gz`.

| File            | Description                                         |
| --------------- | --------------------------------------------------- |
| `manifest.json` | Format, schema version and time of the backup.      |
| `SHA256SUMS`    | SHA-256 checksums of `smsc.db` and `manifest.json`. |
| `smsc.db`       | The database.                                       |

## Restore

This path replaces every message, user and setting with those of a backup, on every node of a cluster, and closes the SMPP sessions. The cluster keeps its nodes and its secrets.

| Method | Path              |
| ------ | ----------------- |
| POST   | `/api/v1/restore` |

### Request Body

An archive from [Back Up](#back-up), with `Content-Type: application/gzip`.

### Sample Response

```
{
    "result": {
        "message": "Backup restored successfully"
    }
}
```

### Response Codes

| Code  | Description                                                                                                                       |
| ----- | --------------------------------------------------------------------------------------------------------------------------------- |
| `400` | Not a backup, a checksum does not match, or the backup's schema is newer than this version's.                                     |
| `421` | The node is not the leader. See [Leader-Only Paths](https://docs.ellanetworks.com/smsc/reference/api/cluster/#leader-only-paths). |
| `503` | No leader, or not ready to restore. Retry.                                                                                        |
