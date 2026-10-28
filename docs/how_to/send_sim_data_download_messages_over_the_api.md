---
description: Send a (U)SIM data download message, such as an OTA platform's secured packet, over the Ella SMSC API, and read the SIM's answer.
---

# Send (U)SIM Data Download Messages over the API

This guide sends a secured packet that an OTA platform built to a SIM, over the HTTP API, and reads the SIM's answer.

## Prerequisites

- **An API token** of a Submitter user, as in [Send Messages from an Application](send_messages_from_an_application.md).
- **The secured packet**, at most 140 octets with its user data header, in hexadecimal.

## 1. Send the packet

Send the packet as a binary message, with:

- `protocol_identifier`: `127`, (U)SIM data download
- `data_coding_scheme`: `246`, 8-bit data, message class 2
- `user_data_header`: the packet's user data header, such as `027000`
- `user_data`: the packet after its header

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

## 2. Wait for the delivery

Get the message until its `status` is no longer `pending`:

```shell
curl http://<host-ip>:5010/api/v1/messages/<id> \
  -H "Authorization: Bearer ellasmsc_..."
```

## 3. Read the SIM's answer

Get the content of the message:

```shell
curl http://<host-ip>:5010/api/v1/messages/<id>/content \
  -H "Authorization: Bearer ellasmsc_..."
```

The SIM's answer is the `user_data` of the last item of `deliver_reports`. See [Get the Content of a Message](../reference/api/messages.md#get-the-content-of-a-message).
