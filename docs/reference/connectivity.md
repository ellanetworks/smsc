---
description: Reference of the network interfaces and ports of Ella SMSC.
---

# Connectivity

| Interface | Peer | Transport | Address | Port |
| --------- | ---- | --------- | ------- | ---- |
| API and UI | Operators, applications, Prometheus | TCP | `api.address` | `api.port` (`5010`) |
| Diameter: S6c | HSS | SCTP | `diameter.address`, or the addresses of `diameter.name` | `diameter.port` (`3868`) |
| Diameter: SGd | MME, SMSF, IP-SM-GW | SCTP | `diameter.address`, or the addresses of `diameter.name` | `diameter.port` (`3868`) |

Defaults are in parentheses.

Ella SMSC listens for Diameter connections and does not open any: each peer connects to it. It accepts any peer that supports S6c or SGd, and sends S6c requests to the peers in its own realm.
