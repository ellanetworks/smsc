package sgd

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ellanetworks/smsc/diameter"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/tbcd"
	"github.com/ellanetworks/smsc/internal/tgpp"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

type MessageStore interface {
	CreateMessage(ctx context.Context, m db.NewMessage) (int64, error)
}

type Handler struct {
	Identity             diameter.Identity
	ServiceCentreAddress string
	Store                MessageStore
	Now                  func() time.Time
	Logger               *slog.Logger
}

type avpID struct {
	code     uint32
	vendorID uint32
}

var ofrKnownAVPs = map[avpID]bool{
	{diameter.AVPSessionID, 0}: true,
	{avpDRMP, 0}:               true,
	{diameter.AVPVendorSpecificApplicationID, 0}: true,
	{diameter.AVPAuthSessionState, 0}:            true,
	{diameter.AVPOriginHost, 0}:                  true,
	{diameter.AVPOriginRealm, 0}:                 true,
	{diameter.AVPDestinationHost, 0}:             true,
	{diameter.AVPDestinationRealm, 0}:            true,
	{tgpp.AVPSCAddress, tgpp.VendorID}:           true,
	{AVPOFRFlags, tgpp.VendorID}:                 true,
	{tgpp.AVPSupportedFeatures, tgpp.VendorID}:   true,
	{tgpp.AVPUserIdentifier, tgpp.VendorID}:      true,
	{AVPEPSLocationInformation, tgpp.VendorID}:   true,
	{AVPNRCellGlobalIdentity, tgpp.VendorID}:     true,
	{AVPSMRPUI, tgpp.VendorID}:                   true,
	{AVPSMSMICorrelationID, tgpp.VendorID}:       true,
	{AVPSMDeliveryOutcome, tgpp.VendorID}:        true,
	{AVPMPSPriority, tgpp.VendorID}:              true,
	{diameter.AVPProxyInfo, 0}:                   true,
	{avpRouteRecord, 0}:                          true,
}

var ofrRequiredAVPs = []struct {
	id           avpID
	minimumBytes int
}{
	{avpID{diameter.AVPSessionID, 0}, 0},
	{avpID{diameter.AVPAuthSessionState, 0}, 4},
	{avpID{diameter.AVPOriginHost, 0}, 0},
	{avpID{diameter.AVPOriginRealm, 0}, 0},
	{avpID{diameter.AVPDestinationRealm, 0}, 0},
	{avpID{tgpp.AVPSCAddress, tgpp.VendorID}, 0},
	{avpID{tgpp.AVPUserIdentifier, tgpp.VendorID}, 0},
	{avpID{AVPSMRPUI, tgpp.VendorID}, 0},
}

func (h *Handler) ServeDiameter(ctx context.Context, _ *diameter.Conn, req *diameter.Message) *diameter.Message {
	if req.CommandCode != CommandMOForwardShortMessage {
		return h.answer(req, diameter.ResultCommandUnsupported)
	}

	return h.moForwardShortMessage(ctx, req)
}

