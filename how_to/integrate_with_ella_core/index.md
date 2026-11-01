# Integrate with Ella Core

This guide connects Ella SMSC to Ella Core, with Ella Core as the HSS (S6c) and the MME or SMSF (SGd).

## Prerequisites

- **Ella SMSC**: installed and started, as in [Install](https://docs.ellanetworks.com/smsc/how_to/install/index.md).

The examples use these values:

| Value          | Role                               |
| -------------- | ---------------------------------- |
| `10.3.0.7`     | Ella SMSC's Diameter address       |
| `001` / `01`   | Ella Core's MCC and MNC            |
| `+15550000000` | Ella SMSC's service centre address |
| `+15550001111` | Ella Core's SMS number             |

## 1. Set the Diameter address

In `smsc.yaml`, set the address Ella Core reaches Ella SMSC on, and restart Ella SMSC:

```
diameter:
  address: 10.3.0.7
```

## 2. Set the operator

In the Ella SMSC UI, go to the **Operator** page:

- Click the edit icon next to **Operator ID (MCC/MNC)**, enter Ella Core's MCC and MNC, e.g. `001` / `01`, and click **Update**.
- Click the edit icon next to **Service Centre Address**, enter `+15550000000`, and click **Update**.
- Click the edit icon next to **Numbering**, enter your country code and prefixes, and click **Update**.

Go to the **Diameter** page and note the **Host**, e.g. `smsc.node.epc.mnc001.mcc001.3gppnetwork.org`.

## 3. Add Ella SMSC to Ella Core

In the Ella Core UI, go to **Operator** and scroll to the **SMS** section:

- Check that **SMS Number** is set, e.g. `+15550001111`, Ella Core's default.
- Click **Add Service Center**, enter:
  - **Address**: `10.3.0.7`
  - **Numbers**: `+15550000000`
  - **Diameter Identity**: the **Host** of step 2.
- Click **Add**.

The service center shows **Connected**.

## 4. Give subscribers an MSISDN

In the Ella Core UI, for each subscriber that sends or receives SMS, go to **Subscribers**, open the subscriber, click the edit icon next to **MSISDN**, enter its E.164 number, e.g. `+15551230001`, and click **Update**.

## 5. Set the SMSC number on the SIM cards

On each SIM card, set the SMSC number to `+15550000000`.

## 6. Verify

In the Ella SMSC UI, go to **Diameter**. Under **Connected Peers**, Ella Core shows **open**, with **HSS** set to **yes**.

Send an SMS between two phones. On the **Messages** page, it shows **delivered**.
