---
description: Step-by-step instructions to back up and restore Ella SMSC.
---

# Back Up and Restore

A backup is encrypted with the encryption key, and does not hold it. Keep a copy of the key, apart from the backups.

## Back up the encryption key

=== "Snap"

    Copy `/var/snap/ella-smsc/common/encryption.key` from any node:

    ```shell
    sudo cp /var/snap/ella-smsc/common/encryption.key encryption.key
    ```

=== "Kubernetes"

    Save the `ella-smsc-encryption` Secret:

    ```shell
    kubectl get secret ella-smsc-encryption -n ella -o jsonpath='{.data.key}' | base64 -d > encryption.key
    ```

With an HSM, keep a copy of the key on another HSM.

## Back up

=== "Snap"

    1. Create an API token for an admin: in **Users**, open the user, and in **API Tokens**, click **Create Token**.

    2. Download a backup:

        ```shell
        curl -OJ http://<host-ip>:5010/api/v1/backup \
          -H "Authorization: Bearer ellasmsc_..."
        ```

        This saves `smsc-backup-<time>.tar.gz`. In a cluster, back up from any node.

=== "Kubernetes"

    1. Create an API token for an admin: in **Users**, open the user, and in **API Tokens**, click **Create Token**.

    2. Open the API:

        ```shell
        kubectl port-forward -n ella svc/ella-smsc 5010:5010
        ```

    3. Download a backup:

        ```shell
        curl -OJ http://localhost:5010/api/v1/backup \
          -H "Authorization: Bearer ellasmsc_..."
        ```

        This saves `smsc-backup-<time>.tar.gz`.

## Restore

=== "Snap"

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

        In a cluster, restore on the leader. Another node answers `421` with the leader's address.

=== "Kubernetes"

    1. Open the API of the leader's pod, named on the **Cluster** page, or `ella-smsc-0` for a standalone node:

        ```shell
        kubectl port-forward -n ella pod/ella-smsc-<n> 5010:5010
        ```

    2. Log in as an admin, and keep the token:

        ```shell
        TOKEN=$(curl -s http://localhost:5010/api/v1/auth/login \
          -H "Content-Type: application/json" \
          -d '{"email": "admin@example.com", "password": "..."}' | jq -r .result.token)
        ```

    3. Restore the backup:

        ```shell
        curl http://localhost:5010/api/v1/restore \
          -H "Authorization: Bearer $TOKEN" \
          -H "Content-Type: application/gzip" \
          --data-binary @smsc-backup-<time>.tar.gz
        ```

## Disaster recovery

Use this procedure when a cluster lost a majority of its nodes, or a standalone node was lost.

=== "Snap"

    1. Install Ella SMSC on a new host, as in [Install](install.md). For a cluster, add a `cluster` block to the configuration file. Before starting it, copy the saved encryption key:

        ```shell
        sudo install -m 0600 encryption.key /var/snap/ella-smsc/common/encryption.key
        ```

    2. Open `http://<host-ip>:5010` in a browser and initialize it with a temporary user.
    3. Restore the backup with that user, as in [Restore](#restore).
    4. For a standalone deployment, stop here.
    5. Log in with a user from the backup, and add the other nodes, as in [Deploy a High Availability Cluster](deploy_ha_cluster.md), steps 2 to 4. Delete the data of any node that was in the old cluster first:

        ```shell
        sudo snap stop ella-smsc.smscd
        sudo rm -rf /var/snap/ella-smsc/common/smsc.db* /var/snap/ella-smsc/common/raft /var/snap/ella-smsc/common/encryption.key
        sudo snap start ella-smsc.smscd
        ```

=== "Kubernetes"

    1. Delete the release and its volumes:

        ```shell
        helm uninstall ella-smsc -n ella
        kubectl delete pvc -n ella -l app.kubernetes.io/name=ella-smsc
        ```

    2. If the `ella-smsc-encryption` Secret is gone, create it again from the saved key:

        ```shell
        kubectl create secret generic ella-smsc-encryption -n ella --from-file=key=encryption.key
        ```

    3. Deploy it again, with the same values:

        ```shell
        helm install ella-smsc oci://ghcr.io/ellanetworks/charts/ella-smsc --namespace ella --set replicas=3
        ```

    4. Initialize it with a temporary user, as in [Deploy a High Availability Cluster](deploy_ha_cluster.md), step 2.
    5. Restore the backup with that user, as in [Restore](#restore).
