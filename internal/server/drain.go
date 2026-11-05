package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/esme"
)

const drainTick = time.Second

type drainer struct {
	node   *cluster.Node
	store  *db.DB
	esmes  *esme.Service
	logger *slog.Logger
	wake   chan struct{}

	draining bool
}

func (d *drainer) changed() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *drainer) run(ctx context.Context) {
	ticker := time.NewTicker(drainTick)
	defer ticker.Stop()

	for {
		d.reconcile(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.wake:
		}
	}
}

func (d *drainer) reconcile(ctx context.Context) {
	m, err := d.store.Member(ctx, d.node.ID())
	if err != nil {
		return
	}

	if m.DrainState == db.DrainActive {
		if d.draining {
			d.logger.InfoContext(ctx, "drain cancelled: accepting SMPP binds again")
			d.esmes.SetDraining(false)
			d.draining = false
		}

		return
	}

	if !d.draining {
		d.logger.InfoContext(ctx, "draining: closing the SMPP sessions and refusing new binds")
		d.esmes.SetDraining(true)
		d.esmes.CloseAll()
		d.draining = true
	}

	if err := d.node.HandOver(); err != nil {
		d.logger.WarnContext(ctx, "draining: failed to hand the leadership over", slog.Any("error", err))
		return
	}

	if d.node.RaftLeader() || m.DrainState == db.DrainDrained {
		return
	}

	if err := d.store.SetDrainState(ctx, d.node.ID(), db.DrainDrained); err != nil {
		d.logger.WarnContext(ctx, "draining: failed to record the drained state", slog.Any("error", err))
		return
	}

	d.logger.InfoContext(ctx, "drained")
}
