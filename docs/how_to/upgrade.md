---
description: Upgrade Ella SMSC to the next release, on a standalone node or a high-availability cluster.
---

# Upgrade

## Prerequisites

- Ella SMSC installed with [Install](install.md) or [Deploy a High Availability Cluster](deploy_ha_cluster.md).
- The target release of `ella-smsc` available.

## 1. Upgrade

=== "Standalone"

    === "Snap"

        Refresh the snap:

        ```shell
        sudo snap refresh ella-smsc
        ```

    === "Docker"

        In the directory of `docker-compose.yaml`, pull the new image and restart the container:

        ```shell
        docker compose pull
        docker compose up -d
        ```

    === "Kubernetes"

        1. Set the new image:

            ```shell
            kubectl set image statefulset/ella-smsc -n ella smsc=ghcr.io/ellanetworks/ella-smsc:<version>
            ```

        2. Wait for the rollout:

            ```shell
            kubectl rollout status statefulset/ella-smsc -n ella
            ```

=== "High Availability"

    === "Snap"

        Repeat these steps for each node:

        1. Open the leader's **Cluster** page.
        2. In the node's **Node actions** menu, click **Drain this node**. Wait until its **Drain** column shows **Drained**.
        3. On that host, refresh the snap:

            ```shell
            sudo snap refresh ella-smsc
            ```

        4. On the leader's **Cluster** page, wait for the node to return to **Healthy**.
        5. In the node's **Node actions** menu, click **Resume this node**. Wait until its **Drain** column shows **Active**.

    === "Kubernetes"

        1. Set the new image:

            ```shell
            kubectl set image statefulset/ella-smsc -n ella smsc=ghcr.io/ellanetworks/ella-smsc:<version>
            ```

        2. Wait for the rollout:

            ```shell
            kubectl rollout status statefulset/ella-smsc -n ella
            ```

## 2. Verify

=== "Standalone"

    Check the running version in the UI's top bar.

=== "High Availability"

    On the leader's **Cluster** page, confirm:

    - Every node's **Version** column shows the target release.
    - Every node is **Healthy** and its **Drain** column shows **Active**.