func (h *Handler) moForwardShortMessage(ctx context.Context, req *diameter.Message) *diameter.Message {
	if ans := h.checkAVPs(req); ans != nil {
		return ans
	}

	if _, ok := req.Find(AVPSMSMICorrelationID, tgpp.VendorID); ok {
		return h.experimental(req, tgpp.ResultErrorFacilityNotSupported)
	}

	scAddress, _ := req.Find(tgpp.AVPSCAddress, tgpp.VendorID)

	scDigits, err := tbcd.Decode(scAddress.Data)
	if err != nil {
		return h.invalidAVP(req, scAddress)
	}

	if scDigits != h.ServiceCentreAddress {
		return h.deliveryFailure(req, CauseUnknownServiceCentre, nil)
	}

	userIdentifier, _ := req.Find(tgpp.AVPUserIdentifier, tgpp.VendorID)

	identifiers, err := userIdentifier.Grouped()
	if err != nil {
		return h.invalidAVP(req, userIdentifier)
	}

	msisdn, ok := diameter.Find(identifiers, tgpp.AVPMSISDN, tgpp.VendorID)
	if !ok {
		return h.deliveryFailure(req, CauseUserNotSCUser, nil)
	}

	originator, err := tbcd.Decode(msisdn.Data)
	if err != nil {
		return h.invalidAVP(req, userIdentifier)
	}

	smRPUI, _ := req.Find(AVPSMRPUI, tgpp.VendorID)
	if len(smRPUI.Data) > maxSMRPUILength {
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

	if tpdu.RequestsTelematicInterworking(submit.ProtocolIdentifier) {
		return h.submitRejected(req, tpdu.FailureTelematicInterworkingNotSupported, receivedAt)
	}

	expiresAt, _, err := submit.Expiry(receivedAt)
	if err != nil {
		return h.submitRejected(req, tpdu.FailureValidityPeriodNotSupported, receivedAt)
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
		h.Logger.Error("failed to store mobile-originated short message", slog.Any("error", err))

		return h.deliveryFailure(req, CauseSCCongestion, h.submitReport(tpdu.FailureSCSystemFailure, receivedAt))
	}

	h.Logger.Debug("accepted mobile-originated short message",
		slog.Int64("message_id", id), slog.String("originator", originator), slog.String("recipient", submit.Destination.Digits))

	return h.answer(req, diameter.ResultSuccess)
}

func (h *Handler) checkAVPs(req *diameter.Message) *diameter.Message {
	for _, a := range req.AVPs {
		if a.Flags&diameter.AVPFlagMandatory != 0 && !ofrKnownAVPs[avpID{a.Code, a.VendorID}] {
			ans := h.answer(req, diameter.ResultAVPUnsupported)
			ans.AVPs = append(ans.AVPs, diameter.FailedAVP(a))

			return ans
		}
	}

	for _, r := range ofrRequiredAVPs {
		if _, ok := req.Find(r.id.code, r.id.vendorID); !ok {
			ans := h.answer(req, diameter.ResultMissingAVP)
			ans.AVPs = append(ans.AVPs, diameter.FailedAVP(
				diameter.OctetString(r.id.code, diameter.AVPFlagMandatory, r.id.vendorID, make([]byte, r.minimumBytes))))

			return ans
		}
	}

	return nil
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
	return h.deliveryFailure(req, CauseInvalidSMEAddress, h.submitReport(failureCause, receivedAt))
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
	return withAuthSessionState(diameter.NewAnswer(req, h.Identity, resultCode))
}

func (h *Handler) experimental(req *diameter.Message, resultCode uint32) *diameter.Message {
	return withAuthSessionState(diameter.NewExperimentalAnswer(req, h.Identity, tgpp.VendorID, resultCode))
}

func (h *Handler) invalidAVP(req *diameter.Message, offending diameter.AVP) *diameter.Message {
	ans := h.answer(req, diameter.ResultInvalidAVPValue)
	ans.AVPs = append(ans.AVPs, diameter.FailedAVP(offending))

	return ans
}

func (h *Handler) deliveryFailure(req *diameter.Message, cause uint32, diagnostic []byte) *diameter.Message {
	ans := h.experimental(req, tgpp.ResultErrorSMDeliveryFailure)

	inner := []diameter.AVP{
		diameter.Unsigned32(AVPSMEnumeratedDeliveryFailureCause, diameter.AVPFlagMandatory, tgpp.VendorID, cause),
	}

	if diagnostic != nil {
		inner = append(inner, diameter.OctetString(AVPSMDiagnosticInfo, diameter.AVPFlagMandatory, tgpp.VendorID, diagnostic))
	}

	ans.AVPs = append(ans.AVPs, diameter.Grouped(AVPSMDeliveryFailureCause, diameter.AVPFlagMandatory, tgpp.VendorID, inner...))

	return ans
}

func withAuthSessionState(ans *diameter.Message) *diameter.Message {
	ans.AVPs = append(ans.AVPs, diameter.Unsigned32(diameter.AVPAuthSessionState, diameter.AVPFlagMandatory, 0,
		diameter.AuthSessionStateNoStateMaintained))

	return ans
}
