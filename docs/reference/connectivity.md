---
description: Reference of the network interfaces and ports of Ella SMSC.
---

# Connectivity

| Interface | Peer | Transport | Address | Port |
| --------- | ---- | --------- | ------- | ---- |
| API and UI | Operators, applications, Prometheus | TCP | `api.address` | `api.port` (`5010`) |
| Diameter: S6c | HSS | SCTP | `diameter.address`, or the addresses of `diameter.name` | `diameter.port` (`3868`) |
| Diameter: SGd and Gdd | MME, SMSF, IP-SM-GW over SGd; SGSN over Gdd | SCTP | `diameter.address`, or the addresses of `diameter.name` | `diameter.port` (`3868`) |
| Diameter: S6c and SGd, outbound | [Configured peers](api/diameter.md#create-a-peer): DEA, DRA | SCTP | The peer's `addresses` | The peer's `port` (`3868`) |
| [SMPP 3.4](smpp.md) | ESMEs, such as OTA platforms | TCP, or TLS with `smpp.tls` | `smpp.address` | `smpp.port` (`2775`) |

Defaults are in parentheses. SMPP listens only when the configuration file has an `smpp` block.

Ella SMSC accepts any Diameter peer that supports S6c or SGd. It connects to the configured peers, and reconnects after 1 to 30 seconds.

## Diameter identity

| Item | Value |
| ---- | ----- |
| Host | `smsc.node.epc.mnc<MNC>.mcc<MCC>.3gppnetwork.org` |
| Realm | `epc.mnc<MNC>.mcc<MCC>.3gppnetwork.org` |

`<MCC>` and `<MNC>` are the operator's, with a 2-digit MNC padded to 3.
