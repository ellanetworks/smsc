---
description: Route the short messages a phone sends to a short code or a range of numbers to an application over SMPP, and receive them as deliver_sm.
---

# Route a Short Code to an Application

This guide gives an application the short messages a phone sends to a short code, or to a range of numbers, over SMPP 3.4, as `deliver_sm`.

## Prerequisites

- **Ella SMSC**: installed, as in [Install](install.md).
- **A user** with the Admin role, logged in to the UI.
- **The SMPP listener**: enabled with TLS, as in steps 1 and 2 of [Send (U)SIM Data Download Messages over SMPP](send_sim_data_download_messages_over_smpp.md).
- **An ESME** for the application, as in step 3 of the same guide.
- **The short code** the phones send to, such as `4000`.

## 1. Add routes to the ESME

Go to **SMPP**, click **Edit** on the ESME, and set **Routes** to the short code, such as:

```
4000
```

To give the application a block of short codes, enter a range of numbers of the same length, first the lower, such as `40000-40099`. To give it a range of international numbers, start both numbers with `+`, such as `+15551230000-+15551230999`.

Click **Update**. The ESME's sessions stay bound.

To add routes with the API instead:

```shell
curl -X PUT http://<host-ip>:5010/api/v1/esmes/ota \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"allowed_networks": ["10.1.0.0/24"], "routes": ["4000"]}'
```

## 2. Bind as receiver or transceiver

Configure the application to bind as a receiver or a transceiver, with:

| Setting | Value |
| ------- | ----- |
| Host, port | `<host-ip>`, `2775`, over TLS |
| `system_id` | `ota` |
| `password` | The ESME's password |
| `interface_version` | `0x34` |

On the **SMPP** page, the **Sessions** of `ota` show **Receiver from** or **Transceiver from** the application's address.

## 3. Send a message from a phone

From a phone on the network, send an SMS to the short code, such as `4000`.

The message appears in **Messages** with status **At application** until the application acknowledges it, then **Delivered**.

## 4. Receive the message in the application

The application receives a `deliver_sm`:

| Field | Value |
| ----- | ----- |
| `source_addr` | The phone's number, such as `15551230001`. |
| `destination_addr` | The number the phone dialled, such as `4000`. |
| `short_message` | The message text, its user data header included. |
| `user_message_reference` | The phone's TP-MR. |

Answer `ESME_ROK` within 30 seconds. To refuse the message for good, answer `ESME_RX_P_APPN` or `ESME_RX_R_APPN` instead.

A `deliver_sm` can arrive twice. Treat one with the same `source_addr`, `destination_addr` and `user_message_reference` as one already received.

## Next steps

- See [Routed messages](../reference/smpp.md#routed-messages) for the retries, expiry and at-least-once semantics.
- See the [ESMEs API](../reference/api/esmes.md) for managing routes.
- See [Architecture](../explanation/architecture.md#messages-to-applications) for how routes interact with HSS delivery.
