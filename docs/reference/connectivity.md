---
description: Reference of the network interfaces and ports of Ella SMSC.
---

# Connectivity

| Interface | Peer | Transport | Address | Port |
| --------- | ---- | --------- | ------- | ---- |
| API and UI | Operators, applications, Prometheus | TCP | `api.address` | `api.port` (`5010`) |
| Diameter: S6c | HSS | SCTP | `diameter.address`, or the addresses of `diameter.name` | `diameter.port` (`3868`) |
| Diameter: SGd | MME, SMSF, IP-SM-GW | SCTP | `diameter.address`, or the addresses of `diameter.name` | `diameter.port` (`3868`) |
| Diameter: S6c and SGd, outbound | [Configured peers](api/diameter.md#create-a-peer): DEA, DRA | SCTP | The peer's `addresses` | The peer's `port` (`3868`) |

Defaults are in parentheses.

Ella SMSC accepts any Diameter peer that supports S6c or SGd. It connects to the configured peers, and reconnects after 1 to 30 seconds.
