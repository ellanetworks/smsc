---
description: Step-by-step instructions to connect Ella SMSC to a 3GPP-compliant 4G or 5G core.
---

# Integrate with a Third-Party Core

This guide connects Ella SMSC to a 4G or 5G core other than Ella Core, through its HSS (S6c) and its MMEs or SMSFs (SGd).

## Prerequisites

- **Ella SMSC**: installed and started, as in [Install](install.md).
- **HSS**: supports S6c, in the realm `epc.mnc<MNC>.mcc<MCC>.3gppnetwork.org` of the core's PLMN.
- **MME or SMSF**: supports SGd, and gives the MSISDN of the phone in each short message it forwards. The HSS names it, in its routing answers, by the Diameter identity it connects to Ella SMSC with.

The examples use `10.3.0.7` as Ella SMSC's Diameter address, `001` / `01` as the core's MCC and MNC, and `+15550000000` as Ella SMSC's service centre address.

## 1. Set the Diameter address

In `smsc.yaml`, set the address the core reaches Ella SMSC on, and restart Ella SMSC:

```yaml
diameter:
  address: 10.3.0.7
```

## 2. Set the operator

In the Ella SMSC UI, go to the **Operator** page:

- Click the edit icon next to **Operator ID (MCC/MNC)**, enter the core's MCC and MNC, e.g. `001` / `01`, and click **Update**.
- Click the edit icon next to **Service Centre Address**, enter `+15550000000`, and click **Update**.
- Click the edit icon next to **Numbering**, enter your country code and prefixes, and click **Update**.

The **Diameter** page shows Ella SMSC's **Host** and **Realm**, e.g. `smsc.node.epc.mnc001.mcc001.3gppnetwork.org` and `epc.mnc001.mcc001.3gppnetwork.org`.

## 3. Add Ella SMSC to the core

In the HSS, and in each MME or SMSF, add Ella SMSC as a Diameter peer:

- **Address**: `10.3.0.7`
- **Port**: `3868`
- **Transport**: SCTP
- **Host** and **Realm**: those of step 2.
- **Application**: S6c in the HSS, SGd in the MME or SMSF.

In each MME or SMSF, route the short messages of service centre address `+15550000000` to Ella SMSC.

## 4. Provision the subscribers

In the HSS, give each subscriber that sends or receives SMS an MSISDN. On each SIM card, set the SMSC number to `+15550000000`.

## 5. Verify

In the Ella SMSC UI, go to **Diameter**. Under **Connected Peers**:

- The HSS shows **open**, with **HSS** set to **yes**. If it shows **no**, its realm differs from Ella SMSC's, or it does not support S6c.
- Each MME or SMSF shows **open**, with **SGd** in **Applications**.

Send an SMS between two phones. On the **Messages** page, it shows **delivered**.

See [Connectivity](../reference/connectivity.md) and [3GPP Compliance](../reference/3gpp_compliance.md) for the interfaces Ella SMSC supports.
