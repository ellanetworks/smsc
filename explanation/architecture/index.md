# Architecture

Ella SMSC is a Short Message Service Centre (SMSC) for private 4G and 5G networks. It stores the short messages sent to the network's subscribers, and delivers them when their phones can receive them.

Ella SMSC is a single application, with an embedded database, an embedded web UI, and an HTTP API.

## How phones send and receive SMS

A phone exchanges SMS with Ella SMSC in one of two ways: over NAS, through the core network, or over IP, through the IMS.

### SMS over NAS

SMS over NAS

With SMS over NAS, the message travels inside the signalling the phone already exchanges with the core network to attach and stay registered. It needs no IMS.

### SMS over IP

SMS over IP

With SMS over IP, the phone sends and receives its messages as SIP messages through the IMS. An IP Short Message Gateway (IP-SM-GW) in the IMS converts between SIP and short messages, and connects to Ella SMSC. [Ella IMS](https://docs.ellanetworks.com/ims/explanation/sms_over_ip/) includes an IP-SM-GW.

## Store and forward

1. Ella SMSC stores the message, and acknowledges it to the sender.
1. It asks the HSS where the recipient is served: an MME, an SMSF or an IP-SM-GW.
1. It sends the message there.
1. If the phone is unreachable, the HSS records that a message waits, and alerts Ella SMSC when the phone is back. Ella SMSC also retries after the [retry intervals](https://docs.ellanetworks.com/smsc/reference/api/delivery/index.md).

Messages to a phone are delivered one at a time, in order.

A message ends delivered, failed, expired at the end of its validity period, or replaced by a later message. See [Statuses](https://docs.ellanetworks.com/smsc/reference/api/messages/#statuses).

## Messages from applications

Besides phones, applications can send messages to subscribers, for example notifications, one-time codes or updates to SIM cards. Ella SMSC offers them two interfaces:

- The [Messages API](https://docs.ellanetworks.com/smsc/reference/api/messages/index.md): Ella SMSC's HTTP API, for applications you write or script.
- [SMPP](https://docs.ellanetworks.com/smsc/reference/smpp/index.md): the standard SMSC protocol, for software that already speaks it, such as OTA platforms.

OTA SIM delivery with SMPP

## 3GPP roles

Ella SMSC integrates all three 3GPP functions:

| Function           | Role                                                                                | Diameter procedures (TS 29.338)                                               |
| ------------------ | ----------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| SC, Service Centre | Stores each message until it is delivered or expires.                               |                                                                               |
| SMS-IWMSC          | Receives the messages phones send (mobile originated, MO), and the HSS's alerts.    | MO-Forward-Short-Message, Alert-Service-Centre                                |
| SMS-GMSC           | Finds where a phone is served, and delivers messages to it (mobile terminated, MT). | Send-Routing-Info-for-SM, MT-Forward-Short-Message, Report-SM-Delivery-Status |
