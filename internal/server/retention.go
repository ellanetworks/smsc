package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/ellanetworks/smsc/internal/settings"
)

const (
	// purgeInterval is the time between two passes. Hourly keeps a pass small at the rates an SMSC takes.
	purgeInterval = time.Hour
	// purgeBatch is the most messages that one transaction deletes, and purgePause the wait between two: the database
	// has a single connection, which delivery and the API get in between.
	purgeBatch = 1000
	purgePause = 10 * time.Millisecond
)

type purgeStore interface {
	PurgeMessages(ctx context.Context, cutoff time.Time, limit int) (int, error)
}

// purger deletes the messages whose retention period passed: once at start, then every interval. A change to the
// retention applies from the next pass.
type purger struct {
	store    purgeStore
	settings func() settings.Settings
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger
}

func (p *purger) run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		p.pass(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pass deletes the messages that reached their final status a retention period ago, in batches, until none is left. A
// pass that fails stops, and the next one resumes: deleting is idempotent.
func (p *purger) pass(ctx context.Context) {
	start := time.Now()
	cutoff := p.now().Add(-p.settings().Retention.Messages)
	purged := 0

	for {
		n, err := p.store.PurgeMessages(ctx, cutoff, purgeBatch)
		purged += n

		if err != nil {
			if ctx.Err() == nil {
				p.logger.Error("failed to purge messages", slog.Int("count", purged), slog.Time("cutoff", cutoff),
					slog.Any("error", err))
			}

			return
		}

		if n < purgeBatch {
			break
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(purgePause):
		}
	}

	if purged > 0 {
		p.logger.Info("purged messages", slog.Int("count", purged), slog.Time("cutoff", cutoff),
			slog.Duration("duration", time.Since(start)))
	}
}
