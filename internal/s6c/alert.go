package s6c

import (
	"context"
	"log/slog"

	"github.com/ellanetworks/smsc/diameter"
	"github.com/ellanetworks/smsc/internal/tbcd"
	"github.com/ellanetworks/smsc/internal/tgpp"
)

type AlertHandler struct {
	Identity             diameter.Identity
	ServiceCentreAddress string
	Alert                func(ctx context.Context, msisdn string) error
	Logger               *slog.Logger
}

var alrKnownAVPs = map[diameter.AVPKey]bool{
	{Code: diameter.AVPSessionID}:                                 true,
	{Code: diameter.AVPDRMP}:                                      true,
	{Code: diameter.AVPVendorSpecificApplicationID}:               true,
	{Code: diameter.AVPAuthSessionState}:                          true,
	{Code: diameter.AVPOriginHost}:                                true,
	{Code: diameter.AVPOriginRealm}:                               true,
	{Code: diameter.AVPDestinationHost}:                           true,
	{Code: diameter.AVPDestinationRealm}:                          true,
	{Code: tgpp.AVPSCAddress, VendorID: tgpp.VendorID}:            true,
	{Code: tgpp.AVPUserIdentifier, VendorID: tgpp.VendorID}:       true,
	{Code: avpSMSMICorrelationID, VendorID: tgpp.VendorID}:        true,
	{Code: avpMaximumUEAvailabilityTime, VendorID: tgpp.VendorID}: true,
	{Code: avpSMSGMSCAlertEvent, VendorID: tgpp.VendorID}:         true,
	{Code: avpServingNode, VendorID: tgpp.VendorID}:               true,
	{Code: tgpp.AVPSupportedFeatures, VendorID: tgpp.VendorID}:    true,
	{Code: diameter.AVPProxyInfo}:                                 true,
	{Code: diameter.AVPRouteRecord}:                               true,
}

var alrRequiredAVPs = []diameter.RequiredAVP{
	{Key: diameter.AVPKey{Code: diameter.AVPSessionID}},
	{Key: diameter.AVPKey{Code: diameter.AVPAuthSessionState}, MinimumLength: 4},
	{Key: diameter.AVPKey{Code: diameter.AVPOriginHost}},
	{Key: diameter.AVPKey{Code: diameter.AVPOriginRealm}},
	{Key: diameter.AVPKey{Code: diameter.AVPDestinationRealm}},
	{Key: diameter.AVPKey{Code: tgpp.AVPSCAddress, VendorID: tgpp.VendorID}},
	{Key: diameter.AVPKey{Code: tgpp.AVPUserIdentifier, VendorID: tgpp.VendorID}},
}

func (h *AlertHandler) ServeDiameter(ctx context.Context, _ *diameter.Conn, req *diameter.Message) *diameter.Message {
	if avpErr := diameter.CheckAVPs(req, alrKnownAVPs, alrRequiredAVPs); avpErr != nil {
		ans := h.answer(req, avpErr.ResultCode)
		ans.AVPs = append(ans.AVPs, diameter.FailedAVP(avpErr.AVP))

		return ans
	}

	scAddress, _ := req.Find(tgpp.AVPSCAddress, tgpp.VendorID)

	scDigits, err := tbcd.Decode(scAddress.Data)
	if err != nil {
		return h.invalidAVP(req, scAddress)
	}

	userIdentifier, _ := req.Find(tgpp.AVPUserIdentifier, tgpp.VendorID)

	msisdn, err := userIdentifierMSISDN(userIdentifier)
	if err != nil {
		return h.invalidAVP(req, userIdentifier)
	}

	if scDigits != h.ServiceCentreAddress {
		h.Logger.Info("ignoring alert for another service centre", slog.String("sc_address", scDigits))
		return h.answer(req, diameter.ResultSuccess)
	}

	if msisdn == "" {
		h.Logger.Info("ignoring alert without an MSISDN")
		return h.answer(req, diameter.ResultSuccess)
	}

	if err := h.Alert(ctx, msisdn); err != nil {
		h.Logger.Error("failed to act on service centre alert", slog.String("msisdn", msisdn), slog.Any("error", err))
		return h.answer(req, diameter.ResultUnableToComply)
	}

	h.Logger.Debug("recipient alerted", slog.String("msisdn", msisdn))

	return h.answer(req, diameter.ResultSuccess)
}

func (h *AlertHandler) answer(req *diameter.Message, resultCode uint32) *diameter.Message {
	ans := diameter.NewAnswer(req, h.Identity, resultCode)
	ans.AVPs = append(ans.AVPs, diameter.Unsigned32(diameter.AVPAuthSessionState, diameter.AVPFlagMandatory, 0,
		diameter.AuthSessionStateNoStateMaintained))

	return ans
}

func (h *AlertHandler) invalidAVP(req *diameter.Message, offending diameter.AVP) *diameter.Message {
	ans := h.answer(req, diameter.ResultInvalidAVPValue)
	ans.AVPs = append(ans.AVPs, diameter.FailedAVP(offending))

	return ans
}
