---
description: Reference of the 3GPP specifications Ella SMSC implements.
---

# 3GPP Compliance

Ella SMSC implements the 3GPP SMSC of TS 23.040, with the S6c and SGd Diameter interfaces of TS 29.338.

!!! note

    Open an [issue](https://github.com/ellanetworks/smsc/issues) if you notice a 3GPP-compliance gap or require a new capability for a production deployment.

## Supported

### Interfaces

| Interface | Peer | Transport |
| --------- | ---- | --------- |
| S6c | HSS | Diameter over SCTP |
| SGd | MME, SGSN, SMSF, IP-SM-GW | Diameter over SCTP |

### SGd (TS 29.338)

- **MO-Forward-Short-Message**, received: a phone's short message, from its MME, SMSF or IP-SM-GW.
- **MT-Forward-Short-Message**, sent: a short message or a status report, to the phone's MME, SGSN, IP-SM-GW, or SMSF for 3GPP or non-3GPP access.

### S6c (TS 29.338)

- **Send-Routing-Info-for-SM**, sent: the serving nodes of the recipient, for a short message or a status report.
- **Report-SM-Delivery-Status**, sent: the outcome of a delivery, which sets or clears the HSS's message waiting data.
- **Alert-Service-Centre**, received: the recipient, or the Alert MSISDN of its subscription, can be reached again.

### Diameter routing (RFC 6733, GSMA IR.88)

- **Configured peers.** Static peering over SCTP, with multi-homed peers, as IR.88 recommends. Ella SMSC opens and keeps the connection.
- **Realm routing.** An MT-Forward-Short-Message to an MME, SMSF or IP-SM-GW that is not connected goes to the peers of the route to its realm, then of the default route, with its Destination-Host unchanged (RFC 6733 clause 6.1). The default route never takes the SMSC's own realm.
- **Failover.** By priority, with load sharing between peers of equal priority, and retransmission with the T flag (RFC 6733 clause 5.5.4).
- **Agents.** Requests that an agent relays are answered on the connection they came on.

### Short messages (TS 23.040)

- **TPDUs.** SMS-SUBMIT received; SMS-DELIVER and SMS-STATUS-REPORT sent; SMS-SUBMIT-REPORT with the TP-Failure-Cause of a refused SMS-SUBMIT.
- **Store and forward.** Messages to a phone are delivered in order, one at a time, and announce the next with TP-More-Messages-to-Send.
- **Validity period.** Relative, absolute and enhanced formats, including single-shot messages.
- **Status reports.** On the delivery, failure, expiry or replacement of a message whose sender set TP-Status-Report-Request.
- **Reject duplicates.** TP-Reject-Duplicates.
- **Replace short message.** Replace types 1 to 7 of TP-Protocol-Identifier, from phones and from the API.
- **Binary messages from the API.** TP-Protocol-Identifier, TP-Data-Coding-Scheme, TP-User-Data-Header and TP-User-Data delivered unchanged, including (U)SIM data download.
- **SMS-DELIVER-REPORT.** Kept from each delivery: in the SM-RP-UI of a successful MT-Forward-Short-Message answer, and in the SM-Diagnostic-Info of an SM Delivery Failure.
- **(U)SIM data download error.** A message whose SMS-DELIVER-REPORT has the TP-Failure-Cause `usim_data_download_error` fails without another attempt.
- **SC time stamps.** Distinct for the messages to a phone.
- **Numbering.** International, national and unknown types of number, converted to international with the operator's numbering plan.

## Not supported

- **SMS-COMMAND.** Refused with the TP-Failure-Cause `command_unsupported`.
- **Telematic interworking.** Refused with the TP-Failure-Cause `telematic_interworking_not_supported`.
- **Device triggering from phones.** Refused with the TP-Failure-Cause `unspecified_tp_pid_error`.
- **SMS-MI-Correlation-ID.** A short message that carries one is refused with DIAMETER_ERROR_FACILITY_NOT_SUPPORTED.
- **Diameter over TCP.**
- **Delivery to an MSC.** A visited network must deliver SMS over SGd (SMS in MME). Ella SMSC does not deliver to an MSC-Number, directly or through a Diameter-to-MAP interworking function.
- **Destination-Host in Report-SM-Delivery-Status.** It is sent to the HSS's realm (TS 29.338 clause 5.3.1.1.2 allows either).
- **MAP over SS7.**
