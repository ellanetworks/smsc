# Send (U)SIM Data Download Messages over SMPP

This guide connects an OTA platform to Ella SMSC over SMPP 3.4 with TLS, sends a secured packet to a SIM with `submit_sm`, and receives the SIM's answer and a delivery receipt as `deliver_sm`.

## Prerequisites

- **Ella SMSC**: installed, as in [Install](https://docs.ellanetworks.com/smsc/how_to/install/index.md).
- **A TLS certificate** for the SMSC, `smpp.pem`, with its chain, and its private key, `smpp.key`, in PEM.
- **A user** with the Admin role, logged in to the UI.
- **The OTA platform's network**: the IP prefix it connects from, such as `10.1.0.0/24`.
- **The secured packet**, at most 140 octets with its user data header.

The examples use `15550001000` as the source and `15551230002` as the MSISDN of the SIM.

## 1. Enable the SMPP listener

Copy the certificate and key:

```
sudo cp smpp.pem smpp.key /var/snap/ella-smsc/common/
sudo chmod 600 /var/snap/ella-smsc/common/smpp.key
```

In `/var/snap/ella-smsc/common/smsc.yaml`, add:

```
smpp:
  address: 0.0.0.0
  port: 2775
  tls:
    cert: /var/snap/ella-smsc/common/smpp.pem
    key: /var/snap/ella-smsc/common/smpp.key
```

Restart Ella SMSC:

```
sudo snap restart ella-smsc.smscd
```

## 2. Create a user for the OTA platform

In the UI, go to **Users** and click **Create**. Set:

- **Email**: `ota@example.com`
- **Password**: any password of at least 8 characters
- **Role**: **Submitter**

Click **Create**.

## 3. Add the ESME

Go to **SMPP** and click **Add ESME**. Set:

- **System ID**: `ota`
- **User**: `ota@example.com`
- **Password**: empty, to generate one
- **Allowed networks**: `10.1.0.0/24`

Click **Add**. Copy the generated password from the **ESME Password** dialog. It is not shown again.

## 4. Bind the OTA platform

Configure the OTA platform to bind as a transceiver, with:

| Setting             | Value                         |
| ------------------- | ----------------------------- |
| Host, port          | `<host-ip>`, `2775`, over TLS |
| `system_id`         | `ota`                         |
| `password`          | The generated password        |
| `interface_version` | `0x34`                        |

On the **SMPP** page, the **Sessions** of `ota` show **Transceiver from** the platform's address.

## 5. Submit the packet

Send a `submit_sm` with:

| Field                                                | Value                                                     |
| ---------------------------------------------------- | --------------------------------------------------------- |
| `source_addr_ton`, `source_addr_npi`, `source_addr`  | `1`, `1`, `15550001000`                                   |
| `dest_addr_ton`, `dest_addr_npi`, `destination_addr` | `1`, `1`, `15551230002`                                   |
| `esm_class`                                          | `0x40`, UDHI                                              |
| `protocol_id`                                        | `0x7F`, (U)SIM data download                              |
| `data_coding`                                        | `0xF6`, 8-bit data, message class 2                       |
| `registered_delivery`                                | `0x09`: the SIM's answer and a delivery receipt           |
| `short_message`                                      | The packet's user data header, then the packet: `027000…` |

`submit_sm_resp` gives the `message_id`.

## 6. Receive the SIM's answer

The SMSC sends a `deliver_sm` with `esm_class` `0x50` and `receipted_message_id` set to the `message_id`. Its `short_message` is the SIM's Response Packet, `027100…`.

Answer it with `deliver_sm_resp` `ESME_ROK`.

## 7. Receive the delivery receipt

The SMSC sends a `deliver_sm` with `esm_class` `0x04` and `receipted_message_id` set to the `message_id`. Its `short_message` is the receipt:

```
id:42 sub:001 dlvrd:001 submit date:2610281430 done date:2610281431 stat:DELIVRD err:000 text:
```

Answer it with `deliver_sm_resp` `ESME_ROK`.

See the [SMPP reference](https://docs.ellanetworks.com/smsc/reference/smpp/index.md) for every field, outcome and refusal.
