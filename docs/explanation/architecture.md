---
description: How Ella SMSC is built and where it sits in a 4G or 5G network.
---

# Architecture

A Short Message Service Centre (SMSC) stores the short messages phones send, and forwards them to their recipients. Ella SMSC is a single process, with one SQLite database, one UI, one API and, when configured, an SMPP listener.

## Where Ella SMSC sits

Ella SMSC has no radio access and stores no subscribers. It relies on the core network, over Diameter:

- **The HSS**, over S6c, says where a recipient is served, and alerts the SMSC when an unreachable recipient is back.
- **The MME or SMSF**, over SGd, carries messages between the phones and the SMSC, in NAS signalling.
- **An IP-SM-GW**, over SGd, carries them for phones registered for SMS over IP.

Ella Core provides the HSS, the MME and the SMSF. For how it works with an SMSC, see Ella Core's [SMS Integration](https://docs.ellanetworks.com/core/explanation/sms_integration/).

Ella SMSC's Diameter host and realm derive from the operator's MCC and MNC: `smsc.node.epc.mnc<MNC>.mcc<MCC>.3gppnetwork.org` in the realm `epc.mnc<MNC>.mcc<MCC>.3gppnetwork.org`. Its peers connect to it, and it asks the HSSs of that realm for routing.

## Reaching other networks

A subscriber roaming on another network is served by a visited MME or SMSF that never connects to Ella SMSC. Ella SMSC reaches it through a Diameter agent it connects to, such as the edge agent of the operator or of an IPX provider. An HSS behind a DRA is reached the same way.

Each agent has routes: the realms it reaches. A default route takes every other realm, except Ella SMSC's own, so a home MME that is briefly disconnected is retried rather than sent out to the IPX. An agent is asked only for the requests of its routes: an HSS added as an agent needs an S6c route to Ella SMSC's realm.

When an agent fails with a short message in flight, Ella SMSC sends it through the next one, and the subscriber may receive it twice.

## Receiving

A phone's message reaches Ella SMSC when the SMSC number on its SIM card is Ella SMSC's service centre address. The MME, SMSF or IP-SM-GW gives the phone's MSISDN, which is the sender. Ella SMSC converts the recipient to an international number with the operator's numbering plan, stores the message, and acknowledges it.

Applications send messages to phones with the [Messages API](../reference/api/messages.md), or over [SMPP](#esmes-over-smpp).

## Delivering

For each message, Ella SMSC asks the HSS for the serving nodes of the recipient, then tries them in turn until one takes the message. Messages to a phone go one at a time, in order. When more wait, the serving node that took one takes the next, without another routing lookup.

Each lookup and delivery is recorded as an attempt, shown in the UI and the API.

## When a phone is unreachable

When no serving node takes a message, Ella SMSC makes sure the HSS records that a message waits. The message and the recipient's other messages wait for a retry, after the [retry intervals](../reference/api/delivery.md). When the phone can receive messages again, the HSS alerts Ella SMSC, which delivers them right away.

A message ends:

- **delivered**, when a serving node takes it;
- **failed**, when the recipient is unknown, not allowed SMS, or has no serving node that supports SGd, when the SIM refuses a (U)SIM data download message, or when an attempt fails and the message is single-shot or retries are off;
- **expired**, when its validity period ends first;
- **replaced**, when a later message from the same sender replaces it. A message from the Messages API or an ESME is replaced only by one from the same user or ESME.

## Status reports

When a phone asks for status reports, Ella SMSC returns one to it when the message ends. It delivers the report as it does a message.

## ESMEs over SMPP

Platforms built for SMSCs, such as OTA platforms, connect as External Short Message Entities (ESMEs), over SMPP 3.4:

```
OTA platform (ESME) ──SMPP over TCP or TLS, 2775──▶ SMPP listener ──▶ submission ──▶ database ──▶ delivery ──▶ HSS, MME, SMSF
        ▲                                                                                       │
        └──────────────── deliver_sm: receipts, SIM answers ◀── notifications ◀─────────────────┘
```

- **The SMPP listener** accepts the ESMEs' connections, checks each bind against the ESME accounts, and keeps the sessions. An ESME account acts as a user: the messages it submits are that user's, and the user's role decides what it may do.
- **Submission.** Each `submit_sm` becomes one short message, through the same path as the [Messages API](../reference/api/messages.md), so the same parameters give the same message. Ella SMSC answers once the message is stored, then delivers it as any other.
- **Notifications.** When a message from an ESME ends, Ella SMSC queues the delivery receipt and the SIM's answer that the ESME asked for. It sends them as `deliver_sm` to a receiving session of that ESME, the answer first, and keeps them until the ESME acknowledges them, binds again, or they expire. They survive a restart.

See the [SMPP reference](../reference/smpp.md).

## Binary messages

Applications can send binary messages, such as the (U)SIM data download messages that an OTA platform sends to update SIM cards: the phone passes them to the SIM instead of showing them. Ella SMSC delivers their parameters and user data unchanged, and keeps the phone's answer to each delivery attempt, which can carry the SIM's response.
