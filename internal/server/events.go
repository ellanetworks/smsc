package server

import (
	"context"
	"log/slog"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/delivery"
	"github.com/ellanetworks/smsc/internal/esme"
	"github.com/ellanetworks/smsc/internal/settings"
)

func (s *Server) applyEvents(ctx context.Context, ev db.Events, database *db.DB, live *settings.Live,
	deliverer *delivery.Deliverer, esmes *esme.Service, reloads *reloader,
) {
	if (ev.Members || ev.Replaced) && s.drain != nil {
		s.drain.changed()
	}

	if ev.Members || ev.Replaced {
		select {
		case s.labels <- struct{}{}:
		default:
		}
	}

	reloads.request(ev.Replaced || ev.Encryption, ev.OIDC || ev.Replaced || ev.Encryption, ev.Replaced)

	if ev.Settings {
		current, err := database.GetSettings(ctx)
		if err != nil {
			s.Logger.ErrorContext(ctx, "failed to reload the settings", slog.Any("error", err))
		} else {
			live.Reload(current)
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
