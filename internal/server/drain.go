package server

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/db"
)

const drainTick = time.Second

type binds interface {
	SetDraining(draining bool)
	CloseAll()
}

type drainer struct {
	node   *cluster.Node
	store  *db.DB
	esmes  binds
	logger *slog.Logger
	wake   chan struct{}

	leaderless     atomic.Bool
	memberDraining bool
	refusing       bool
}

func (d *drainer) setLeaderless(leaderless bool) {
	if d.leaderless.Swap(leaderless) != leaderless {
		d.changed()
	}
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
	if err == nil {
		d.memberDraining = m.DrainState != db.DrainActive
	}

	d.refuseBinds(ctx)

	if err != nil || !d.memberDraining {
		return
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

func (d *drainer) refuseBinds(ctx context.Context) {
	refuse := d.memberDraining || d.leaderless.Load()

	if refuse == d.refusing {
		return
	}

	d.refusing = refuse

	if !refuse {
		d.logger.InfoContext(ctx, "accepting SMPP binds again")
		d.esmes.SetDraining(false)

		return
	}

	if d.memberDraining {
		d.logger.InfoContext(ctx, "draining: closing the SMPP sessions and refusing new binds")
	} else {
		d.logger.WarnContext(ctx, "no cluster leader for 30s: closing the SMPP sessions and refusing new binds, so that ESMEs bind to a node with a leader")
	}

	d.esmes.SetDraining(true)
	d.esmes.CloseAll()
}
