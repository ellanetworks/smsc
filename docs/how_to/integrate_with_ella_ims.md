---
description: Step-by-step instructions to deliver SMS over IP through Ella IMS's IP-SM-GW.
---

# Integrate with Ella IMS

This guide lets Ella SMSC deliver SMS to phones registered for SMS over IP with Ella IMS's IP-SM-GW. Phones send SMS over IP to Ella SMSC the same way.

## Prerequisites

- **Ella SMSC**: connected to Ella Core, as in [Integrate with Ella Core](integrate_with_ella_core.md).
- **Ella IMS**: connected to Ella Core, as in Ella IMS's [Integrate with Ella Core](https://docs.ellanetworks.com/ims/how_to/integrate_with_ella_core/).

The examples use `10.3.0.7` as Ella SMSC's Diameter address and `+15550000000` as its service centre address.

## 1. Enable SMS over IP in Ella IMS

Follow Ella IMS's [Enable SMS over IP](https://docs.ellanetworks.com/ims/how_to/enable_sms_over_ip/) guide. When you add the SMSC, enter:

- **Address**: `10.3.0.7`
- **Roles**: **SGd** only
- **Service Centres**: `15550000000`
- **Transport**: **SCTP**
- **Host**: the **Host** of Ella SMSC's **Diameter** page, e.g. `smsc.node.epc.mnc001.mcc001.3gppnetwork.org`

## 2. Verify

In the Ella SMSC UI, go to **Diameter**. Ella IMS shows **open**, with **Applications** set to **SGd** and **HSS** set to **no**.

Send an SMS to a phone registered for SMS over IP. On the **Messages** page, click the message: its delivery attempt went to Ella IMS's host, marked **(IP-SM-GW)**.
