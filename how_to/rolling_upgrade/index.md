# Perform a Rolling Upgrade

This guide walks through upgrading every node in a running Ella SMSC high-availability cluster, one at a time, without taking the cluster offline.

## Prerequisites

- A running cluster deployed via [Deploy a High Availability Cluster](https://docs.ellanetworks.com/smsc/how_to/deploy_ha_cluster/index.md).
- The next version of `ella-smsc` available.

## Upgrade one node

Repeat these steps for each node.

1. Open the **Cluster** page on any healthy node other than the one you are about to upgrade.

1. In the node's **Node actions** menu, click **Drain this node**. Wait until its **Drain** column shows **Drained**.

1. On that host, refresh the snap:

   ```
   sudo snap refresh ella-smsc
   ```

1. On the **Cluster** page, wait for the node to return to **Healthy**.

1. In the node's **Node actions** menu, click **Resume this node**. Wait until its **Drain** column shows **Active**.

1. Move to the next node.

## Verify the upgrade

After every node has been refreshed, open the **Cluster** page and confirm:

- Every node's **Version** column shows the target release.
- Every node is **Healthy** and its **Drain** column shows **Active**.
