package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/db"
	smscs6c "github.com/ellanetworks/smsc/internal/s6c"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

const (
	outcomeSuccess       = "success"
	outcomeTimeout       = "timeout"
	outcomeNoHSS         = "no_hss"
	outcomeUnreachable   = "unreachable"
	outcomeDiameterError = "diameter_error"
	outcomeError         = "error"
)

var experimentalOutcomes = map[uint32]string{
	tgpp.ResultErrorUserUnknown:          "user_unknown",
	tgpp.ResultErrorAbsentUser:           "absent_user",
	tgpp.ResultErrorUserBusyForMTSMS:     "user_busy_for_mt_sms",
	tgpp.ResultErrorFacilityNotSupported: "facility_not_supported",
	tgpp.ResultErrorIllegalUser:          "illegal_user",
	tgpp.ResultErrorIllegalEquipment:     "illegal_equipment",
	tgpp.ResultErrorSMDeliveryFailure:    "sm_delivery_failure",
	tgpp.ResultErrorServiceNotSubscribed: "service_not_subscribed",
	tgpp.ResultErrorServiceBarred:        "service_barred",
	tgpp.ResultErrorMWDListFull:          "mwd_list_full",
}

var deliveryFailureCauses = map[uint32]string{
	sgd.CauseMemoryCapacityExceeded: "memory_capacity_exceeded",
	sgd.CauseEquipmentProtocolError: "equipment_protocol_error",
	sgd.CauseEquipmentNotSMEquipped: "equipment_not_sm_equipped",
	sgd.CauseUnknownServiceCentre:   "unknown_service_centre",
	sgd.CauseSCCongestion:           "sc_congestion",
	sgd.CauseInvalidSMEAddress:      "invalid_sme_address",
	sgd.CauseUserNotSCUser:          "user_not_sc_user",
}

var absentDiagnostics = map[uint32]string{
	tgpp.AbsentUserNoPagingResponseMSC:        "no_paging_response_msc",
	tgpp.AbsentUserIMSIDetached:               "imsi_detached",
	tgpp.AbsentUserRoamingRestriction:         "roaming_restriction",
	tgpp.AbsentUserDeregisteredNonGPRS:        "deregistered_non_gprs",
	tgpp.AbsentUserPurgedNonGPRS:              "ms_purged_non_gprs",
	tgpp.AbsentUserNoPagingResponseSGSN:       "no_paging_response_sgsn",
	tgpp.AbsentUserGPRSDetached:               "gprs_detached",
	tgpp.AbsentUserDeregisteredGPRS:           "deregistered_gprs",
	tgpp.AbsentUserPurgedGPRS:                 "ms_purged_gprs",
	tgpp.AbsentUserUnidentifiedSubscriberMSC:  "unidentified_subscriber_msc",
	tgpp.AbsentUserUnidentifiedSubscriberSGSN: "unidentified_subscriber_sgsn",
	tgpp.AbsentUserDeregisteredIMS:            "deregistered_ims",
	tgpp.AbsentUserNoResponseIPSMGW:           "no_response_ip_sm_gw",
	tgpp.AbsentUserTemporarilyUnavailable:     "temporarily_unavailable",
}

var tpFailureCauses = map[byte]string{
	0x80: "telematic_interworking_not_supported",
	0x81: "short_message_type_0_not_supported",
	0x82: "cannot_replace_short_message",
	0x8f: "unspecified_tp_pid_error",
	0x90: "data_coding_scheme_not_supported",
	0x91: "message_class_not_supported",
	0x9f: "unspecified_tp_dcs_error",
	0xa0: "command_cannot_be_actioned",
	0xa1: "command_unsupported",
	0xaf: "unspecified_tp_command_error",
	0xb0: "tpdu_not_supported",
	0xc0: "sc_busy",
	0xc1: "no_sc_subscription",
	0xc2: "sc_system_failure",
	0xc3: "invalid_sme_address",
	0xc4: "destination_sme_barred",
	0xc5: "sm_rejected_duplicate_sm",
	0xc6: "tp_vpf_not_supported",
	0xc7: "tp_vp_not_supported",
	0xd0: "usim_sms_storage_full",
	0xd1: "no_sms_storage_capability_in_usim",
	0xd2: "error_in_ms",
	0xd3: "memory_capacity_exceeded",
	0xd4: "usim_application_toolkit_busy",
	0xd5: "usim_data_download_error",
	0xff: "unspecified_error_cause",
}

