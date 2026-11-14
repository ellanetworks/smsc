# Route a Short Code to an Application

This guide gives an application the short messages a phone sends to a short code, or to a range of numbers, over SMPP 3.4, as `deliver_sm`.

## Prerequisites

- **Ella SMSC**: installed, as in [Install](https://docs.ellanetworks.com/smsc/how_to/install/index.md).
- **A user** with the Admin role, logged in to the UI.
- **The SMPP listener**: enabled with TLS, as in steps 1 and 2 of [Send (U)SIM Data Download Messages over SMPP](https://docs.ellanetworks.com/smsc/how_to/send_sim_data_download_messages_over_smpp/index.md).
- **An ESME** for the application, as in step 3 of the same guide.
- **The short code** the phones send to, such as `4000`.

## 1. Add routes to the ESME

Go to **SMPP**, click **Edit** on the ESME, and set **Routes** to the short code, such as:

```
4000
```

To give the application a block of short codes, enter a range of numbers of the same length, first the lower, such as `40000-40099`. To give it a range of international numbers, start both numbers with `+`, such as `+15551230000-+15551230999`.

Click **Update**. The ESME's sessions stay bound.

## 2. Bind as receiver or transceiver

Configure the application to bind as a receiver or a transceiver, with:

| Setting             | Value                         |
| ------------------- | ----------------------------- |
| Host, port          | `<host-ip>`, `2775`, over TLS |
| `system_id`         | `ota`                         |
| `password`          | The ESME's password           |
| `interface_version` | `0x34`                        |

On the **SMPP** page, the **Sessions** of `ota` show **Receiver from** or **Transceiver from** the application's address.

## 3. Send a message from a phone

From a phone on the network, send an SMS to the short code, such as `4000`.

The message appears in **Messages** with status **At application** until the application acknowledges it, then **Delivered**.

## 4. Receive the message in the application

The application receives a `deliver_sm`:

| Field                    | Value                                            |
| ------------------------ | ------------------------------------------------ |
| `source_addr`            | The phone's number, such as `15551230001`.       |
| `destination_addr`       | The number the phone dialled, such as `4000`.    |
| `short_message`          | The message text, its user data header included. |
| `user_message_reference` | The phone's TP-MR.                               |

## Next steps

- See [Routed messages](https://docs.ellanetworks.com/smsc/reference/smpp/#routed-messages) for the retries, expiry and at-least-once semantics.
- See the [ESMEs API](https://docs.ellanetworks.com/smsc/reference/api/esmes/index.md) for managing routes.
- See [Architecture](https://docs.ellanetworks.com/smsc/explanation/architecture/#messages-to-applications) for how routes interact with HSS delivery.
