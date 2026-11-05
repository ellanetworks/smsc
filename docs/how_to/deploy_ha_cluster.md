---
description: Step-by-step instructions to deploy Ella SMSC as a high-availability cluster.
---

# Deploy a High Availability Cluster

Ella SMSC can be deployed as a high-availability cluster to provide redundancy and failover capabilities. See [High Availability](../explanation/high_availability.md) for more information.

## Prerequisites

- Three hosts meeting the standard [system requirements](../reference/system_reqs.md).
- Ella SMSC installed on each host via the [Install](install.md) guide, with a `cluster` block in the [configuration file](../reference/config_file.md).

## 1. Start node 1

Start node 1, open a browser at the API address and initialize it normally.

## 2. Create a join token for node 2

Still in node 1, open the **Cluster** page, click **Add Node**, click **Create Token**, then copy the token.

## 3. Start node 2

Start node 2, open a browser at the API address and click **Join an existing cluster instead**. Paste the token and click **Join**.

## 4. Add node 3

Repeat steps 2 and 3 for node 3.

## 5. Connect the Diameter peers

Connect every Diameter peer, such as Ella Core, the HSS or Ella IMS, to every node, with each node's address and Diameter host. The **Cluster** page lists each node's **Diameter Host**. Or connect the nodes and the peers through a Diameter routing agent, as in [Connect through Diameter Agents](connect_through_diameter_agents.md).

## 6. Verify

On the **Cluster** page, all three nodes appear as **Healthy**.
