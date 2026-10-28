---
description: Reference of the network interfaces and ports of Ella SMSC.
---

# Connectivity

| Interface | Peer | Transport | Address | Port |
| --------- | ---- | --------- | ------- | ---- |
| API and UI | Operators, applications, Prometheus | TCP | `api.address` | `api.port` (`5010`) |
| Diameter: S6c | HSS | SCTP | `diameter.address`, or the addresses of `diameter.name` | `diameter.port` (`3868`) |
| Diameter: SGd | MME, SMSF, IP-SM-GW | SCTP | `diameter.address`, or the addresses of `diameter.name` | `diameter.port` (`3868`) |
| Diameter: S6c and SGd, outbound | Configured [Diameter peers](api/diameter.md#create-a-peer): a DEA, DRA or IPX agent | SCTP | The peer's `addresses` | The peer's `port` (`3868`) |

Defaults are in parentheses.

Ella SMSC listens for Diameter connections. It accepts any peer that supports S6c or SGd, and sends S6c requests to the peers in its own realm.

It also connects to the configured Diameter peers, keeps each connection up, and reconnects after 1 to 30 seconds when it drops. It sends a peer the requests of the peer's routes:

- an MT-Forward-Short-Message to an MME, SMSF or IP-SM-GW that is not connected, by the realm of that node, then by the default route for any other realm than its own;
- a routing request (S6c) to the HSS, by the s6c route to its own realm.

A peer forwards the request with its Destination-Host unchanged.