func (d *Deliverer) record(ctx context.Context, log *slog.Logger, messageID int64, a db.DeliveryAttempt) {
	a.MessageID, a.CompletedAt = messageID, d.Now()

	if _, err := d.Store.CreateDeliveryAttempt(ctx, a); err != nil {
		log.Error("failed to record delivery attempt", slog.Any("error", err))
	}

	log.Info("short message delivery attempt", slog.String("step", string(a.Step)), slog.String("node", a.Node),
		slog.String("outcome", a.Outcome))
}

func (k nodeKind) nodeType() db.NodeType {
	switch k {
	case kindSGSN:
		return db.NodeTypeSGSN
	case kindSMSF3GPP:
		return db.NodeTypeSMSF3GPP
	case kindSMSFNon3GPP:
		return db.NodeTypeSMSFNon3GPP
	default:
		return db.NodeTypeMME
	}
}

func routingAttemptOf(routing s6c.Routing, err error) db.DeliveryAttempt {
	a := attemptOf(err)

	var re *s6c.ResultError

	switch {
	case err == nil:
		a.AbsentDiagnostics = absentDiagnosticsOf(routing.Absent)
	case errors.As(err, &re):
		a.AbsentDiagnostics = absentDiagnosticsOf(re.Absent)
	}

	return a
}

func attemptOf(err error) db.DeliveryAttempt {
	if err == nil {
		return db.DeliveryAttempt{Outcome: outcomeSuccess, ResultCode: ptr(diameter.ResultSuccess)}
	}

	if r, ok := tgpp.ResultOf(err); ok {
		a := db.DeliveryAttempt{Outcome: outcomeDiameterError, ResultCode: ptr(r.Code)}

		if r.Experimental {
			a.VendorID = ptr(r.VendorID)
		}

		if r.Experimental && r.VendorID == tgpp.VendorID {
			if outcome, ok := experimentalOutcomes[r.Code]; ok {
				a.Outcome = outcome
			}

			addDetails(&a, r.Code, err)
		}

		return a
	}

	switch {
	case errors.Is(err, smscs6c.ErrNoHSS):
		return db.DeliveryAttempt{Outcome: outcomeNoHSS}
	case errors.Is(err, diameter.ErrNotConnected), errors.Is(err, diameter.ErrUnknownPeer):
		return db.DeliveryAttempt{Outcome: outcomeUnreachable}
	case errors.Is(err, context.DeadlineExceeded):
		return db.DeliveryAttempt{Outcome: outcomeTimeout}
	default:
		return db.DeliveryAttempt{Outcome: outcomeError}
	}
}

func addDetails(a *db.DeliveryAttempt, code uint32, err error) {
	var sgdErr *sgd.ResultError

	switch {
	case errors.As(err, &sgdErr) && code == tgpp.ResultErrorSMDeliveryFailure:
		a.FailureCause = optionalName(deliveryFailureCauses, sgdErr.DeliveryFailureCause)

		if fcs, ok := tpdu.DeliverReportFailureCause(sgdErr.DiagnosticInfo); ok {
			a.TPFailureCause = tpFailureCauseName(fcs)
		}
	case errors.As(err, &sgdErr) && code == tgpp.ResultErrorAbsentUser:
		a.AbsentDiagnostic = optionalName(absentDiagnostics, sgdErr.AbsentUserDiagnostic)
	}
}

func absentDiagnosticsOf(d s6c.AbsentUserDiagnostics) db.AbsentDiagnostics {
	return db.AbsentDiagnostics{
		MME:         optionalName(absentDiagnostics, d.MME),
		MSC:         optionalName(absentDiagnostics, d.MSC),
		SGSN:        optionalName(absentDiagnostics, d.SGSN),
		SMSF3GPP:    optionalName(absentDiagnostics, d.SMSF3GPP),
		SMSFNon3GPP: optionalName(absentDiagnostics, d.SMSFNon3GPP),
	}
}

func optionalName(names map[uint32]string, v *uint32) string {
	if v == nil {
		return ""
	}

	if name, ok := names[*v]; ok {
		return name
	}

	return fmt.Sprintf("unknown_%d", *v)
}

func tpFailureCauseName(fcs byte) string {
	if name, ok := tpFailureCauses[fcs]; ok {
		return name
	}

	if fcs >= 0xe0 && fcs <= 0xfe {
		return fmt.Sprintf("application_specific_%d", fcs)
	}

	return fmt.Sprintf("unknown_%d", fcs)
}
