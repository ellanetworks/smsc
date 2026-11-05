---
description: Explanation of how high availability works in Ella SMSC.
---

# High Availability

A high availability (HA) cluster is several Ella SMSC nodes acting as one service centre, with one service centre address. Every node accepts short messages from phones over Diameter, from ESMEs over SMPP, and from users over the API and UI. Messages, users and settings are shared by every node.

The cluster uses the [Raft](https://raft.github.io/) consensus algorithm. A message is acknowledged to its sender only once a majority of the nodes have stored it, so an acknowledged message survives the loss of any minority of them.

## Failure tolerance

A cluster keeps working as long as a majority of its voting nodes are up.

| Voters | Failures tolerated |
| ------ | ------------------ |
| 1      | 0                  |
| 3      | 1                  |
| 5      | 2                  |

Ella SMSC keeps an odd number of voters, at most 5. Other nodes are standbys, promoted when a voter fails. We recommend 3 or 5 nodes.

## Diameter peers

Each node has its own Diameter host, fixed by its node ID, and they share the realm and the service centre address. Any node accepts MO-Forward-Short-Message and Alert-Service-Centre, and the leader delivers. So every Diameter peer, such as Ella Core, the HSS or Ella IMS, connects to every node, or every node and every peer connect through a Diameter routing agent.

## ESMEs

An ESME binds to any node. Its limit of 4 sessions applies across the cluster. Delivery receipts and SIM answers go out through its receiver sessions, on whichever node they are bound.

## Failover

When the leader fails, the other nodes elect a new one within a few seconds. Until then, Ella SMSC answers new messages with a temporary error, so the sender retries, and delivers nothing.

The new leader resumes delivery. It waits up to 35 seconds before delivering to a phone that the old leader was delivering to, so that the phone never receives the same message twice.

## Draining

Draining a node prepares it for an upgrade or removal: it closes its SMPP sessions, so that its ESMEs bind to another node, and hands over the leadership. It keeps its Diameter connections and its vote.

## Upgrades

Upgrade one node at a time, one version at a time. The cluster moves to the new version's database once every node runs it. Downgrades are not supported.

## Disaster recovery

If a majority of the voters are lost for good, the cluster stops accepting messages. Restore the last backup on a new cluster. See [Back Up and Restore](../how_to/back_up_and_restore.md).

## Further reading

- [Deploy a High Availability Cluster](../how_to/deploy_ha_cluster.md)
- [Scale a High Availability Cluster](../how_to/scale_ha_cluster.md)
- [Perform a Rolling Upgrade](../how_to/rolling_upgrade.md)
- [Cluster API](../reference/api/cluster.md)
