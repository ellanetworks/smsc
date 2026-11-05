package server

import (
	"context"
	"fmt"
	"time"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/delivery"
	"github.com/ellanetworks/smsc/internal/esme"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/ellanetworks/smsc/version"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
)

const pendingTimeout = 2 * time.Second

type metrics struct {
	registry *prometheus.Registry
	received *intake.Received
	delivery *delivery.Metrics
	peers    *peerMetrics
	smpp     *esme.Metrics
	requests *api.RequestMetrics
}

func newMetrics(database *db.DB, node *cluster.Node) (*metrics, error) {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		received: intake.New(),
		delivery: delivery.NewMetrics(),
		peers:    newPeerMetrics(),
		requests: api.NewRequestMetrics(),
	}
	leading := func() bool { leading, _ := node.Leader(); return leading }
	m.smpp = esme.NewMetrics(database, leading)

	v := version.Get()

	build := prometheus.Labels{"version": v.Version}
	if v.Revision != "" {
		build["revision"] = v.Revision
	}

	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		versioncollector.NewCollector("ellasmsc", versioncollector.WithExtraConstLabels(build)),
		pendingCollector{database: database, leading: leading},
	)

	if err := cluster.RegisterRaftMetrics(m.registry); err != nil {
		return nil, fmt.Errorf("register raft metrics: %w", err)
	}

	m.registry.MustRegister(node.Collectors()...)
	m.registry.MustRegister(database.Collectors()...)
	m.registry.MustRegister(m.received.Collectors()...)
	m.registry.MustRegister(m.delivery.Collectors()...)
	m.registry.MustRegister(m.peers.collectors()...)
	m.registry.MustRegister(m.smpp.Collectors()...)
	m.registry.MustRegister(m.requests.Collectors()...)

	return m, nil
}

func (m *metrics) watchHSS(hss hssSender) {
	m.registry.MustRegister(hssPeersCollector{count: func() int {
		return len(hss.hosts())
	}})
}

var pendingDesc = prometheus.NewDesc(
	"ellasmsc_messages_pending",
	"Short messages and status reports waiting for delivery, by type (message, status_report) and state: due (to be "+
		"delivered now, or being delivered) or waiting (for a retry or for the phone to be reachable). Due messages "+
		"that stay due mean the deliverer is falling behind.",
	[]string{"type", "state"}, nil,
)

type pendingCollector struct {
	database *db.DB
	leading  func() bool
}

func (c pendingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- pendingDesc
}

func (c pendingCollector) Collect(ch chan<- prometheus.Metric) {
	if !c.leading() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), pendingTimeout)
	defer cancel()

	counts, err := c.database.CountPending(ctx, time.Now())
	if err != nil {
		ch <- prometheus.NewInvalidMetric(pendingDesc, err)
		return
	}

	for kind, count := range counts {
		ch <- prometheus.MustNewConstMetric(pendingDesc, prometheus.GaugeValue, float64(count.Due), string(kind), "due")

		ch <- prometheus.MustNewConstMetric(pendingDesc, prometheus.GaugeValue, float64(count.Waiting), string(kind), "waiting")
	}
}
