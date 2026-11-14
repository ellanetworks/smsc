# Upgrade

## Prerequisites

- Ella SMSC installed with [Install](https://docs.ellanetworks.com/smsc/how_to/install/index.md) or [Deploy a High Availability Cluster](https://docs.ellanetworks.com/smsc/how_to/deploy_ha_cluster/index.md).
- The target release of `ella-smsc` available.

## 1. Upgrade

Refresh the snap:

```
sudo snap refresh ella-smsc
```

In the directory of `docker-compose.yaml`, pull the new image and restart the container:

```
docker compose pull
docker compose up -d
```

1. Set the new image:

   ```
   kubectl set image statefulset/ella-smsc -n ella smsc=ghcr.io/ellanetworks/ella-smsc:<version>
   ```

1. Wait for the rollout:

   ```
   kubectl rollout status statefulset/ella-smsc -n ella
   ```

Repeat these steps for each node:

1. Open the leader's **Cluster** page.

1. In the node's **Node actions** menu, click **Drain this node**. Wait until its **Drain** column shows **Drained**.

1. On that host, refresh the snap:

   ```
   sudo snap refresh ella-smsc
   ```

1. On the leader's **Cluster** page, wait for the node to return to **Healthy**.

1. In the node's **Node actions** menu, click **Resume this node**. Wait until its **Drain** column shows **Active**.

1. Set the new image:

   ```
   kubectl set image statefulset/ella-smsc -n ella smsc=ghcr.io/ellanetworks/ella-smsc:<version>
   ```

1. Wait for the rollout:

   ```
   kubectl rollout status statefulset/ella-smsc -n ella
   ```

## 2. Verify

Check the running version in the UI's top bar.

On the leader's **Cluster** page, confirm:

- Every node's **Version** column shows the target release.
- Every node is **Healthy** and its **Drain** column shows **Active**.
