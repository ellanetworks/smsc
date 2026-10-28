---
description: Step-by-step instructions to reach roaming subscribers through an IPX Diameter agent, and an HSS through a DRA.
---

# Connect through Diameter Agents

This guide connects Ella SMSC to Diameter agents that it opens the connection to:

- the edge agent (DEA) of the operator or of an IPX provider, to deliver short messages to subscribers roaming on visited networks;
- a Diameter routing agent (DRA) in front of the HSS, when the HSS does not connect to Ella SMSC itself.

The HSSs, MMEs and SMSFs that connect to Ella SMSC need none of this: see [Integrate with a Third-Party Core](integrate_with_third_party_core.md).

## Prerequisites

- **Ella SMSC**: integrated with its core, as in [Integrate with a Third-Party Core](integrate_with_third_party_core.md).
- **An API token** of a Network Manager or an Admin: see [Send Messages from an Application](send_messages_from_an_application.md).
- **The agent**: its Diameter host and its SCTP addresses. Its operator adds Ella SMSC's host, realm and Diameter address as a peer.
- **The visited networks**: they deliver short messages in the MME (SMS in MME, advertised as "SMS over NAS (SGd)" in IR.21), or in a 5G SMSF that the HSS names with its Diameter host and realm. Ella SMSC does not deliver to an MSC.
- **The HSS**: registers the visited MME or SMSF for SMS, and names it, with its realm, in its routing answers.

The examples use `dea1.ipx.example.net` at `192.0.2.10` and `198.51.100.10` as the edge agent, `dra1.example.net` at `10.3.0.20` as the DRA, and `epc.mnc001.mcc001.3gppnetwork.org` as the realm of Ella SMSC.

## 1. Add the edge agent

Send the short messages to every visited network through the edge agent, with a default `sgd` route:

```shell
curl https://<host-ip>:5010/api/v1/diameter/peers \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "host": "dea1.ipx.example.net",
    "addresses": ["192.0.2.10", "198.51.100.10"],
    "routes": [{ "default": true, "application": "sgd" }]
  }'
```

List every SCTP address of the agent: it is refused when it connects from another one.

The default route never takes Ella SMSC's own realm, so a home MME that is not connected is never reached through the IPX. To reach the nodes of a realm through another agent, give that agent a route to the realm, e.g. `{ "realm": "epc.mnc002.mcc208.3gppnetwork.org", "application": "sgd" }`: a route to a realm comes before the default route.

To add a second agent for failover, add it with the same routes and `"priority": 1`. With the same priority, the two agents share the messages.

## 2. Add the DRA in front of the HSS

Only when the HSS does not connect to Ella SMSC: send the routing requests to the DRA, with an `s6c` route to Ella SMSC's own realm:

```shell
curl https://<host-ip>:5010/api/v1/diameter/peers \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "host": "dra1.example.net",
    "addresses": ["10.3.0.20"],
    "routes": [{ "realm": "epc.mnc001.mcc001.3gppnetwork.org", "application": "s6c" }]
  }'
```

An agent can carry both: give the edge agent the `s6c` route too, when it reaches the HSS.

A configured peer serves only its routes. If you add the HSS itself as a peer, give it the `s6c` route: without it, Ella SMSC logs a warning when it connects, and does not ask it for routing.

## 3. Verify

In the Ella SMSC UI, go to **Diameter**. Each agent shows **open**. The DRA shows **HSS** set to **yes**. Or, with the API:

```shell
curl https://<host-ip>:5010/api/v1/diameter/peers -H "Authorization: Bearer $TOKEN"
```

A peer that does not open shows its last `error`, such as a refused connection.

Send a short message to a roaming subscriber. On the **Messages** page, its delivery attempt names the visited MME, and the message shows **delivered**.

## Security

Through the edge agent, short messages can come from the visited networks. Ella SMSC accepts them from any realm that the agent relays: have the operator's edge agent filter the realms of its roaming partners. See [Security](../explanation/security.md#diameter-peers).

When an agent fails while a short message is in flight, Ella SMSC sends it again through the next agent, marked as a possible retransmission. The visited MME may then deliver it twice.
