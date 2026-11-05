package server

import (
	"context"
	"log/slog"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/delivery"
	"github.com/ellanetworks/smsc/internal/esme"
	"github.com/ellanetworks/smsc/internal/oidc"
	"github.com/ellanetworks/smsc/internal/settings"
)

func (s *Server) applyEvents(ctx context.Context, ev db.Events, database *db.DB, live *settings.Live, oidcLive *oidc.Live,
	deliverer *delivery.Deliverer, esmes *esme.Service, secrets *secrets,
) {
	if (ev.Members || ev.Replaced) && s.drain != nil {
		s.drain.changed()
	}

	if ev.Replaced {
		if err := secrets.reload(ctx, database); err != nil {
			s.Logger.ErrorContext(ctx, "failed to reload the secrets", slog.Any("error", err))
		}
	}

	if ev.Settings {
		current, err := database.GetSettings(ctx)
		if err != nil {
			s.Logger.ErrorContext(ctx, "failed to reload the settings", slog.Any("error", err))
		} else {
			live.Reload(current)
		}
	}

	if ev.OIDC {
		current, err := database.GetOIDC(ctx)
		if err != nil {
			s.Logger.ErrorContext(ctx, "failed to reload the OIDC settings", slog.Any("error", err))
		} else {
			oidcLive.Reload(current)
		}
	}

	if len(ev.Alerted) > 0 {
		deliverer.Alerted(ev.Alerted)
	} else if ev.Messages {
		deliverer.Notify()
	}

	if ev.Notifications {
		esmes.NotificationsQueued()
	}

	for _, id := range ev.ClosedESMEs {
		esmes.CloseESME(id)
	}

	if ev.Replaced {
		esmes.CloseAll()
	}
}
