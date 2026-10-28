---
description: Reference of the SMPP 3.4 interface of Ella SMSC for ESMEs, such as OTA platforms.
---

# SMPP

Ella SMSC is an SMPP 3.4 SMSC for External Short Message Entities (ESMEs), such as an OTA platform. An ESME submits short messages to phones with `submit_sm`, and receives delivery receipts and the SIM's answers as `deliver_sm`.

The listener runs when the [configuration file](config_file.md) has an `smpp` block. ESME accounts are managed with the [ESMEs API](api/esmes.md) or the **SMPP** page of the UI.

"§" without a TS number refers to SMPP v3.4, Issue 1.2. Rows marked **Convention** are Ella SMSC's choices where SMPP 3.4 has no rule.

## Transport

| Item | Value |
| ---- | ----- |
| Transport | TCP (§2.4). Port `smpp.port`, default `2775`, the IANA "smpp" port. |
| TLS | With `smpp.tls`, TLS 1.2 or later from the first octet. **Convention**: SMPP 3.4 has no TLS. One listener is either plain or TLS. |
| `interface_version` | `0x34`. A bind of an earlier version is refused. |
| SMSC `system_id` | `EllaSMSC`, in bind responses. |
| `command_length` | 16 to 69,632 octets (68 KiB). Ella SMSC's limit: SMPP 3.4 sets none (§3.2.2). |

## Supported PDUs

