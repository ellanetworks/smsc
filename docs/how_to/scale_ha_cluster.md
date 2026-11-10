---
description: Add and remove nodes in a running Ella SMSC high-availability cluster.
---

# Scale a High Availability Cluster

This guide walks through adding a node to an existing Ella SMSC high-availability cluster, and removing one.

## Prerequisites

- A running HA cluster deployed (see [Deploy a High Availability Cluster](deploy_ha_cluster.md)).

## Add a node

=== "Snap"

    1. Prepare a host with Ella SMSC installed per the [Install](install.md) guide, with a `cluster` block in the configuration file.
    2. Open the **Cluster** page on any node in the cluster.
    3. Click **Add Node**, click **Create Token**, and copy the token.
    4. Start Ella SMSC on the new host.
    5. Open the new node's UI in a browser. Click **Join an existing cluster instead**, paste the token, and click **Join**.
    6. Connect every Diameter peer to the new node, unless they connect through a Diameter routing agent.
    7. On the leader's **Cluster** page, verify the new node appears and is shown as **Healthy**.

=== "Kubernetes"

    1. Add a Kubernetes node for the new pod.
    2. Add a pod:

        ```shell
        kubectl scale statefulset ella-smsc -n ella --replicas=4
        ```

    3. Connect every Diameter peer to the new pod.
    4. On the leader's **Cluster** page, verify the new node appears and is shown as **Healthy**.

## Remove a node

=== "Snap"

    1. Open the leader's **Cluster** page.
    2. In the node's **Node actions** menu, click **Drain this node**. Wait until its **Drain** column shows **Drained**.
    3. In the same menu, click **Remove from cluster**. For a node that is down for good, check **Force remove (skip drain)**.
    4. Disconnect the Diameter peers from the removed node.

=== "Kubernetes"

    1. Open the leader's **Cluster** page.
    2. In the **Node actions** menu of the node with the highest pod number, click **Drain this node**. Wait until its **Drain** column shows **Drained**.
    3. In the same menu, click **Remove from cluster**.
    4. Remove its pod and volume:

        ```shell
        kubectl scale statefulset ella-smsc -n ella --replicas=3
        kubectl delete pvc data-ella-smsc-3 -n ella
        ```

    5. Disconnect the Diameter peers from the removed node.
