package server

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	CheckShutdown = "shutdown"

	readinessTick    = time.Second
	leaderlessUnbind = 30 * time.Second
)

type readinessMetrics struct {
	checks *prometheus.GaugeVec
}

func newReadinessMetrics() *readinessMetrics {
	return &readinessMetrics{
		checks: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ellasmsc_ready_check",
			Help: "1 when the readiness check passes, 0 when it fails.",
		}, []string{"check"}),
	}
}

func (m *readinessMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{m.checks}
}

type readiness struct {
	node      *cluster.Node
	clustered bool
	stopping  func() bool
	drain     *drainer
	metrics   *readinessMetrics
	logger    *slog.Logger
	now       func() time.Time

	mu         sync.Mutex
	failing    string
	observed   bool
	leaderless time.Time
}

func (r *readiness) checks(ctx context.Context, withCluster bool) []cluster.Check {
	shutdown := cluster.Check{Name: CheckShutdown, OK: true}
	if r.stopping() {
		shutdown = cluster.Check{Name: CheckShutdown, Detail: "the node is shutting down"}
	}

	return append([]cluster.Check{shutdown}, r.node.Readiness(ctx, withCluster)...)
}

func (r *readiness) run(ctx context.Context) {
	ticker := time.NewTicker(readinessTick)
	defer ticker.Stop()

	for {
		r.observe(ctx, r.checks(ctx, false))

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *readiness) observe(ctx context.Context, checks []cluster.Check) {
	var failing, details []string

	leader := true

	for _, c := range checks {
		v := 0.0
		if c.OK {
			v = 1
		} else {
			failing = append(failing, c.Name)
			details = append(details, c.Name+": "+c.Detail)
		}

		r.metrics.checks.WithLabelValues(c.Name).Set(v)

		if c.Name == cluster.CheckLeader {
			leader = c.OK
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.logTransition(ctx, failing, details)
	r.trackLeader(leader)
}

func (r *readiness) logTransition(ctx context.Context, failing, details []string) {
	summary := strings.Join(failing, ",")

	if r.observed && summary == r.failing {
		return
	}

	switch {
	case len(failing) > 0:
		r.logger.WarnContext(ctx, "not ready", slog.Any("failing", details))
	case r.observed:
		r.logger.InfoContext(ctx, "ready")
	}

	r.failing, r.observed = summary, true
}

func (r *readiness) setDrain(d *drainer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.drain = d
}

func (r *readiness) trackLeader(leader bool) {
	if r.drain == nil {
		return
	}

	if leader || !r.clustered || r.stopping() {
		if !r.leaderless.IsZero() {
			r.leaderless = time.Time{}
			r.drain.setLeaderless(false)
		}

		return
	}

	now := r.now()

	if r.leaderless.IsZero() {
		r.leaderless = now
	}

	if now.Sub(r.leaderless) >= leaderlessUnbind {
		r.drain.setLeaderless(true)
	}
}
