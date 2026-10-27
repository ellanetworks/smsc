---
description: How Ella SMSC is built and where it sits in a 4G or 5G network.
---

# Architecture

A Short Message Service Centre (SMSC) stores the short messages phones send, and forwards them to their recipients. Ella SMSC is a single process, with one SQLite database, one UI and one API.

## Where Ella SMSC sits

Ella SMSC has no radio access and stores no subscribers. It relies on the core network, over Diameter:

- **The HSS**, over S6c, says where a recipient is served, and alerts the SMSC when an unreachable recipient is back.
- **The MME or SMSF**, over SGd, carries messages between the phones and the SMSC, in NAS signalling.
- **An IP-SM-GW**, over SGd, carries them for phones registered for SMS over IP.

Ella Core provides the HSS, the MME and the SMSF. For how it works with an SMSC, see Ella Core's [SMS Integration](https://docs.ellanetworks.com/core/explanation/sms_integration/).

Ella SMSC's Diameter host and realm derive from the operator's MCC and MNC: `smsc.node.epc.mnc<MNC>.mcc<MCC>.3gppnetwork.org` in the realm `epc.mnc<MNC>.mcc<MCC>.3gppnetwork.org`. Its peers connect to it, and it asks the HSSs of that realm for routing.

## Receiving

A phone's message reaches Ella SMSC when the SMSC number on its SIM card is Ella SMSC's service centre address. The MME, SMSF or IP-SM-GW gives the phone's MSISDN, which is the sender. Ella SMSC converts the recipient to an international number with the operator's numbering plan, stores the message, and acknowledges it.

Applications send messages to phones with the [Messages API](../reference/api/messages.md).

## Delivering

For each message, Ella SMSC asks the HSS for the serving nodes of the recipient, then tries them in turn until one takes the message. Messages to a phone go one at a time, in order. When more wait, the serving node that took one takes the next, without another routing lookup.

Each lookup and delivery is recorded as an attempt, shown in the UI and the API.

## When a phone is unreachable

When no serving node takes a message, Ella SMSC makes sure the HSS records that a message waits. The message and the recipient's other messages wait for a retry, after the [retry intervals](../reference/api/delivery.md). When the phone can receive messages again, the HSS alerts Ella SMSC, which delivers them right away.

A message ends:

- **delivered**, when a serving node takes it;
- **failed**, when the recipient is unknown, not allowed SMS, or has no serving node that supports SGd, or when an attempt fails and the message is single-shot or retries are off;
- **expired**, when its validity period ends first;
- **replaced**, when a later message from the same phone replaces it.

## Status reports

When a phone asks for status reports, Ella SMSC returns one to it when the message ends. It delivers the report as it does a message.
