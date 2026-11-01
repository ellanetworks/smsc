# Back Up and Restore

Ella SMSC keeps all its data, messages, users and settings, in one database file: `db.path` in the [configuration file](https://docs.ellanetworks.com/smsc/reference/config_file/index.md). The commands below use the snap's path, `/var/snap/ella-smsc/common/smsc.db`.

The backup holds message content and user accounts. Store and transfer it encrypted.

## Back up

1. Stop Ella SMSC:

   ```
   sudo snap stop ella-smsc.smscd
   ```

1. Copy the database:

   ```
   sudo cp /var/snap/ella-smsc/common/smsc.db smsc-backup.db
   ```

1. Start Ella SMSC:

   ```
   sudo snap start ella-smsc.smscd
   ```

## Restore

1. Stop Ella SMSC:

   ```
   sudo snap stop ella-smsc.smscd
   ```

1. Replace the database with the backup, and remove the files that go with the old one:

   ```
   sudo cp smsc-backup.db /var/snap/ella-smsc/common/smsc.db
   sudo rm -f /var/snap/ella-smsc/common/smsc.db-wal /var/snap/ella-smsc/common/smsc.db-shm
   ```

1. Start Ella SMSC:

   ```
   sudo snap start ella-smsc.smscd
   ```
