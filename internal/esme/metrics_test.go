package esme

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/prometheus/client_golang/prometheus"
)

type pendingCounterFunc func(ctx context.Context, now time.Time) (map[db.NotificationType]int, error)

func (f pendingCounterFunc) CountPendingNotifications(ctx context.Context, now time.Time) (map[db.NotificationType]int, error) {
	return f(ctx, now)
}

func TestPendingCollector(t *testing.T) {
	failure := errors.New("database is locked")

	m := NewMetrics(pendingCounterFunc(func(ctx context.Context, _ time.Time) (map[db.NotificationType]int, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the count has no deadline")
		}

		return nil, failure
	}), nil)

	reg := prometheus.NewRegistry()
	reg.MustRegister(m.pending)

	if _, err := reg.Gather(); !errors.Is(err, failure) {
		t.Fatalf("Gather = %v, want %v", err, failure)
	}
}
