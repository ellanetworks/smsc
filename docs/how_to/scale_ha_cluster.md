---
description: Add and remove nodes in a running Ella SMSC high-availability cluster.
---

# Scale a High Availability Cluster

This guide walks through adding a node to an existing Ella SMSC high-availability cluster, removing one, and replacing one whose database diverged.

## Prerequisites

- A running HA cluster deployed (see [Deploy a High Availability Cluster](deploy_ha_cluster.md)).

## Add a node

1. Prepare a host with Ella SMSC installed per the [Install](install.md) guide, with a `cluster` block in the configuration file.
2. Open the **Cluster** page on any node in the cluster.
3. Click **Add Node**, click **Create Token**, and copy the token.
4. Start Ella SMSC on the new host.
5. Open the new node's UI in a browser. Click **Join an existing cluster instead**, paste the token, and click **Join**.
6. Connect every Diameter peer to the new node, unless they connect through a Diameter routing agent.
7. On the **Cluster** page, verify the new node appears and is shown as **Healthy**.

## Remove a node

1. Open the **Cluster** page on any node in the cluster.
2. In the node's **Node actions** menu, click **Drain this node**. Wait until its **Drain** column shows **Drained**.
3. In the same menu, click **Remove from cluster**. For a node that is down for good, check **Force remove (skip drain)**.
4. Disconnect the Diameter peers from the removed node.

## Replace a diverged node

A node whose database differs from the leader's shows **Diverged** in the **Healthy** column of the **Cluster** page, and increments `ellasmsc_cluster_state_mismatches_total`.

1. Remove the node, as in [Remove a node](#remove-a-node).
2. On the removed node, stop Ella SMSC and delete its data:

    ```shell
    sudo snap stop ella-smsc.smscd
    sudo rm -rf /var/snap/ella-smsc/common/smsc.db* /var/snap/ella-smsc/common/raft
    sudo snap start ella-smsc.smscd
    ```

3. Add the node back, as in [Add a node](#add-a-node), steps 2, 3, 5 and 6. It gets a new **Diameter Host**.
4. On the **Cluster** page, verify the node is shown as **Healthy**.
