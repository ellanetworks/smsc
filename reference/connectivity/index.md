# Connectivity

| Interface                                                              | Peer                                                                                                   | Transport                   | Address                                                 | Port                       |
| ---------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ | --------------------------- | ------------------------------------------------------- | -------------------------- |
| API and UI                                                             | Operators, applications, Prometheus                                                                    | TCP                         | `api.address`                                           | `api.port` (`5010`)        |
| Diameter: S6c                                                          | HSS                                                                                                    | SCTP                        | `diameter.address`, or the addresses of `diameter.name` | `diameter.port` (`3868`)   |
| Diameter: SGd and Gdd                                                  | MME, SMSF, IP-SM-GW over SGd; SGSN over Gdd                                                            | SCTP                        | `diameter.address`, or the addresses of `diameter.name` | `diameter.port` (`3868`)   |
| Diameter: S6c and SGd, outbound                                        | [Configured peers](https://docs.ellanetworks.com/smsc/reference/api/diameter/#create-a-peer): DEA, DRA | SCTP                        | The peer's `addresses`                                  | The peer's `port` (`3868`) |
| [SMPP 3.4](https://docs.ellanetworks.com/smsc/reference/smpp/index.md) | ESMEs, such as OTA platforms                                                                           | TCP, or TLS with `smpp.tls` | `smpp.address`                                          | `smpp.port` (`2775`)       |
| Cluster                                                                | The other Ella SMSC nodes                                                                              | TCP, mutual TLS 1.3         | `cluster.address`, advertised as `cluster.advertise`    | `cluster.port` (`7000`)    |

Defaults are in parentheses. SMPP listens only when the configuration file has an `smpp` block, and the cluster port only when it has a `cluster` block.

Ella SMSC accepts any Diameter peer that supports S6c or SGd. It connects to the configured peers, and reconnects after 1 to 30 seconds. In a cluster, every node connects to the configured peers, and every other peer connects to every node.

## Diameter identity

| Item  | Value                                                  |
| ----- | ------------------------------------------------------ |
| Host  | `smsc-<id>.node.epc.mnc<MNC>.mcc<MCC>.3gppnetwork.org` |
| Realm | `epc.mnc<MNC>.mcc<MCC>.3gppnetwork.org`                |

`<MCC>` and `<MNC>` are the operator's, with a 2-digit MNC padded to 3. `<id>` is the last 12 hex characters of the node ID. The host does not change for the life of the node.
