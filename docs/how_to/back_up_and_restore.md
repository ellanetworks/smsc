---
description: Step-by-step instructions to back up and restore Ella SMSC.
---

# Back Up and Restore

Ella SMSC backs up and restores over its [API](../reference/api/backup.md), while it runs. A backup holds message content, user accounts, and the single sign-on client secret. Store and transfer it encrypted.

## Back up

1. Create an API token for an admin: in **Users**, open the user, and in **API Tokens**, click **Create Token**.

2. Download a backup:

    ```shell
    curl -OJ http://<host-ip>:5010/api/v1/backup \
      -H "Authorization: Bearer ellasmsc_..."
    ```

    This saves `smsc-backup-<time>.tar.gz`. To back up on a schedule, run this command from a cron job, and keep as many backups as you need.

## Restore

1. Log in as an admin, and keep the token:

    ```shell
    TOKEN=$(curl -s http://<host-ip>:5010/api/v1/auth/login \
      -H "Content-Type: application/json" \
      -d '{"email": "admin@example.com", "password": "..."}' | jq -r .result.token)
    ```

2. Restore the backup:

    ```shell
    curl http://<host-ip>:5010/api/v1/restore \
      -H "Authorization: Bearer $TOKEN" \
      -H "Content-Type: application/gzip" \
      --data-binary @smsc-backup-<time>.tar.gz
    ```

Every message, user and setting is now the backup's. Messages received after the backup was taken are gone. Users log in again if their sessions were not in the backup, and ESMEs bind again.
