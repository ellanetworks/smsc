package delivery

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/db"
	smscs6c "github.com/ellanetworks/smsc/internal/s6c"
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

var deliveryFailureOutcomes = map[uint32]string{
	sgd.CauseMemoryCapacityExceeded: "memory_capacity_exceeded",
	sgd.CauseEquipmentProtocolError: "equipment_protocol_error",
	sgd.CauseEquipmentNotSMEquipped: "equipment_not_sm_equipped",
	sgd.CauseUnknownServiceCentre:   "unknown_service_centre",
	sgd.CauseSCCongestion:           "sc_congestion",
	sgd.CauseInvalidSMEAddress:      "invalid_sme_address",
	sgd.CauseUserNotSCUser:          "user_not_sc_user",
}

func (d *Deliverer) record(ctx context.Context, log *slog.Logger, messageID int64, step db.AttemptStep, node string, err error) {
	a := attemptOf(err)
	a.MessageID, a.AttemptedAt, a.Step, a.Node = messageID, d.Now(), step, node

	if _, err := d.Store.CreateDeliveryAttempt(ctx, a); err != nil {
		log.Error("failed to record delivery attempt", slog.Any("error", err))
	}

	log.Info("short message delivery attempt", slog.String("step", string(step)), slog.String("node", node),
		slog.String("outcome", a.Outcome))
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

			var re *sgd.ResultError
			if r.Code == tgpp.ResultErrorSMDeliveryFailure && errors.As(err, &re) && re.DeliveryFailureCause != nil {
				if outcome, ok := deliveryFailureOutcomes[*re.DeliveryFailureCause]; ok {
					a.Outcome = outcome
				}
			}
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
