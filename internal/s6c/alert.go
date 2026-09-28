package s6c

import (
	"context"
	"log/slog"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/tgpp"
)

type AlertHandler struct {
	Identity             diameter.Identity
	ServiceCentreAddress string
	Alert                func(ctx context.Context, msisdn string) error
	Logger               *slog.Logger
}

func (h *AlertHandler) ServeDiameter(ctx context.Context, _ *diameter.Conn, req *diameter.Message) *diameter.Message {
	alert, err := s6c.ParseAlertServiceCentreRequest(req)
	if err != nil {
		return tgpp.NewErrorAnswer(req, h.Identity, err)
	}

	if alert.ServiceCentreAddress != h.ServiceCentreAddress {
		h.Logger.Info("ignoring alert for another service centre", slog.String("sc_address", alert.ServiceCentreAddress))
		return h.answer(req, diameter.ResultSuccess)
	}

	msisdn := alert.User.MSISDN
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
	return tgpp.NewAnswer(req, h.Identity, resultCode)
}
