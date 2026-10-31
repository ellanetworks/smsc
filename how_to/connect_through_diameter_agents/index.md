# Connect through Diameter Agents

This guide connects Ella SMSC to an edge agent (DEA), to deliver short messages to roaming subscribers, and to a DRA in front of the HSS.

## Prerequisites

- **Ella SMSC**: integrated with its core, as in [Integrate with a Third-Party Core](https://docs.ellanetworks.com/smsc/how_to/integrate_with_third_party_core/index.md).
- **A user** with the Network Manager or Admin role.
- **The agents**: their Diameter hosts and SCTP addresses. Each has Ella SMSC's host, realm and Diameter address as a peer.
- **The visited networks**: support SMS in MME (SGd), or an SMSF with a Diameter host and realm.

The examples use these values:

| Value                                                 | Role                    |
| ----------------------------------------------------- | ----------------------- |
| `dea1.ipx.example.net`, `192.0.2.10`, `198.51.100.10` | Edge agent              |
| `dra1.example.net`, `10.3.0.20`                       | DRA in front of the HSS |
| `epc.mnc001.mcc001.3gppnetwork.org`                   | Ella SMSC's realm       |

## 1. Add the edge agent

In the Ella SMSC UI, go to **Diameter** and click **Add Peer**:

- **Host**: `dea1.ipx.example.net`
- **Addresses**: `192.0.2.10, 198.51.100.10`, every SCTP address of the agent.
- **Port**: `3868`

Click **Add Route**, and keep **Application** set to **SGd** and **Destination** to **Default**. Click **Add**.

To add a backup agent, repeat with its host and addresses, and set the route's **Priority** to `1`.

## 2. Add the DRA

Skip this step if the HSS connects to Ella SMSC.

Click **Add Peer**:

- **Host**: `dra1.example.net`
- **Addresses**: `10.3.0.20`
- **Port**: `3868`

Click **Add Route**, and set:

- **Application**: **S6c**
- **Destination**: **Realm**
- **Realm**: `epc.mnc001.mcc001.3gppnetwork.org`

Click **Add**.

## 3. Verify

On the **Diameter** page, under **Configured Peers**:

- Each agent shows **open**. Hover over another state to see why.
- The DRA shows **HSS** set to **yes**.

Send a short message to a roaming subscriber. On the **Messages** page, it shows **delivered**.
