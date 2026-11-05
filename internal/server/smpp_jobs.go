package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/esme"
)

const (
	notificationExpiry = 10 * time.Second
	lostSessionsCheck  = 10 * time.Second
)

func expireNotifications(ctx context.Context, esmes *esme.Service) {
	ticker := time.NewTicker(notificationExpiry)
	defer ticker.Stop()

	for {
		esmes.Expire(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func dropLostSMPPSessions(ctx context.Context, node *cluster.Node, store *db.DB, logger *slog.Logger) {
	ticker := time.NewTicker(lostSessionsCheck)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		nodes, err := store.SMPPSessionNodes(ctx)
		if err != nil {
			continue
		}

		for _, id := range nodes {
			if id == node.ID() || !node.Unreachable(id) {
				continue
			}

			lost, err := store.NodeSMPPSessions(ctx, id)
			if err != nil {
				continue
			}

			if err := store.SyncSMPPSessions(ctx, id, nil); err != nil {
				if ctx.Err() == nil {
					logger.WarnContext(ctx, "failed to drop the SMPP sessions of an unreachable node", slog.String("node", id),
						slog.Any("error", err))
				}

				continue
			}

			logger.InfoContext(ctx, "dropped the SMPP sessions of an unreachable node", slog.String("node", id),
				slog.Int("sessions", len(lost)))
		}
	}
}
