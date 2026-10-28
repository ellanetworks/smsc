---
description: Step-by-step instructions to reach roaming subscribers through an IPX Diameter agent, and an HSS through a DRA.
---

# Connect through Diameter Agents

This guide connects Ella SMSC to an edge agent (DEA), to deliver short messages to roaming subscribers, and to a DRA in front of the HSS.

## Prerequisites

- **Ella SMSC**: integrated with its core, as in [Integrate with a Third-Party Core](integrate_with_third_party_core.md).
- **An API token** of a Network Manager or an Admin: see [Send Messages from an Application](send_messages_from_an_application.md).
- **The agents**: their Diameter hosts and SCTP addresses. Each has Ella SMSC's host, realm and Diameter address as a peer.
- **The visited networks**: support SMS in MME (SGd), or an SMSF with a Diameter host and realm.

The examples use these values:

| Value | Role |
|---|---|
| `dea1.ipx.example.net`, `192.0.2.10`, `198.51.100.10` | Edge agent |
| `dra1.example.net`, `10.3.0.20` | DRA in front of the HSS |
| `epc.mnc001.mcc001.3gppnetwork.org` | Ella SMSC's realm |

## 1. Add the edge agent

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

List every SCTP address of the agent.

To add a backup agent, repeat with its host and addresses, and `"priority": 1` on its route.

## 2. Add the DRA

Skip this step if the HSS connects to Ella SMSC.

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

## 3. Verify

In the Ella SMSC UI, go to **Diameter**:

- Each agent shows **open**.
- The DRA shows **HSS** set to **yes**.

Send a short message to a roaming subscriber. On the **Messages** page, it shows **delivered**.
