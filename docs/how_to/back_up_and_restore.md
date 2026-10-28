---
description: Step-by-step instructions to back up and restore Ella SMSC.
---

# Back Up and Restore

Ella SMSC keeps all its data, messages, users and settings, in one database file: `db.path` in the [configuration file](../reference/config_file.md). The commands below use the snap's path, `/var/snap/ella-smsc/common/smsc.db`.

The backup holds message content and user accounts. Store and transfer it encrypted.

## Back up

1. Stop Ella SMSC:

    ```shell
    sudo snap stop ella-smsc.smscd
    ```

2. Copy the database:

    ```shell
    sudo cp /var/snap/ella-smsc/common/smsc.db smsc-backup.db
    ```

3. Start Ella SMSC:

    ```shell
    sudo snap start ella-smsc.smscd
    ```

## Restore

1. Stop Ella SMSC:

    ```shell
    sudo snap stop ella-smsc.smscd
    ```

2. Replace the database with the backup, and remove the files that go with the old one:

    ```shell
    sudo cp smsc-backup.db /var/snap/ella-smsc/common/smsc.db
    sudo rm -f /var/snap/ella-smsc/common/smsc.db-wal /var/snap/ella-smsc/common/smsc.db-shm
    ```

3. Start Ella SMSC:

    ```shell
    sudo snap start ella-smsc.smscd
    ```
