package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/ellanetworks/smsc/internal/settings"
)

const (
	purgeInterval = time.Hour
	purgeBatch    = 1000
	purgePause    = 10 * time.Millisecond
)

type purgeStore interface {
	PurgeMessages(ctx context.Context, cutoff time.Time, limit int) (int, error)
}

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

func (p *purger) pass(ctx context.Context) {
	start := time.Now()
	cutoff := p.now().Add(-p.settings().Retention.Messages)
	purged := 0

	for {
		n, err := p.store.PurgeMessages(ctx, cutoff, purgeBatch)
		purged += n

		if err != nil {
			if ctx.Err() == nil {
				p.logger.ErrorContext(ctx, "failed to purge messages", slog.Int("count", purged), slog.Time("cutoff", cutoff),
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
		p.logger.InfoContext(ctx, "purged messages", slog.Int("count", purged), slog.Time("cutoff", cutoff),
			slog.Duration("duration", time.Since(start)))
	}
}
