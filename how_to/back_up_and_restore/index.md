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

1. Create an API token for an admin: in **Users**, open the user, and in **API Tokens**, click **Create Token**.

1. Open the API:

   ```
   kubectl port-forward -n ella svc/ella-smsc 5010:5010
   ```

1. Download a backup:

   ```
   curl -OJ http://localhost:5010/api/v1/backup \
     -H "Authorization: Bearer ellasmsc_..."
   ```

   This saves `smsc-backup-<time>.tar.gz`.

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

   In a cluster, restore on the leader. Another node answers `421` with the leader's address.

1. Open the API of the leader's pod, named on the **Cluster** page, or `ella-smsc-0` for a standalone node:

   ```
   kubectl port-forward -n ella pod/ella-smsc-<n> 5010:5010
   ```

1. Log in as an admin, and keep the token:

   ```
   TOKEN=$(curl -s http://localhost:5010/api/v1/auth/login \
     -H "Content-Type: application/json" \
     -d '{"email": "admin@example.com", "password": "..."}' | jq -r .result.token)
   ```

1. Restore the backup:

   ```
   curl http://localhost:5010/api/v1/restore \
     -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/gzip" \
     --data-binary @smsc-backup-<time>.tar.gz
   ```

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

1. Delete the cluster and its volumes:

   ```
   kubectl delete -k smsc/k8s/ha
   kubectl delete pvc -n ella -l app.kubernetes.io/name=ella-smsc
   ```

1. Deploy it again:

   ```
   kubectl apply -k smsc/k8s/ha
   ```

1. Initialize it with a temporary user, as in [Deploy a High Availability Cluster](https://docs.ellanetworks.com/smsc/how_to/deploy_ha_cluster/index.md), step 2.

1. Restore the backup with that user, as in [Restore](#restore).
