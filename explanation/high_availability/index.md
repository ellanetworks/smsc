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

Each node has its own Diameter host, and they share the realm and the service centre address. Any node receives messages, and the leader delivers them. Connect each Diameter peer to every node, or connect them all through a Diameter routing agent.

## ESMEs

An ESME binds to any node. Its limit of 4 sessions applies across the cluster. Delivery receipts and SIM answers go out through its receiver sessions, on whichever node they are bound.

## Failover

When the leader fails, the other nodes elect a new one within a few seconds. Until then, Ella SMSC answers new messages with a temporary error, so the sender retries, and delivers nothing.

## Draining

Draining a node prepares it for an upgrade or removal: it closes its SMPP sessions, and hands over the leadership. It keeps its Diameter connections and its vote.

## Upgrades

Upgrade one node at a time, one version at a time. The cluster moves to the new version's database once every node runs it. Downgrades are not supported.

## Disaster recovery

If a majority of the voters are lost for good, the cluster stops accepting messages. Restore the last backup on a new cluster. See [Back Up and Restore](https://docs.ellanetworks.com/smsc/how_to/back_up_and_restore/index.md).

## Further reading

- [Deploy a High Availability Cluster](https://docs.ellanetworks.com/smsc/how_to/deploy_ha_cluster/index.md)
- [Scale a High Availability Cluster](https://docs.ellanetworks.com/smsc/how_to/scale_ha_cluster/index.md)
- [Perform a Rolling Upgrade](https://docs.ellanetworks.com/smsc/how_to/rolling_upgrade/index.md)
- [Cluster API](https://docs.ellanetworks.com/smsc/reference/api/cluster/index.md)
