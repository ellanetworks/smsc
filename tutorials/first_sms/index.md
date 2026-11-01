# Send Your First SMS

In this tutorial, we will add Ella SMSC to a private 5G network running Ella Core, and send an SMS between two phones.

You can expect to spend about 15 minutes completing this tutorial.

## Pre-requisites

To complete this tutorial, you will need:

- A 5G network running Ella Core, as built in Ella Core's [Getting Started (5G Network with Radio Hardware)](https://docs.ellanetworks.com/core/tutorials/getting_started_hardware/) tutorial, with PLMN `00101`.
- Two phones attached to the network, with SIM cards whose SMSC number you can set.

## 1. Install Ella SMSC

On the host running Ella Core, install and start the Ella SMSC snap:

```
sudo snap install ella-smsc
sudo snap start --enable ella-smsc.smscd
```

## 2. Initialize Ella SMSC

Open your browser at `http://<host-ip>:5010`. The **Initialize Ella SMSC** page opens.

Enter an email and a password of at least 8 characters, and click **Create**.

The **Diameter** page shows no connected peers yet.

## 3. Connect Ella Core

In the Ella Core UI, go to **Operator** and scroll to the **SMS** section.

Click **Add Service Center** and set:

- **Address**: `127.0.0.1`
- **Numbers**: `+15550000000`
- **Diameter Identity**: `smsc.node.epc.mnc001.mcc001.3gppnetwork.org`

Click **Add**. The service center shows **Connected**.

In the Ella SMSC UI, go to **Diameter**. Under **Connected Peers**, Ella Core shows **open**, with **HSS** set to **yes**.

## 4. Give the subscribers an MSISDN

In the Ella Core UI, for each of the two subscribers:

- Go to **Subscribers** and open the subscriber.
- In **Provisioning**, click the edit icon next to **MSISDN**.
- Enter `+15551230001` for the first subscriber and `+15551230002` for the second, and click **Update**.

## 5. Set the SMSC number on the SIM cards

On each SIM card, set the SMSC number to `+15550000000`.

## 6. Send an SMS

From the first phone, send `hello` to `+15551230002`. The second phone receives it.

In the Ella SMSC UI, go to **Messages**. The message shows **delivered**. Click it to see its routing and delivery attempts.

You have added SMS to your 5G network.

## Next steps

- [Integrate with Ella IMS](https://docs.ellanetworks.com/smsc/how_to/integrate_with_ella_ims/index.md) to deliver SMS over IP.
- [Send messages from an application](https://docs.ellanetworks.com/smsc/how_to/send_messages_from_an_application/index.md).
