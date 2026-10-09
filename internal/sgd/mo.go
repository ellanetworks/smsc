package sgd

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

type MessageStore interface {
	CreateMessage(ctx context.Context, m db.NewMessage) (int64, error)
}

type Handler struct {
	Identity func() diameter.Identity
	Settings func() settings.Settings
	Store    MessageStore
	Stored   func()
	// Received counts the messages that phones send.
	Received *intake.Received
	Now      func() time.Time
	Logger   *slog.Logger
}

func (h *Handler) ServeDiameter(ctx context.Context, _ *diameter.Conn, req *diameter.Message) *diameter.Message {
	if req.CommandCode != sgd.CommandMOForwardShortMessage {
		return h.answer(req, diameter.ResultCommandUnsupported)
	}

	return h.moForwardShortMessage(ctx, req)
}

func (h *Handler) moForwardShortMessage(ctx context.Context, req *diameter.Message) *diameter.Message {
	// Every answer but those that store the message, or fail to, refuses it.
	result := intake.Rejected

	defer func() { h.Received.Add(intake.OriginMobile, result, 1) }()

	if err := sgd.CheckMOForwardShortMessage(req); err != nil {
		return tgpp.NewErrorAnswer(req, h.Identity(), err)
	}

	if _, ok := req.Find(tgpp.AVPSMSMICorrelationID, tgpp.VendorID); ok {
		return h.experimental(req, tgpp.ResultErrorFacilityNotSupported)
	}

	scAddress, _ := req.Find(tgpp.AVPSCAddress, tgpp.VendorID)

	scDigits, err := tgpp.DecodeE164(scAddress.Data)
	if err != nil {
		return h.invalidAVP(req, scAddress)
	}

	current := h.Settings()

	if scDigits != current.Operator.ServiceCentreAddress {
		return h.deliveryFailure(req, sgd.CauseUnknownServiceCentre, nil)
	}

	userIdentifier, _ := req.Find(tgpp.AVPUserIdentifier, tgpp.VendorID)

	user, err := tgpp.ParseUserIdentifier(userIdentifier)
	if err != nil {
		return h.invalidAVP(req, userIdentifier)
	}

	originator := user.MSISDN
	if originator == "" {
		return h.deliveryFailure(req, sgd.CauseUserNotSCUser, nil)
	}

	smRPUI, _ := req.Find(sgd.AVPSMRPUI, tgpp.VendorID)
	if len(smRPUI.Data) > sgd.MaxSMRPUILength {
		return h.invalidAVP(req, smRPUI)
	}

	receivedAt := h.Now()

	submit, err := tpdu.DecodeSubmit(smRPUI.Data)
	if err != nil {
		return h.submitRejected(req, submitFailureCause(smRPUI.Data, err), receivedAt)
	}

	if submit.Destination.Digits == "" {
		return h.submitRejected(req, tpdu.FailureInvalidSMEAddress, receivedAt)
	}

	recipient, err := current.Operator.Numbering.International(submit.Destination.TypeOfNumber, submit.Destination.Digits)
	if err != nil {
		return h.submitRejected(req, tpdu.FailureInvalidSMEAddress, receivedAt)
	}

	if tpdu.RequestsTelematicInterworking(submit.ProtocolIdentifier) {
		return h.submitRejected(req, tpdu.FailureTelematicInterworkingNotSupported, receivedAt)
	}

	expiresAt, hasValidity, err := submit.Expiry(receivedAt)
	if err != nil {
		return h.submitRejected(req, tpdu.FailureValidityPeriodNotSupported, receivedAt)
	}

	if !hasValidity {
		expiresAt = receivedAt.Add(current.Delivery.DefaultValidity)
	}

	id, err := h.Store.CreateMessage(ctx, db.NewMessage{
		Originator: db.Address{
			Digits:        originator,
			TypeOfNumber:  tpdu.TypeOfNumberInternational,
			NumberingPlan: tpdu.NumberingPlanISDN,
		},
		Recipient: db.Address{
			Digits:        submit.Destination.Digits,
			TypeOfNumber:  submit.Destination.TypeOfNumber,
			NumberingPlan: submit.Destination.NumberingPlan,
		},
		MSISDN:             recipient,
		Origin:             db.OriginMobile,
		MessageReference:   submit.MessageReference,
		ProtocolIdentifier: submit.ProtocolIdentifier,
		RejectDuplicates:   submit.RejectDuplicates,
		Replace:            tpdu.IsReplaceType(submit.ProtocolIdentifier),
		TPDU:               smRPUI.Data,
		SingleShot:         submit.SingleShot(),
		SubmittedAt:        receivedAt,
		ExpiresAt:          expiresAt,
	})
	if errors.Is(err, db.ErrDuplicate) {
		return h.submitRejected(req, tpdu.FailureRejectedDuplicate, receivedAt)
	}

	if err != nil {
		result = intake.Error

		h.Logger.Error("failed to store mobile-originated short message", slog.Any("error", err))

		return h.deliveryFailure(req, sgd.CauseSCCongestion, h.submitReport(tpdu.FailureSCSystemFailure, receivedAt))
	}

	result = intake.Accepted

	if h.Stored != nil {
		h.Stored()
	}

	h.Logger.Debug("accepted mobile-originated short message",
		slog.Int64("message_id", id), slog.String("originator", originator), slog.String("recipient", recipient))

	return h.answer(req, diameter.ResultSuccess)
}

func submitFailureCause(b []byte, err error) byte {
	switch {
	case errors.Is(err, tpdu.ErrUnsupportedAddress):
		return tpdu.FailureInvalidSMEAddress
	case errors.Is(err, tpdu.ErrUnsupportedValidityPeriod):
		return tpdu.FailureValidityPeriodNotSupported
	case errors.Is(err, tpdu.ErrUnsupportedMessageType):
		if mti, _ := tpdu.MessageType(b); mti == tpdu.MessageTypeCommand {
			return tpdu.FailureCommandUnsupported
		}

		return tpdu.FailureTPDUNotSupported
	default:
		return tpdu.FailureUnspecified
	}
}

func (h *Handler) submitRejected(req *diameter.Message, failureCause byte, receivedAt time.Time) *diameter.Message {
	return h.deliveryFailure(req, sgd.CauseInvalidSMEAddress, h.submitReport(failureCause, receivedAt))
}

func (h *Handler) submitReport(failureCause byte, receivedAt time.Time) []byte {
	report, err := tpdu.EncodeSubmitReportError(failureCause, receivedAt)
	if err != nil {
		h.Logger.Error("failed to encode SMS-SUBMIT-REPORT", slog.Any("error", err))
		return nil
	}

	return report
}

func (h *Handler) answer(req *diameter.Message, resultCode uint32) *diameter.Message {
	return tgpp.NewAnswer(req, h.Identity(), resultCode)
}

func (h *Handler) experimental(req *diameter.Message, resultCode uint32) *diameter.Message {
	return tgpp.NewExperimentalAnswer(req, h.Identity(), resultCode)
}

func (h *Handler) invalidAVP(req *diameter.Message, offending diameter.AVP) *diameter.Message {
	ans := h.answer(req, diameter.ResultInvalidAVPValue)
	ans.AVPs = append(ans.AVPs, diameter.FailedAVP(offending))

	return ans
}

func (h *Handler) deliveryFailure(req *diameter.Message, cause sgd.DeliveryFailureCause, diagnostic []byte) *diameter.Message {
	ans, err := sgd.NewDeliveryFailureAnswer(req, h.Identity(), cause, diagnostic)
	if err != nil {
		return h.answer(req, diameter.ResultUnableToComply)
	}

	return ans
}
