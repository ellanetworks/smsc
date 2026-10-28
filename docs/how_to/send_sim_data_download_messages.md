---
description: Send (U)SIM data download messages, such as an OTA platform's secured packets, over the Ella SMSC API or SMPP, and read the SIM's answers.
---

# Send (U)SIM Data Download Messages

This guide sends a secured packet that an OTA platform built to a SIM, and reads the SIM's answer, over the [API](#over-the-api) or over [SMPP](#over-smpp).

## Over the API

You need an API token with the Submitter role. See [Send Messages from an Application](send_messages_from_an_application.md).

### 1. Send the packet

Send the packet as a binary message, with:

- `protocol_identifier`: `127`, (U)SIM data download
- `data_coding_scheme`: a message class 2 coding, such as `246`
- `user_data_header`: the packet's user data header, in hexadecimal, such as `027000`
- `user_data`: the packet, in hexadecimal

```shell
curl http://<host-ip>:5010/api/v1/messages \
  -H "Authorization: Bearer ellasmsc_..." \
  -H "Content-Type: application/json" \
  -d '{
        "from": "+15550001000",
        "to": "+15551230002",
        "binary": {
          "protocol_identifier": 127,
          "data_coding_scheme": 246,
          "user_data_header": "027000",
          "user_data": "<packet>"
        }
      }'
```

Note the `id` of the message in the response.

For a packet over 140 octets with its header, send each part as its own request, with a concatenation information element in its `user_data_header`.

### 2. Wait for the delivery

Get the message until its `status` is no longer `pending`:

```shell
curl http://<host-ip>:5010/api/v1/messages/<id> \
  -H "Authorization: Bearer ellasmsc_..."
```

### 3. Read the SIM's answer

Get the content of the message:

```shell
curl http://<host-ip>:5010/api/v1/messages/<id>/content \
  -H "Authorization: Bearer ellasmsc_..."
```

The SIM's answer is the `user_data` of the last of `deliver_reports`. `deliver_reports` is absent when the serving node did not pass the phone's answer on.

See the [Messages API](../reference/api/messages.md).

## Over SMPP

The OTA platform is an SMPP 3.4 ESME. It submits the packet with `submit_sm`, and receives the SIM's answer as a `deliver_sm`.

### 1. Enable the SMPP listener

Add an `smpp` block to the [configuration file](../reference/config_file.md), and restart Ella SMSC:

```yaml
smpp:
  address: 0.0.0.0
  port: 2775
  tls:
    cert: /etc/smsc/smpp.pem
    key: /etc/smsc/smpp.key
```

Without `tls`, SMPP is plain TCP: use it only over a private link, such as an IPsec VPN, or in a lab.

### 2. Create a user and an ESME

Log in as an Admin. Creating users and ESMEs needs an access token, not an API token:

```shell
TOKEN=$(curl -s http://<host-ip>:5010/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email": "admin@example.com", "password": "<password>"}' | jq -r .result.token)
```

Create a Submitter user for the OTA platform:

```shell
curl http://<host-ip>:5010/api/v1/users \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"email": "ota@example.com", "password": "<password>", "role_id": 4}'
```

Create an ESME for that user, allowed to bind from the OTA platform's network:

```shell
curl http://<host-ip>:5010/api/v1/esmes \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
        "system_id": "ota",
        "user": "ota@example.com",
        "allowed_networks": ["10.1.0.0/24"],
        "forward_sim_answers": true
      }'
```

Copy the `password` in the response. It is not shown again. SMPP 3.4 limits passwords to 8 characters.

In the UI, the same is in **Users** and **SMPP**.

### 3. Bind

Configure the OTA platform to bind as a transceiver, or as a transmitter and a receiver, with:

- host: `<host-ip>`, port `2775`, over TLS if `smpp.tls` is set
- `system_id`: `ota`
- `password`: the generated password
- `interface_version`: `0x34`

A refused bind is `ESME_RBINDFAIL`, whatever the reason. The Ella SMSC log gives it. See [Binds](../reference/smpp.md#binds).

### 4. Submit the packet

Send a `submit_sm` with:

| Field | Value |
| ----- | ----- |
| `source_addr_ton`, `source_addr_npi`, `source_addr` | `1`, `1`, `15550001000`, or a short code with TON `3` |
| `dest_addr_ton`, `dest_addr_npi`, `destination_addr` | `1`, `1`, `15551230002`, the MSISDN of the SIM |
| `esm_class` | `0x40`: UDHI |
| `protocol_id` | `0x7F`: (U)SIM data download |
| `data_coding` | `0xF6`, 8-bit data, class 2. `0x02` and `0x04` become `0xF6` too. |
| `registered_delivery` | `0x08` for the SIM's answer, `0x09` for the answer and a delivery receipt. `0x00` with `forward_sim_answers` on: the answer only. |
| `short_message` | The packet's user data header, then the packet: `027000…` |

`submit_sm_resp` gives the `message_id`, which is the `id` of the message in the [Messages API](../reference/api/messages.md).

For a packet over 140 octets with its header, send each part as its own `submit_sm`, with a concatenation information element in its user data header. See [`submit_sm`](../reference/smpp.md#submit_sm) for every field.

### 5. Read the SIM's answer

When the phone answers, the SMSC sends a `deliver_sm` on a receiver or transceiver session of the ESME:

| Field | Value |
| ----- | ----- |
| `esm_class` | `0x50`: SME Manual/User Acknowledgement, UDHI |
| `protocol_id` | `0x7F` |
| `data_coding` | That of the `submit_sm` |
| `short_message` | The SIM's Response Packet: `027100…` |
| `receipted_message_id` | The `message_id` |

Answer it with `deliver_sm_resp` `ESME_ROK`. Without that answer, the SMSC sends it again.

With `registered_delivery` `0x09`, the receipt follows the answer, as a `deliver_sm` with `esm_class` `0x04`:

```
id:42 sub:001 dlvrd:001 submit date:2610281430 done date:2610281431 stat:DELIVRD err:000 text:
```

A SIM that rejects the packet answers with an RP-ERROR: the message fails, the answer is still forwarded, and the receipt is `stat:UNDELIV err:066`. See [SIM answers](../reference/smpp.md#sim-answers).

### Example: pySim smpp-ota-tool

[pySim](https://osmocom.org/projects/pysim)'s `contrib/smpp-ota-tool.py` builds a secured packet from APDUs, submits it over SMPP, and decodes the answer. It sends `registered_delivery` `0x00`, so the ESME needs `forward_sim_answers` on.

From a pySim checkout, select DF.GSM on the SIM RFM application of a sysmoISIM:

```shell
PYTHONPATH=./ ./contrib/smpp-ota-tool.py \
  --host <host-ip> --port 2775 \
  --system-id ota --password <password> \
  --src-addr 15550001000 --dest-addr 15551230002 \
  --kic <KIC> --kid <KID> --kic-idx 1 --kid-idx 1 \
  --algo-crypt triple_des_cbc2 --algo-auth triple_des_cbc2 \
  --tar B00010 \
  --apdu A0A40000027F20 --apdu A0C0000016 \
  --timeout 60
```

The tool prints the last response and status word:

```
0000ffff7f2002000000000009b106350400838a838a 9000
```

The tool connects over plain TCP. `--timeout` is how long it waits for the answer, which includes the delivery to the phone. Do not use `--por-in-submit`: Ella SMSC does not forward an answer that the SIM sends in an SMS-SUBMIT.
