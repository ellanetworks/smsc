---
description: Send (U)SIM data download messages, such as an OTA platform's secured packets, over the Ella SMSC API, and read the SIM's answers.
---

# Send (U)SIM Data Download Messages

This guide sends a secured packet that an OTA platform built to a SIM, and reads the SIM's answer.

You need an API token with the Submitter role. See [Send Messages from an Application](send_messages_from_an_application.md).

## 1. Send the packet

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

The SIM's answer is the `user_data` of the last of `deliver_reports`. `deliver_reports` is absent when the serving node did not pass the phone's answer on.

See the [Messages API](../reference/api/messages.md).