| PDU | Direction | State | Handling |
| --- | --------- | ----- | -------- |
| `bind_transmitter`, `bind_receiver`, `bind_transceiver` | ESME → SMSC | OPEN | [Bind checks](#binds). Success: `system_id` `EllaSMSC` and `sc_interface_version` `0x34` (§5.3.2.25). Any failure: `ESME_RBINDFAIL`. In a bound state: `ESME_RALYBND`. The `address_range` of `bind_receiver` is ignored. |
| `submit_sm` | ESME → SMSC | BOUND_TX, BOUND_TRX | [Mapped to one SMS-SUBMIT](#submit_sm). `message_id` is the decimal message ID of the [Messages API](api/messages.md). In another state: `ESME_RINVBNDSTS`. |
| `deliver_sm` | SMSC → ESME | BOUND_RX, BOUND_TRX | [Delivery receipts](#delivery-receipts) and [SIM answers](#sim-answers). |
| `enquire_link` | Both | Bound | Answered at once. In OPEN: `ESME_RINVBNDSTS`. |
| `unbind` | Both | Bound | The SMSC finishes the `submit_sm` in progress, answers `unbind_resp`, and closes. |
| `generic_nack` | Both | Any | Sent for an unknown or unsupported command, or an invalid `command_length` (§3.3). Received for a `deliver_sm`: a temporary failure. |

| Not supported | Response |
| ------------- | -------- |
| `query_sm`, `replace_sm`, `cancel_sm`, `submit_multi`, `data_sm`, `alert_notification`, `outbind`, reserved command IDs | `generic_nack` `ESME_RINVCMDID`. The session stays up. |
| `command_length` under 16 or over 68 KiB | `generic_nack` `ESME_RINVCMDLEN`, then the SMSC closes the session. |
| A body on a PDU that has none, or a body shorter than its mandatory parameters | `generic_nack` `ESME_RINVCMDLEN`. The session stays up. |
| Optional parameters that do not decode as TLVs | Its response with `ESME_RINVOPTPARSTREAM`. |
| A field over its maximum length | Its response with `ESME_RINVPARLEN`. |

Unknown TLVs, and known TLVs not expected for the PDU, are ignored (§3.3). Responses may go out of order: they match their requests by `sequence_number`.

## Binds

The SMSC checks a bind in this order:

1. At most 20 binds per minute from the client address, or its IPv6 /64.
2. `interface_version` is `0x34` or later.
3. At most 100 wrong passwords per hour for the `system_id`. A successful bind resets the count.
4. `system_id` and `password` match an ESME account. The password is checked against a bcrypt hash; an unknown `system_id` takes as long.
5. The client address is in the account's `allowed_networks`. An empty list allows no address.
6. The role of the account's user allows the bind type:

    | Bind | Permissions | Roles |
    | ---- | ----------- | ----- |
    | Transmitter | Send messages | Admin, Network Manager, Submitter |
    | Receiver | Read messages and their content | Admin, Network Manager, Submitter |
    | Transceiver | Both | Admin, Network Manager, Submitter |

    A Read Only user's ESME cannot bind. See [Roles](api/index.md#roles).

7. Fewer than `max_sessions` sessions of the account are bound.

Every failure is `ESME_RBINDFAIL`, never `ESME_RINVSYSID` or `ESME_RINVPASWD`, so the response does not tell which check failed. The log gives the reason, the `system_id` and the client address.

Updating or deleting an ESME, or changing or deleting its user, closes its sessions. The ESME then binds again through these checks.

## Timers and windows

| Item | Value | Action |
| ---- | ----- | ------ |
| `session_init_timer` | 30 s | A connection that has not bound is closed. |
| `enquire_link_timer` | 30 s without a PDU received | The SMSC sends `enquire_link`, and closes the session if it gets no response within the `response_timer`. |
| `inactivity_timer` | 90 s without a PDU received | The session is closed. |
| `response_timer` | 30 s | A `deliver_sm` without a response is a temporary failure. |
| Inbound window | 64 requests | Handled at once per session (SMPP 3.4 §2.5.2 suggests 10; the database serializes writes, so the window only overlaps parsing and I/O). One more is answered `ESME_RTHROTTLED`. |
| Outbound window | 10 `deliver_sm` | Unacknowledged per session (§2.6.2 guideline). |
| Shutdown | 5 s | The SMSC sends `unbind` to each session and waits for `unbind_resp`, then closes. |

SMPP 3.4 leaves the timer values to the SMSC (§7.2). They are not configurable.

## `submit_sm`

One `submit_sm` stores exactly one SMS-SUBMIT, one message of the [Messages API](api/messages.md), owned by the ESME's user. The SMSC delivers it as it does a message from the API. It does not split long user data: concatenation is the ESME's, with a UDH or with `sar_*`.

### Addressing

The destination is a phone, routed through the HSS (§5.2.5, §5.2.6, §5.2.9; TS 23.040 §9.1.2.5):

| `dest_addr_ton` | `dest_addr_npi` | `destination_addr` | Error |
| --------------- | --------------- | ------------------ | ----- |
| `1`, international | `0` or `1`, E.164 | The international number | |
| `0`, unknown, or `2`, national | `0` or `1` | Converted to international with the operator's numbering plan, as for phones | |
| Other | | | `ESME_RINVDSTTON` |
| | Other | | `ESME_RINVDSTNPI` |
| | | Not 1 to 15 digits without a leading 0 once international | `ESME_RINVDSTADR` |

The source is shown to the recipient as the sender (§5.2.8):

| `source_addr_ton` | `source_addr_npi` | `source_addr` | Stored as |
| ----------------- | ----------------- | ------------- | --------- |
| `1`, international | `0` or `1` | 1 to 15 digits, no leading 0 | TON 1, NPI 1 |
| `5`, alphanumeric | Any | 1 to 11 letters and digits, single spaces between words | TON 5 |
| `0`, `2`, `3`, `4`, `6`, such as a short code | `0`, `1`, `3`, `4`, `8`, `9` or `10` | 1 to 20 digits | The TON and NPI given |

An empty `source_addr` is `ESME_RINVSRCADR`: the SMSC has no default source. A reserved TON is `ESME_RINVSRCTON`, another NPI `ESME_RINVSRCNPI`, and an invalid address `ESME_RINVSRCADR`. The NPIs accepted are those with the same value and meaning in SMPP 3.4 Table 5-4 and TS 23.040 §9.1.2.5; SMPP's `6`, E.212, is not one.

Receipts and SIM answers use the destination as submitted as their source, and the source as their destination (§2.11).

### `data_coding`

SMPP 3.4 reuses some GSM codepoints of TS 23.038 §4 and redefines others, so `data_coding` is mapped, not copied (§5.2.19):

| `data_coding` | SMPP 3.4 meaning | TP-DCS and user data |
| ------------- | ---------------- | -------------------- |
| `0x00` | SMSC default alphabet | Per the ESME's `default_alphabet`. `gsm7`: GSM 7-bit default alphabet septets, one per octet, `0x1B` for the extension table; an octet over `0x7F` is `ESME_RSUBMITFAIL`. TP-DCS `0x00`. `latin1`: as `0x03`. **Convention**: SMPP 3.4 leaves `0x00` to the SMSC vendor. |
| `0x01` | IA5 (ASCII) | Converted to GSM 7-bit, TP-DCS `0x00`, or to UCS2, TP-DCS `0x08`, when a character has no GSM 7-bit equivalent. An octet over `0x7F` is `ESME_RSUBMITFAIL`. |
| `0x02` | Octet unspecified (§5.2.19 note b, a GSM reserved codepoint) | 8-bit, TP-DCS `0x04`. |
| `0x03` | Latin-1 | As `0x01`. |
| `0x04` | Octet unspecified | 8-bit, TP-DCS `0x04`. |
| `0x08` | UCS2 | TP-DCS `0x08`, octets copied. An odd number of octets is `ESME_RSUBMITFAIL`. |
| `0xC0` to `0xDF` | GSM message waiting indication | Copied, GSM 7-bit. Bit 2 set, reserved in TS 23.038 §4, is `ESME_RSUBMITFAIL`. |
| `0xF0` to `0xF7` | GSM message class control | Copied. For example `0xF6`, 8-bit data, class 2. |
| `0x05` to `0x07`, `0x09` to `0xBF`, `0xE0` to `0xEF`, `0xF8` to `0xFF` | Other character sets, or reserved | `ESME_RSUBMITFAIL`. SMPP 3.4 has no status for an invalid `data_coding`. |

**`dest_addr_subunit`** (§5.3.2.1), which §5.2.19 note e recommends for the message class: `1` MS display, class 0; `2` mobile equipment, class 1; `3` smart card, class 2; `4` external unit, class 3. It sets the class bits of TP-DCS. A different class already in `data_coding`, or a coding group without class bits, is `ESME_RINVOPTPARAMVAL`. `0` and reserved values change nothing.

**(U)SIM data download.** With `protocol_id` `0x7F`, `data_coding` `0x02` or `0x04` and no `dest_addr_subunit`, TP-DCS is `0xF6`, 8-bit data, class 2. **Convention**: TS 23.040 §9.2.3.9 requires class 2 for (U)SIM data download, and SMPP 3.4 has no class with `0x02` or `0x04`. pySim-smpp2sim does the same.

With a GSM 7-bit TP-DCS, the SMSC packs the septets, after the UDH with fill bits (TS 23.040 §9.2.3.24).

### `esm_class`

| Bits (§5.2.12) | Value | Handling |
| -------------- | ----- | -------- |
| 1-0, messaging mode | `00` default, `11` store and forward | Accepted. Ella SMSC is store and forward. |
| 1-0 | `01` datagram, `10` transaction | `ESME_RINVESMCLASS` |
| 5-2, message type | `0010`, `0100`, ESME acknowledgements | `ESME_RINVESMCLASS`: Ella SMSC does not route phones' messages to ESMEs. |
| 5-2 | Other | Taken as `0000`, normal message (§3.3). |
| 6, UDHI | `1` | TP-UDHI = 1. The user data starts with the UDH, its length octet included. |
| 7, reply path | `1` | `ESME_RINVESMCLASS` |

### `protocol_id`

Copied to TP-PID (§5.2.13), with the rules of the [Messages API](api/messages.md#create-a-message): telematic interworking (`0x20` to `0x3E`) is `ESME_RSUBMITFAIL`; `0x7C` and `0x7F` need class 2 after the `data_coding` mapping, else `ESME_RSUBMITFAIL`; device triggering (`0x48`) needs an application port addressing information element, else `ESME_RSUBMITFAIL`; replace types `0x41` to `0x47` replace the pending message of the same type from the same source (TS 23.040 §9.2.3.9).

### `registered_delivery`

`registered_delivery` asks the SMSC for notifications to the ESME (§5.2.17). It does not set TP-Status-Report-Request: the phone sends no status report.

| Bits | Value | Handling |
| ---- | ----- | -------- |
| 1-0 | `00` | No receipt. |
| 1-0 | `01` | A [receipt](#delivery-receipts) on the final outcome. |
| 1-0 | `10` | A receipt on failure only: `failed`, `expired` or `replaced`. |
| 1-0 | `11` | Reserved: no receipt (§3.3). |
| 3-2 | `10` or `11`, SME Manual/User Acknowledgement | The [SIM answer](#sim-answers) is forwarded. |
| 3-2 | `01`, SME Delivery Acknowledgement | Accepted, not honoured: it means the user read the message (§2.11), which the network does not report. |
| 4 | Intermediate notification | Accepted, not honoured. |

### Validity and scheduling

| Field | Handling |
| ----- | -------- |
| `validity_period` | Empty: the default validity of the [delivery settings](api/delivery.md). Absolute or relative (§7.1.1): the end of the validity period. In the past, malformed, or with a quarter-hour offset over 48: `ESME_RINVEXPIRY`. The SMSC enforces it; the SMS-DELIVER carries none. |
| `schedule_delivery_time` | Not empty: `ESME_RINVSCHED`. |

**Convention**: a two-digit year is read within 49 years before and 50 years after the current year. §7.1.1 has no century rule.

### User data, `sar_*` and ports

| Field | Handling |
| ----- | -------- |
| `short_message` (§5.2.22), `message_payload` (§5.3.2.32) | One or the other. Both: `ESME_ROPTPARNOTALLWD`. |
| `sm_length` | Must match `short_message`, else `ESME_RINVMSGLEN`. |
| User data | Must fit one TPDU once mapped: 140 octets, or 160 septets, the UDH included. Else `ESME_RINVMSGLEN`. |
| `sar_msg_ref_num`, `sar_total_segments`, `sar_segment_seqnum` | All three: a concatenation information element, `0x00` with a reference up to 255, else `0x08` (TS 23.040 §9.2.3.24.1, §9.2.3.24.8). Not all three: ignored. A total of 0, or a sequence number of 0 or over the total: `ESME_RINVOPTPARAMVAL`. |
| `source_port`, `destination_port` | A 16-bit application port addressing information element, `0x05` (TS 23.040 §9.2.3.24.4). **Convention**: a missing one of the two is port 0. |
| `sar_*` or ports with UDHI set | `ESME_ROPTPARNOTALLWD` (§5.2.12 notes). |
| `user_message_reference` | Echoed in the receipt and the SIM answer. |
| `sm_default_msg_id` | `1` to `254`: `ESME_RINVDFTMSGID`. Ella SMSC has no canned messages. |
| `replace_if_present_flag` | `1`: `ESME_RINVSUBREP`. Use a replace type `protocol_id` instead. Other values are taken as `0`. |
| `priority_flag`, `service_type` | Ignored. |

### `command_status`

| Condition | `command_status` |
| --------- | ---------------- |
| Stored | `ESME_ROK` (`0x00000000`), with `message_id` |
| Length of the user data, `sm_length` | `ESME_RINVMSGLEN` (`0x00000001`) |
| Not bound as transmitter or transceiver | `ESME_RINVBNDSTS` (`0x00000004`) |
| Store failed | `ESME_RSYSERR` (`0x00000008`) |
| `source_addr` | `ESME_RINVSRCADR` (`0x0000000A`) |
| `destination_addr` | `ESME_RINVDSTADR` (`0x0000000B`) |
| `replace_if_present_flag` `1` | `ESME_RINVSUBREP` (`0x00000042`) |
| `esm_class` | `ESME_RINVESMCLASS` (`0x00000043`) |
| `data_coding`, `protocol_id`, UDH, or GSM 7-bit, IA5 or UCS2 content | `ESME_RSUBMITFAIL` (`0x00000045`) |
| `source_addr_ton` | `ESME_RINVSRCTON` (`0x00000048`) |
| `source_addr_npi` | `ESME_RINVSRCNPI` (`0x00000049`) |
| `dest_addr_ton` | `ESME_RINVDSTTON` (`0x00000050`) |
| `dest_addr_npi` | `ESME_RINVDSTNPI` (`0x00000051`) |
| Inbound window full | `ESME_RTHROTTLED` (`0x00000058`) |
| `schedule_delivery_time` | `ESME_RINVSCHED` (`0x00000061`) |
| `validity_period` | `ESME_RINVEXPIRY` (`0x00000062`) |
| `sm_default_msg_id` | `ESME_RINVDFTMSGID` (`0x00000063`) |
| Optional parameters that do not decode | `ESME_RINVOPTPARSTREAM` (`0x000000C0`) |
| `short_message` and `message_payload`; `sar_*` or ports with UDHI | `ESME_ROPTPARNOTALLWD` (`0x000000C1`) |
| A field over its maximum length | `ESME_RINVPARLEN` (`0x000000C2`) |
| `sar_*` values, `dest_addr_subunit` against `data_coding` | `ESME_RINVOPTPARAMVAL` (`0x000000C4`) |

An error `submit_sm_resp` has no body (§4.4.2). The SMSC logs the reason at level `debug`, with the `system_id`.

## Notifications

When a message from an ESME ends, the SMSC queues the SIM answer, then the receipt, that the ESME asked for, and sends them as `deliver_sm`.

| Item | Handling |
| ---- | -------- |
| Session | A receiver or transceiver session of the ESME that submitted the message, the least busy one. **Convention**: SMPP 3.4 does not say which session a `deliver_sm` goes to. |
| Unbound | Notifications wait, in the database, until the ESME binds as receiver or transceiver. They survive a restart. |
| Order | Notifications on one message go one at a time: the receipt waits until the SIM answer is acknowledged, rejected or expired. |
| `deliver_sm_resp` `ESME_ROK` | Acknowledged. |
| `ESME_RX_P_APPN`, `ESME_RX_R_APPN` | Rejected, not sent again. |
| `ESME_RX_T_APPN`, other errors, `generic_nack`, no response within the `response_timer`, session lost | Sent again after 1 minute, doubling, at most 1 hour. |
| Expiry | The default validity of the [delivery settings](api/delivery.md) after the message ended. An expired notification is not sent. |

Delivery is at least once: the ESME removes duplicates by `receipted_message_id`.

### Delivery receipts

A receipt is an SMSC Delivery Receipt (§2.11, §4.6.1, Appendix B):

| Field | Value |
| ----- | ----- |
| `source_addr_ton`, `source_addr_npi`, `source_addr` | The destination of the message, as submitted. |
| `dest_addr_ton`, `dest_addr_npi`, `destination_addr` | The source of the message. |
| `esm_class` | `0x04`, SMSC Delivery Receipt. |
| `protocol_id`, `priority_flag`, `registered_delivery` | `0`. |
| `data_coding` | `0x01`, IA5. **Convention**: it does not depend on what the ESME takes `0x00` for. |
| `short_message` | The receipt text, below. |
| `receipted_message_id` (`0x001E`) | The `message_id`. |
| `message_state` (`0x0427`) | Below. |
| `user_message_reference` (`0x0204`) | That of the `submit_sm`, when it had one. |

The text follows the example of Appendix B, which is vendor specific:

```
id:42 sub:001 dlvrd:001 submit date:2610281430 done date:2610281431 stat:DELIVRD err:000 text:
```

- `id` is the `message_id`, not padded.
- `dlvrd` is `001` for a delivered message, else `000`.
- `submit date` is when the SMSC received the message, or, for a replaced message, when it was replaced (Appendix B, Table B-1). `done date` is when it ended. Both are `YYMMDDhhmm`, in UTC.
- `text:` is empty: the content may be binary.

| Message status | `stat` | `message_state` (§5.2.28) | `err` |
| -------------- | ------ | ------------------------- | ----- |
| `delivered` | `DELIVRD` | `2` | `000` |
| `expired` | `EXPIRED` | `3` | `070`, TP-Status `0x46` |
| `failed` | `UNDELIV` | `5` | The TP-Status of the failure, such as `066`, TP-Status `0x42`, for a (U)SIM data download error |
| `replaced` | `DELETED` | `4` | `002`, TP-Status `0x02` |

**Convention**: `err` is the TP-Status (TS 23.040 §9.2.3.15) in decimal. Appendix B allows a "network specific error code or an SMSC error code". `network_error_code` is not sent.

### SIM answers

The SIM answer is the TP-User-Data of the SMS-DELIVER-REPORT of the last delivery attempt (TS 23.040 §9.2.2.1a): from the RP-ACK, or from the RP-ERROR of an SM delivery failure. For a (U)SIM data download message, it carries the SIM's Response Packet (TS 31.115 §4.4, TS 31.111 §7.1.1.1).

It is forwarded when either:

- `registered_delivery` bits 3-2 are `10` or `11`, an SME Manual/User Acknowledgement, for any `protocol_id`. **Convention**: SMPP 3.4 defines it as "an application generated reply message" (§2.11); Ella SMSC reads the Response Packet as one;
- the ESME's `forward_sim_answers` is on, for `protocol_id` `0x7F`. **Convention**, for ESMEs that do not set those bits, such as pySim's `smpp-ota-tool.py`.

A report without TP-User-Data gives no answer. A replaced message gets none.

| Field | Receipt | SIM answer |
| ----- | ------- | ---------- |
| `esm_class` message type | `0001`, SMSC Delivery Receipt | `0100`, SME Manual/User Acknowledgement. **Convention**. |
| `esm_class` UDHI | `0` | `1` when the report has TP-UDHI, or the message's TP-PID is `0x7F`. A SIM does not set TP-UDHI on a Response Packet, which is read as if it were set (TS 31.115 §4.1). |
| Typical `esm_class` | `0x04` | `0x50` |
| `short_message` | Receipt text | The TP-User-Data, its UDH included, such as `027100…` |
| `protocol_id` | `0` | The report's TP-PID, else the message's |
| `data_coding` | `0x01` | 8-bit: the `submit_sm`'s `data_coding` when it is `0x02`, `0x04` or `0xF4` to `0xF7`, else `0x04`. UCS2: `0x08`. GSM 7-bit: septets one per octet, `0x00`, or UCS2, `0x08`, for an ESME whose `default_alphabet` is `latin1`. A report without TP-DCS is GSM 7-bit (TS 23.040 §9.2.3.27). |
| `receipted_message_id` | The `message_id` | The `message_id`. **Convention**: §5.3.2.12 defines it for receipts only, and an ESME ignores a TLV it does not expect (§3.3). |
| `message_state` | Present | Absent |
| `user_message_reference` | Echoed | Echoed |
| Addresses | Swapped (§2.11) | Swapped. **Convention**. |

Without `receipted_message_id`, an ESME correlates the answer with the TAR and counter of the Response Packet (TS 31.115 §4.4).

A SIM that rejects a (U)SIM data download message answers with an RP-ERROR whose TP-Failure-Cause is `0xD5`. The message fails at once with TP-Status `0x42`: the ESME gets the answer, then, if it asked, the receipt `stat:UNDELIV err:066`.

## Not supported

- The PDUs listed under [Supported PDUs](#supported-pdus) as not supported.
- SMPP 3.3 and earlier, SMPP 5.0, and outbind.
- Messages from phones to ESMEs, such as a Response Packet that the SIM sends in an SMS-SUBMIT (TS 31.115 §4.2).
- Splitting by the SMSC of user data longer than one TPDU.
- Scheduled delivery, the datagram and transaction modes, and intermediate notifications.
- The PROXY protocol: the client address of a session is that of the TCP connection.
