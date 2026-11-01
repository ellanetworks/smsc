# SMPP

Ella SMSC is an SMPP 3.4 SMSC for External Short Message Entities (ESMEs), such as OTA platforms. An ESME submits short messages to phones with `submit_sm`, and receives delivery receipts and SIM answers as `deliver_sm`.

The listener runs when the [configuration file](https://docs.ellanetworks.com/smsc/reference/config_file/index.md) has an `smpp` block. ESME accounts are managed with the [ESMEs API](https://docs.ellanetworks.com/smsc/reference/api/esmes/index.md) or the **SMPP** page of the UI.

"§" without a TS number refers to SMPP v3.4, Issue 1.2. **Convention** marks a behaviour that SMPP 3.4 does not define. All conventions are listed under [Conventions](#conventions).

## Transport

| Item                | Value                                                                                                       |
| ------------------- | ----------------------------------------------------------------------------------------------------------- |
| Transport           | TCP (§2.4), on `smpp.port`, default `2775`.                                                                 |
| TLS                 | With `smpp.tls`: TLS 1.2 or later, from the first octet. A listener is either plain or TLS. **Convention**. |
| Client address      | The address of the TCP connection. The PROXY protocol is not supported.                                     |
| `interface_version` | `0x34`.                                                                                                     |
| SMSC `system_id`    | `EllaSMSC`, in bind responses.                                                                              |
| `command_length`    | 16 to 69,632 octets (68 KiB). **Convention**: SMPP 3.4 sets no maximum (§3.2.1).                            |
| `sequence_number`   | `0x00000001` to `0x7FFFFFFF` (§5.1.4).                                                                      |

## PDUs

### Supported

| PDU                                                     | Direction   | Session state       | Handling                                                                                                 |
| ------------------------------------------------------- | ----------- | ------------------- | -------------------------------------------------------------------------------------------------------- |
| `bind_transmitter`, `bind_receiver`, `bind_transceiver` | ESME → SMSC | OPEN                | See [Binds](#binds).                                                                                     |
| `submit_sm`                                             | ESME → SMSC | BOUND_TX, BOUND_TRX | One short message. See [`submit_sm`](#submit_sm).                                                        |
| `deliver_sm`                                            | SMSC → ESME | BOUND_RX, BOUND_TRX | Delivery receipts and SIM answers. See [Notifications](#notifications).                                  |
| `enquire_link`                                          | Both        | Bound               | Answered at once (§4.11).                                                                                |
| `unbind`                                                | Both        | Bound               | See [Session end](#session-end).                                                                         |
| `generic_nack`                                          | Both        | Any                 | Sent for a PDU that cannot be handled (§4.3). Received in answer to a `deliver_sm`: a temporary failure. |

### Errors

| PDU received                                                                                                                     | Response                                                 | Session  |
| -------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------- | -------- |
| `command_length` under 16 or over 68 KiB                                                                                         | `generic_nack` `ESME_RINVCMDLEN`                         | Closed   |
| `sequence_number` of `0` or over `0x7FFFFFFF`                                                                                    | `generic_nack` `ESME_RUNKNOWNERR`, `sequence_number` `0` | Stays up |
| `query_sm`, `replace_sm`, `cancel_sm`, `submit_multi`, `data_sm`, `outbind`, `deliver_sm`, an undefined or reserved `command_id` | `generic_nack` `ESME_RINVCMDID`                          | Stays up |
| `alert_notification`                                                                                                             | None: discarded (§4.12)                                  | Stays up |
| A bind in a bound state                                                                                                          | Its response with `ESME_RALYBND`                         | Stays up |
| Another request in a state that does not allow it (§2.3, Table 2-1)                                                              | Its response with `ESME_RINVBNDSTS`                      | Stays up |
| A body on a PDU that has none, or a body shorter than its mandatory parameters                                                   | `generic_nack` `ESME_RINVCMDLEN`                         | Stays up |
| A C-Octet String over its maximum length                                                                                         | Its response with `ESME_RINVPARLEN`                      | Stays up |
| Optional parameters that do not decode as TLVs                                                                                   | Its response with `ESME_RINVOPTPARSTREAM`                | Stays up |
| A response that matches no request                                                                                               | None: discarded                                          | Stays up |

Unknown TLVs, and known TLVs not expected for the PDU, are ignored (§3.3). Responses may go out of order: they match their requests by `sequence_number` (§2.5.2).

## Binds

### Checks

The SMSC checks a bind in this order:

1. At most 20 binds per minute from the client address: per IPv4 address, or per IPv6 /64.

1. `interface_version` is `0x34` or later.

1. At most 100 wrong passwords per hour for the `system_id`, from inside its allowed networks. A successful bind resets the count. Wrong passwords from outside the allowed networks, and binds with an unknown `system_id`, are counted apart, with the same limit, and never lock out the account.

1. `system_id` and `password` match an ESME account. A check against an unknown `system_id` takes as long as one against a known `system_id`.

1. The client address is in the account's `allowed_networks`. An empty list allows no address.

1. The role of the account's user allows the bind type:

   | Bind        | Permissions                     | Roles                             |
   | ----------- | ------------------------------- | --------------------------------- |
   | Transmitter | Send messages                   | Admin, Network Manager, Submitter |
   | Receiver    | Read messages and their content | Admin, Network Manager, Submitter |
   | Transceiver | Both                            | Admin, Network Manager, Submitter |

   See [Roles](https://docs.ellanetworks.com/smsc/reference/api/#roles).

1. Fewer than 4 sessions of the account are bound.

### Response

| Outcome            | `command_status`                                             | Body                                                                   |
| ------------------ | ------------------------------------------------------------ | ---------------------------------------------------------------------- |
| Accepted           | `ESME_ROK`                                                   | `system_id` `EllaSMSC`, and `sc_interface_version` `0x34` (§5.3.2.25). |
| Refused, any check | `ESME_RBINDFAIL`, never `ESME_RINVSYSID` or `ESME_RINVPASWD` | None. `bind_transceiver_resp`: `system_id` `EllaSMSC` (Table 4-6).     |

The log gives the reason of a refused bind, with the `system_id` and the client address, at level `info`.

### Bind fields

| Field                                                  | Handling                      |
| ------------------------------------------------------ | ----------------------------- |
| `system_id`                                            | Up to 15 characters (§4.1.1). |
| `password`                                             | Up to 8 characters (§4.1.1).  |
| `interface_version`                                    | See [Checks](#checks).        |
| `system_type`, `addr_ton`, `addr_npi`, `address_range` | Ignored.                      |

### Account changes

Updating or deleting an ESME, or changing the role of or deleting its user, closes its sessions.

## Sessions

### Limits

| Item              | Value                                                                                                                      |
| ----------------- | -------------------------------------------------------------------------------------------------------------------------- |
| Sessions per ESME | 4, of any bind types.                                                                                                      |
| ESME accounts     | 50.                                                                                                                        |
| Inbound window    | 64 requests handled at once per session. One more is answered `ESME_RTHROTTLED`. §2.5.2 recommends at most 10 outstanding. |
| Outbound window   | 10 `deliver_sm` without a response per session (§2.6.2).                                                                   |

### Timers

The timers are fixed (§7.2).

| Timer                | Value                       | Action                                                                                                |
| -------------------- | --------------------------- | ----------------------------------------------------------------------------------------------------- |
| `session_init_timer` | 30 s                        | A connection that has not bound is closed.                                                            |
| `enquire_link_timer` | 30 s without a PDU received | The SMSC sends `enquire_link`, and closes the session without a response within the `response_timer`. |
| `inactivity_timer`   | 90 s without a PDU received | The session is closed.                                                                                |
| `response_timer`     | 30 s                        | A `deliver_sm` without a response is a temporary failure.                                             |

### Session end

| Event                  | Handling                                                                                                                                                                                                                 |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `unbind` from the ESME | The SMSC finishes the `submit_sm` in progress, answers `unbind_resp`, waits up to 2 s for the ESME to close the connection, then closes it (§2.2, §4.2). A `submit_sm` received meanwhile is answered `ESME_RINVBNDSTS`. |
| SMSC shutdown          | The SMSC sends `unbind` to each session, waits up to 5 s for `unbind_resp`, then closes.                                                                                                                                 |

## `submit_sm`

One `submit_sm` stores exactly one SMS-SUBMIT: one message of the [Messages API](https://docs.ellanetworks.com/smsc/reference/api/messages/index.md), owned by the ESME's user, delivered as a message from the API.

`submit_sm_resp` carries `message_id`: the `id` of the message in the Messages API, in decimal. The SMSC answers once the message is stored.

### Addressing

The destination is a phone, routed through the HSS (§5.2.5, §5.2.6, §5.2.9; TS 23.040 §9.1.2.5):

| `dest_addr_ton`                | `dest_addr_npi` | `destination_addr`                                                                                                                   |
| ------------------------------ | --------------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| `1`, international             | `0` or `1`      | The international number.                                                                                                            |
| `0`, unknown, or `2`, national | `0` or `1`      | Converted to international with the operator's [numbering plan](https://docs.ellanetworks.com/smsc/reference/api/operator/index.md). |

| Error                                                                              | `command_status`  |
| ---------------------------------------------------------------------------------- | ----------------- |
| Another `dest_addr_ton`                                                            | `ESME_RINVDSTTON` |
| Another `dest_addr_npi`                                                            | `ESME_RINVDSTNPI` |
| Not 1 to 20 digits, or not 1 to 15 digits without a leading `0` once international | `ESME_RINVDSTADR` |

The source is shown to the recipient as the sender (§5.2.8):

| `source_addr_ton`         | `source_addr_npi`                    | `source_addr`                                                           | Sent to the phone as  |
| ------------------------- | ------------------------------------ | ----------------------------------------------------------------------- | --------------------- |
| `1`, international        | `0` or `1`                           | 1 to 15 digits, no leading `0`                                          | TON 1, NPI 1          |
| `5`, alphanumeric         | Any                                  | 1 to 11 letters and digits, single spaces between words, not all digits | TON 5                 |
| `0`, `2`, `3`, `4` or `6` | `0`, `1`, `3`, `4`, `8`, `9` or `10` | 1 to 20 digits                                                          | The TON and NPI given |

| Error                        | `command_status`                                                                                       |
| ---------------------------- | ------------------------------------------------------------------------------------------------------ |
| Empty `source_addr`          | `ESME_RINVSRCADR`. The SMSC has no default source.                                                     |
| A reserved `source_addr_ton` | `ESME_RINVSRCTON`                                                                                      |
| Another `source_addr_npi`    | `ESME_RINVSRCNPI`. The NPIs accepted have the same value and meaning in §5.2.6 and TS 23.040 §9.1.2.5. |
| Another `source_addr`        | `ESME_RINVSRCADR`                                                                                      |

### `data_coding`

`data_coding` (§5.2.19) is mapped to a TP-DCS (TS 23.038 §4):

| `data_coding`                                                          | SMPP 3.4 meaning                  | TP-DCS and user data                                                                                                                                                       |
| ---------------------------------------------------------------------- | --------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `0x00`                                                                 | SMSC default alphabet             | TP-DCS `0x00`. The octets are GSM 7-bit default alphabet septets, one per octet, `0x1B` for the extension table. An octet over `0x7F`: `ESME_RSUBMITFAIL`. **Convention**. |
| `0x01`                                                                 | IA5 (ASCII)                       | Converted to GSM 7-bit, TP-DCS `0x00`, or to UCS2, TP-DCS `0x08`, when a character has no GSM 7-bit equivalent. An octet over `0x7F`: `ESME_RSUBMITFAIL`.                  |
| `0x02`                                                                 | Octet unspecified                 | TP-DCS `0x04`, 8-bit.                                                                                                                                                      |
| `0x03`                                                                 | Latin-1                           | Converted to GSM 7-bit, TP-DCS `0x00`, or to UCS2, TP-DCS `0x08`, when a character has no GSM 7-bit equivalent.                                                            |
| `0x04`                                                                 | Octet unspecified                 | TP-DCS `0x04`, 8-bit.                                                                                                                                                      |
| `0x08`                                                                 | UCS2                              | TP-DCS `0x08`, octets copied. An odd number of octets: `ESME_RSUBMITFAIL`.                                                                                                 |
| `0xF0` to `0xF7`                                                       | GSM message class control         | Copied.                                                                                                                                                                    |
| `0xC0` to `0xDF`                                                       | GSM message waiting indication    | `ESME_RSUBMITFAIL`.                                                                                                                                                        |
| `0x05` to `0x07`, `0x09` to `0xBF`, `0xE0` to `0xEF`, `0xF8` to `0xFF` | Other character sets, or reserved | `ESME_RSUBMITFAIL`. SMPP 3.4 has no status for an invalid `data_coding`.                                                                                                   |

With a GSM 7-bit TP-DCS, the SMSC packs the septets after the user data header, with fill bits (TS 23.040 §9.2.3.24).

**`dest_addr_subunit`** (§5.3.2.1, §5.2.19 note e) sets the message class bits of TP-DCS:

| `dest_addr_subunit`   | Class      |
| --------------------- | ---------- |
| `0`, `5` to `255`     | Unchanged. |
| `1`, MS display       | Class 0    |
| `2`, mobile equipment | Class 1    |
| `3`, smart card       | Class 2    |
| `4`, external unit    | Class 3    |

A different class already in `data_coding`, or a TP-DCS without class bits: `ESME_RINVOPTPARAMVAL`.

**(U)SIM data download.** With `protocol_id` `0x7F`, `data_coding` `0x02` or `0x04` and no `dest_addr_subunit`, TP-DCS is `0xF6`, 8-bit data, class 2. **Convention**: TS 23.040 §9.2.3.9 requires class 2 for (U)SIM data download.

### `esm_class`

| Bits (§5.2.12)      | Value                                 | Handling                                                                                                                                        |
| ------------------- | ------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| 1-0, messaging mode | `00` default, `11` store and forward  | Store and forward.                                                                                                                              |
| 1-0                 | `01` datagram, `10` transaction       | `ESME_RINVESMCLASS`                                                                                                                             |
| 5-2, message type   | `0010`, `0100`, ESME acknowledgements | `ESME_RINVESMCLASS`                                                                                                                             |
| 5-2                 | Other                                 | Normal message (§3.3).                                                                                                                          |
| 6, UDHI             | `1`                                   | TP-UDHI = 1. The user data starts with the user data header, its length octet included. A header longer than the user data: `ESME_RSUBMITFAIL`. |
| 7, reply path       | `1`                                   | `ESME_RINVESMCLASS`                                                                                                                             |

### `protocol_id`

Copied to TP-PID (§5.2.13; TS 23.040 §9.2.3.9):

| `protocol_id`                                        | Handling                                                                                                                                                                            |
| ---------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `0x20` to `0x3E`, telematic interworking             | `ESME_RSUBMITFAIL`                                                                                                                                                                  |
| `0x41` to `0x47`, replace short message types 1 to 7 | Replaces the pending message of the same type, from the same source, submitted by the same ESME. A message from a phone, the API or another ESME is never replaced. **Convention**. |
| `0x48`, device triggering                            | Needs an application port addressing information element, else `ESME_RSUBMITFAIL`.                                                                                                  |
| `0x7C`, `0x7F`                                       | Need message class 2 after the `data_coding` mapping, else `ESME_RSUBMITFAIL`.                                                                                                      |
| Other                                                | Copied.                                                                                                                                                                             |

### `registered_delivery`

`registered_delivery` (§5.2.17) asks for notifications to the ESME. It does not set TP-Status-Report-Request.

| Bits | Value                                         | Handling                                                      |
| ---- | --------------------------------------------- | ------------------------------------------------------------- |
| 1-0  | `00`                                          | No receipt.                                                   |
| 1-0  | `01`                                          | A [receipt](#delivery-receipts) on the final status.          |
| 1-0  | `10`                                          | A receipt on failure only: `failed`, `expired` or `replaced`. |
| 1-0  | `11`                                          | Reserved: no receipt (§3.3).                                  |
| 3-2  | `10` or `11`, SME Manual/User Acknowledgement | The [SIM answer](#sim-answers).                               |
| 3-2  | `01`, SME Delivery Acknowledgement            | Accepted, not honoured.                                       |
| 4    | `1`, intermediate notification                | Accepted, not honoured.                                       |

### Validity and scheduling

| Field                    | Handling                                                                                                                                                                                                                                                                            |
| ------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `validity_period`        | Empty: the default validity of the [delivery settings](https://docs.ellanetworks.com/smsc/reference/api/delivery/index.md). Absolute or relative (§7.1.1): the end of the validity period. In the past or malformed: `ESME_RINVEXPIRY`. The SMS-DELIVER carries no validity period. |
| `schedule_delivery_time` | Not empty: `ESME_RINVSCHED`.                                                                                                                                                                                                                                                        |

A two-digit year is read within 49 years before and 50 years after the current year. **Convention**.

### User data and optional parameters

| Field                                                         | Handling                                                                                                                                                                                                                                        |
| ------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `short_message` (§5.2.22), `message_payload` (§5.3.2.32)      | One or the other. Both: `ESME_ROPTPARNOTALLWD`.                                                                                                                                                                                                 |
| `sm_length`                                                   | Must match `short_message`. `255`, or a mismatch: `ESME_RINVMSGLEN`.                                                                                                                                                                            |
| User data                                                     | Must fit one TPDU once mapped: 140 octets, or 160 septets, the user data header included. Else `ESME_RINVMSGLEN`. The SMSC does not split user data.                                                                                            |
| `sar_msg_ref_num`, `sar_total_segments`, `sar_segment_seqnum` | All three: a concatenation information element, `0x00` with a reference up to 255, else `0x08` (TS 23.040 §9.2.3.24.1, §9.2.3.24.8). Not all three: ignored. A total of 0, or a sequence number of 0 or over the total: `ESME_RINVOPTPARAMVAL`. |
| `source_port`, `destination_port`                             | A 16-bit application port addressing information element, `0x05` (TS 23.040 §9.2.3.24.4). A missing one of the two is port 0. **Convention**.                                                                                                   |
| `sar_*` or ports with UDHI set                                | `ESME_ROPTPARNOTALLWD` (§5.2.12).                                                                                                                                                                                                               |
| `user_message_reference`                                      | Echoed in the receipt and the SIM answer.                                                                                                                                                                                                       |
| `sm_default_msg_id`                                           | `1` to `254`: `ESME_RINVDFTMSGID`. The SMSC has no canned messages.                                                                                                                                                                             |
| `replace_if_present_flag`                                     | `1`: `ESME_RINVSUBREP`. Other values: taken as `0`.                                                                                                                                                                                             |
| `service_type`, `priority_flag`, other optional parameters    | Ignored.                                                                                                                                                                                                                                        |

### Order of `submit_sm_resp` and `deliver_sm`

The message is handed to delivery once `submit_sm_resp` is written. A notification on the message can still reach the ESME before its `submit_sm_resp` when another message to the same recipient is being delivered.

## Notifications

When a message from an ESME reaches its final status, the SMSC queues the SIM answer, then the receipt, that the `submit_sm` asked for, and sends them as `deliver_sm`.

| Item                                                                                         | Handling                                                                                                                                                                                   |
| -------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Session                                                                                      | A receiver or transceiver session of the ESME that submitted the message, the one with the fewest `deliver_sm` in flight. **Convention**.                                                  |
| No receiving session                                                                         | Notifications wait until the ESME binds as receiver or transceiver. They survive a restart.                                                                                                |
| Order                                                                                        | Notifications on one message go one at a time: the receipt waits until the SIM answer is acknowledged, rejected or expired.                                                                |
| `deliver_sm_resp` `ESME_ROK`                                                                 | Acknowledged.                                                                                                                                                                              |
| `deliver_sm_resp` `ESME_RX_P_APPN` or `ESME_RX_R_APPN`                                       | Rejected. Not sent again.                                                                                                                                                                  |
| `ESME_RX_T_APPN`, another status, `generic_nack`, or no response within the `response_timer` | Sent again after 1 minute, then doubling, at most every hour.                                                                                                                              |
| Session closed or unbinding during the send                                                  | Sent again at once on another receiving session, or on the next bind.                                                                                                                      |
| Expiry                                                                                       | The default validity of the [delivery settings](https://docs.ellanetworks.com/smsc/reference/api/delivery/index.md), from the message's final status. An expired notification is not sent. |
| ESME deleted                                                                                 | Its notifications are dropped.                                                                                                                                                             |

Notifications are delivered at least once. The SIM answer and the receipt of a message share its `receipted_message_id`, and differ by the message type of `esm_class`.

### Delivery receipts

A receipt is an SMSC Delivery Receipt (§2.11, §4.6.1, Appendix B):

| Field                                                 | Value                                         |
| ----------------------------------------------------- | --------------------------------------------- |
| `source_addr_ton`, `source_addr_npi`, `source_addr`   | The destination of the message, as submitted. |
| `dest_addr_ton`, `dest_addr_npi`, `destination_addr`  | The source of the message, as submitted.      |
| `esm_class`                                           | `0x04`, SMSC Delivery Receipt.                |
| `protocol_id`, `priority_flag`, `registered_delivery` | `0`.                                          |
| `data_coding`                                         | `0x01`, IA5. **Convention**.                  |
| `short_message`                                       | The receipt text.                             |
| `receipted_message_id` (`0x001E`)                     | The `message_id`.                             |
| `message_state` (`0x0427`)                            | See the table below.                          |
| `user_message_reference` (`0x0204`)                   | That of the `submit_sm`, when present.        |

The text follows the example format of Appendix B:

```
id:42 sub:001 dlvrd:001 submit date:2610281430 done date:2610281431 stat:DELIVRD err:000 text:
```

| Field         | Value                                                                                                                       |
| ------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `id`          | The `message_id`, not padded.                                                                                               |
| `sub`         | `001`.                                                                                                                      |
| `dlvrd`       | `001` for a delivered message, else `000`.                                                                                  |
| `submit date` | When the SMSC received the message, `YYMMDDhhmm`, UTC.                                                                      |
| `done date`   | When the message reached its final status, `YYMMDDhhmm`, UTC. For a replaced message, when it was replaced. **Convention**. |
| `stat`, `err` | See the table below.                                                                                                        |
| `text`        | Empty.                                                                                                                      |

| Message status | `stat`    | `message_state` (§5.2.28) | `err`                                                         |
| -------------- | --------- | ------------------------- | ------------------------------------------------------------- |
| `delivered`    | `DELIVRD` | `2`                       | `000`                                                         |
| `expired`      | `EXPIRED` | `3`                       | `070`, TP-Status `0x46`                                       |
| `replaced`     | `DELETED` | `4`                       | `002`, TP-Status `0x02`                                       |
| `failed`       | `UNDELIV` | `5`                       | The TP-Status of the failure, such as `066`, TP-Status `0x42` |

`err` is the TP-Status (TS 23.040 §9.2.3.15), in decimal. **Convention**. `network_error_code` is not sent.

### SIM answers

The SIM answer is the TP-User-Data of the SMS-DELIVER-REPORT of the last delivery attempt (TS 23.040 §9.2.2.1a), from the RP-ACK, or from the RP-ERROR of an SM delivery failure. For a (U)SIM data download message, it is the SIM's Response Packet (TS 31.115 §4.4; TS 31.111 §7.1.1.1).

It is sent when `registered_delivery` bits 3-2 are `10` or `11`, for any `protocol_id`, as an SME Manual/User Acknowledgement (§2.11). **Convention**.

No SIM answer is sent for a replaced message, or when the SMS-DELIVER-REPORT has no TP-User-Data.

| Field                    | Value                                                                                                                                                      |
| ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Addresses                | As in a receipt.                                                                                                                                           |
| `esm_class` message type | `0100`, SME Manual/User Acknowledgement. **Convention**.                                                                                                   |
| `esm_class` UDHI         | `1` when the report has TP-UDHI, or when the message's TP-PID is `0x7F` and the user data starts with a valid user data header (TS 31.115 §4.1). Else `0`. |
| `esm_class`, typical     | `0x50`                                                                                                                                                     |
| `protocol_id`            | The report's TP-PID, else the message's.                                                                                                                   |
| `data_coding`            | See the table below.                                                                                                                                       |
| `short_message`          | The TP-User-Data, its user data header included, such as `027100…`.                                                                                        |
| `receipted_message_id`   | The `message_id`. **Convention**: §5.3.2.12 defines it for receipts.                                                                                       |
| `message_state`          | Absent.                                                                                                                                                    |
| `user_message_reference` | That of the `submit_sm`, when present.                                                                                                                     |

| Report TP-DCS                                 | `data_coding`                                                                      | `short_message`         |
| --------------------------------------------- | ---------------------------------------------------------------------------------- | ----------------------- |
| GSM 7-bit, or no TP-DCS (TS 23.040 §9.2.3.27) | `0x00`                                                                             | Septets, one per octet. |
| UCS2                                          | `0x08`                                                                             | The octets.             |
| 8-bit                                         | That of the `submit_sm` when it is `0x02`, `0x04` or `0xF4` to `0xF7`, else `0x04` | The octets.             |

A SIM that rejects a (U)SIM data download message answers with an RP-ERROR whose TP-Failure-Cause is `0xD5`, (U)SIM data download error. The message fails at once with TP-Status `0x42`: the ESME gets the SIM answer, then the receipt `stat:UNDELIV err:066`.

## `command_status` values

Values the SMSC sends (§5.1.3):

| `command_status`        | Value        | Sent in          | When                                                                                                         |
| ----------------------- | ------------ | ---------------- | ------------------------------------------------------------------------------------------------------------ |
| `ESME_ROK`              | `0x00000000` | Any response     | Success.                                                                                                     |
| `ESME_RINVMSGLEN`       | `0x00000001` | `submit_sm_resp` | `sm_length`, or user data longer than one TPDU.                                                              |
| `ESME_RINVCMDLEN`       | `0x00000002` | `generic_nack`   | `command_length`, or a body of the wrong length.                                                             |
| `ESME_RINVCMDID`        | `0x00000003` | `generic_nack`   | Unsupported or undefined `command_id`.                                                                       |
| `ESME_RINVBNDSTS`       | `0x00000004` | Response         | A request that the session state does not allow.                                                             |
| `ESME_RALYBND`          | `0x00000005` | Bind response    | A bind in a bound state.                                                                                     |
| `ESME_RSYSERR`          | `0x00000008` | `submit_sm_resp` | The message could not be stored or encoded.                                                                  |
| `ESME_RINVSRCADR`       | `0x0000000A` | `submit_sm_resp` | `source_addr`.                                                                                               |
| `ESME_RINVDSTADR`       | `0x0000000B` | `submit_sm_resp` | `destination_addr`.                                                                                          |
| `ESME_RBINDFAIL`        | `0x0000000D` | Bind response    | Any refused bind.                                                                                            |
| `ESME_RINVSUBREP`       | `0x00000042` | `submit_sm_resp` | `replace_if_present_flag` `1`.                                                                               |
| `ESME_RINVESMCLASS`     | `0x00000043` | `submit_sm_resp` | `esm_class`.                                                                                                 |
| `ESME_RSUBMITFAIL`      | `0x00000045` | `submit_sm_resp` | `data_coding`, `protocol_id`, the user data header, or GSM 7-bit, IA5 or UCS2 user data.                     |
| `ESME_RINVSRCTON`       | `0x00000048` | `submit_sm_resp` | `source_addr_ton`.                                                                                           |
| `ESME_RINVSRCNPI`       | `0x00000049` | `submit_sm_resp` | `source_addr_npi`.                                                                                           |
| `ESME_RINVDSTTON`       | `0x00000050` | `submit_sm_resp` | `dest_addr_ton`.                                                                                             |
| `ESME_RINVDSTNPI`       | `0x00000051` | `submit_sm_resp` | `dest_addr_npi`.                                                                                             |
| `ESME_RTHROTTLED`       | `0x00000058` | `submit_sm_resp` | Inbound window full.                                                                                         |
| `ESME_RINVSCHED`        | `0x00000061` | `submit_sm_resp` | `schedule_delivery_time`.                                                                                    |
| `ESME_RINVEXPIRY`       | `0x00000062` | `submit_sm_resp` | `validity_period`.                                                                                           |
| `ESME_RINVDFTMSGID`     | `0x00000063` | `submit_sm_resp` | `sm_default_msg_id`.                                                                                         |
| `ESME_RINVOPTPARSTREAM` | `0x000000C0` | Response         | Optional parameters that do not decode.                                                                      |
| `ESME_ROPTPARNOTALLWD`  | `0x000000C1` | `submit_sm_resp` | `short_message` with `message_payload`; `sar_*` or ports with UDHI.                                          |
| `ESME_RINVPARLEN`       | `0x000000C2` | Response         | A field over its maximum length, or a TLV of the wrong length.                                               |
| `ESME_RINVOPTPARAMVAL`  | `0x000000C4` | `submit_sm_resp` | `sar_*` values; `dest_addr_subunit` against `data_coding`; a TLV that should be a C-Octet String and is not. |
| `ESME_RUNKNOWNERR`      | `0x000000FF` | `generic_nack`   | Invalid `sequence_number`.                                                                                   |

An error response has no body (§4.1.2, §4.1.4, §4.4.2), except `bind_transceiver_resp`, which carries `system_id` (Table 4-6). The SMSC logs the reason of a refused `submit_sm` at level `debug`, with the `system_id`.

Values the SMSC acts on in `deliver_sm_resp`: see [Notifications](#notifications).

## Conventions

Behaviours that SMPP 3.4 does not define:

| Behaviour                                                                                | Where                                                                   |
| ---------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| TLS from the first octet.                                                                | [Transport](#transport)                                                 |
| A maximum `command_length` of 68 KiB.                                                    | [Transport](#transport)                                                 |
| `data_coding` `0x00` is the GSM 7-bit default alphabet, unpacked.                        | [`data_coding`](#data_coding)                                           |
| `data_coding` `0x02` or `0x04` with `protocol_id` `0x7F` becomes TP-DCS `0xF6`, class 2. | [`data_coding`](#data_coding)                                           |
| A replace type `protocol_id` replaces only messages of the same ESME.                    | [`protocol_id`](#protocol_id)                                           |
| A two-digit year is within 49 years before and 50 years after the current year.          | [Validity and scheduling](#validity-and-scheduling)                     |
| A missing `source_port` or `destination_port` is port 0.                                 | [User data and optional parameters](#user-data-and-optional-parameters) |
| Notifications go to a receiving session of the ESME that submitted the message.          | [Notifications](#notifications)                                         |
| Receipts use `data_coding` `0x01`.                                                       | [Delivery receipts](#delivery-receipts)                                 |
| A replaced message's receipt is `DELETED`, with `done date` the replacement time.        | [Delivery receipts](#delivery-receipts)                                 |
| Receipt `err` is the TP-Status in decimal.                                               | [Delivery receipts](#delivery-receipts)                                 |
| The SIM's answer is an SME Manual/User Acknowledgement, with `receipted_message_id`.     | [SIM answers](#sim-answers)                                             |

## Not supported

- SMPP 3.3 and earlier, and SMPP 5.0.
- `query_sm`, `replace_sm`, `cancel_sm`, `submit_multi`, `data_sm`, `alert_notification` and `outbind`.
- Messages from phones to ESMEs, such as a Response Packet that the SIM sends in an SMS-SUBMIT (TS 31.115 §4.2).
- Splitting user data longer than one TPDU.
- Scheduled delivery, the datagram and transaction modes, SME delivery acknowledgements and intermediate notifications.
- The PROXY protocol.
