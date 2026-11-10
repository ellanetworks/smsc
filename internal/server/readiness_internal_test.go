package server

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeBinds struct {
	draining bool
	closed   int
}

func (f *fakeBinds) SetDraining(draining bool) { f.draining = draining }

func (f *fakeBinds) CloseAll() { f.closed++ }

type countingHandler struct {
	mu      sync.Mutex
	records []string
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.records = append(h.records, r.Message)

	return nil
}

func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *countingHandler) WithGroup(string) slog.Handler { return h }

func (h *countingHandler) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	n := 0

	for _, r := range h.records {
		if r == msg {
			n++
		}
	}

	return n
}

func newTestReadiness(now *time.Time) (*readiness, *fakeBinds, *countingHandler) {
	logs := &countingHandler{}
	logger := slog.New(logs)
	b := &fakeBinds{}
	d := &drainer{esmes: b, logger: logger, wake: make(chan struct{}, 1)}

	return &readiness{
		clustered: true, stopping: func() bool { return false }, drain: d, metrics: newReadinessMetrics(), logger: logger,
		now: func() time.Time { return *now },
	}, b, logs
}

func TestLeaderlessNodeUnbindsSMPPAfter30s(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	r, b, _ := newTestReadiness(&now)

	leaderless := []cluster.Check{{Name: cluster.CheckLeader, Detail: "no contact with a cluster leader"}}
	led := []cluster.Check{{Name: cluster.CheckLeader, OK: true}}

	r.observe(ctx, leaderless)
	r.drain.refuseBinds(ctx)

	now = now.Add(leaderlessUnbind - time.Second)

	r.observe(ctx, leaderless)
	r.drain.refuseBinds(ctx)

	if b.draining || b.closed != 0 {
		t.Fatalf("binds after %s without a leader: draining %t, closed %d", leaderlessUnbind-time.Second, b.draining, b.closed)
	}

	now = now.Add(time.Second)

	r.observe(ctx, leaderless)
	r.drain.refuseBinds(ctx)

	if !b.draining || b.closed != 1 {
		t.Fatalf("binds after %s without a leader: draining %t, closed %d", leaderlessUnbind, b.draining, b.closed)
	}

	r.observe(ctx, led)
	r.drain.refuseBinds(ctx)

	if b.draining {
		t.Fatal("binds still refused once a leader is back")
	}

	r.drain.memberDraining = true
	r.drain.refuseBinds(ctx)

	if !b.draining || b.closed != 2 {
		t.Fatalf("binds while draining: draining %t, closed %d", b.draining, b.closed)
	}
}

func TestSingleNodeNeverUnbindsForALeader(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	r, b, _ := newTestReadiness(&now)
	r.clustered = false

	leaderless := []cluster.Check{{Name: cluster.CheckLeader, Detail: "this node leads, but its term is not established yet"}}

	r.observe(ctx, leaderless)

	now = now.Add(time.Hour)

	r.observe(ctx, leaderless)
	r.drain.refuseBinds(ctx)

	if b.draining {
		t.Fatal("a single node refused binds")
	}
}

func TestReadinessLogsTransitionsOnly(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	r, _, logs := newTestReadiness(&now)

	ok := []cluster.Check{{Name: CheckShutdown, OK: true}, {Name: cluster.CheckLeader, OK: true}}
	behind := func(n int) []cluster.Check {
		return []cluster.Check{{Name: CheckShutdown, OK: true}, {Name: cluster.CheckDatabase, Detail: string(rune('a' + n))}}
	}

	r.observe(ctx, ok)
	r.observe(ctx, ok)

	for n := range 5 {
		r.observe(ctx, behind(n))
	}

	r.observe(ctx, ok)

	if got := logs.count("not ready"); got != 1 {
		t.Fatalf("logged not ready %d times, want 1", got)
	}

	if got := logs.count("ready"); got != 1 {
		t.Fatalf("logged ready %d times, want 1", got)
	}

	if v := testutil.ToFloat64(r.metrics.checks.WithLabelValues(CheckShutdown)); v != 1 {
		t.Fatalf("ellasmsc_ready_check{check=shutdown} = %v", v)
	}

	r.observe(ctx, behind(0))

	if v := testutil.ToFloat64(r.metrics.checks.WithLabelValues(cluster.CheckDatabase)); v != 0 {
		t.Fatalf("ellasmsc_ready_check{check=database} = %v", v)
	}
}
