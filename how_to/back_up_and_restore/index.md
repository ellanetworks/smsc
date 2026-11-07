# Back Up and Restore

A backup holds message content, user accounts, and the single sign-on client secret. Store and transfer it encrypted.

## Back up

1. Create an API token for an admin: in **Users**, open the user, and in **API Tokens**, click **Create Token**.

1. Download a backup:

   ```
   curl -OJ http://<host-ip>:5010/api/v1/backup \
     -H "Authorization: Bearer ellasmsc_..."
   ```

   This saves `smsc-backup-<time>.tar.gz`. In a cluster, back up from any node.

## Restore

1. Log in as an admin, and keep the token:

   ```
   TOKEN=$(curl -s http://<host-ip>:5010/api/v1/auth/login \
     -H "Content-Type: application/json" \
     -d '{"email": "admin@example.com", "password": "..."}' | jq -r .result.token)
   ```

1. Restore the backup:

   ```
   curl http://<host-ip>:5010/api/v1/restore \
     -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/gzip" \
     --data-binary @smsc-backup-<time>.tar.gz
   ```

   In a cluster, restore on any node.

## Disaster recovery

Use this procedure when a cluster lost a majority of its nodes, or a standalone node was lost.

1. Install Ella SMSC on a new host, as in [Install](https://docs.ellanetworks.com/smsc/how_to/install/index.md). For a cluster, add a `cluster` block to the configuration file.

1. Open `http://<host-ip>:5010` in a browser and initialize it with a temporary user.

1. Restore the backup with that user, as in [Restore](#restore).

1. For a standalone deployment, stop here.

1. Log in with a user from the backup, and add the other nodes, as in [Deploy a High Availability Cluster](https://docs.ellanetworks.com/smsc/how_to/deploy_ha_cluster/index.md), steps 2 to 4. Delete the data of any node that was in the old cluster first:

   ```
   sudo snap stop ella-smsc.smscd
   sudo rm -rf /var/snap/ella-smsc/common/smsc.db* /var/snap/ella-smsc/common/raft
   sudo snap start ella-smsc.smscd
   ```
