package server

import (
	"context"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/delivery"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/ellanetworks/smsc/version"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
)

// pendingTimeout bounds counting the pending messages for a scrape.
const pendingTimeout = 2 * time.Second

// metrics are those the API serves. They are created once per server and outlive the Diameter node, which the
// server replaces when the operator settings change its identity, so that a settings change keeps the counts.
type metrics struct {
	registry *prometheus.Registry
	received *intake.Received
	delivery *delivery.Metrics
	peers    *peerMetrics
}

// newMetrics registers the metrics of the Go runtime, the process, the build, the database, the messages and the
// requests to the peers. watchHSS adds the count of the HSSs.
func newMetrics(database *db.DB) *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		received: intake.New(),
		delivery: delivery.NewMetrics(),
		peers:    newPeerMetrics(),
	}

	// The build's labels are those of Prometheus's own build_info, with the SMSC's version and, when the build sets
	// it, its revision.
	v := version.Get()

	build := prometheus.Labels{"version": v.Version}
	if v.Revision != "" {
		build["revision"] = v.Revision
	}

	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		versioncollector.NewCollector("ellasmsc", versioncollector.WithExtraConstLabels(build)),
		pendingCollector{database},
	)
	m.registry.MustRegister(database.Collectors()...)
	m.registry.MustRegister(m.received.Collectors()...)
	m.registry.MustRegister(m.delivery.Collectors()...)
	m.registry.MustRegister(m.peers.collectors()...)

	return m
}

// watchHSS registers the count of the HSSs the SMSC can route through, read from the node in place when scraped.
func (m *metrics) watchHSS(hss *hssRequester) {
	m.registry.MustRegister(hssPeersCollector{count: func() int {
		_, hosts := hss.candidates()
		return len(hosts)
	}})
}

var pendingDesc = prometheus.NewDesc(
	"ellasmsc_messages_pending",
	"Short messages waiting for delivery, by state: due (to be delivered now, or being delivered) or waiting (for a "+
		"retry or for the phone to be reachable). Due messages that stay due mean the deliverer is falling behind.",
	[]string{"state"}, nil,
)

// pendingCollector counts the pending messages in the database when scraped, so that the count is that of the
// messages the API lists.
type pendingCollector struct {
	database *db.DB
}

func (c pendingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- pendingDesc
}

func (c pendingCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), pendingTimeout)
	defer cancel()

	due, waiting, err := c.database.CountPending(ctx, time.Now())
	if err != nil {
		ch <- prometheus.NewInvalidMetric(pendingDesc, err)
		return
	}

	ch <- prometheus.MustNewConstMetric(pendingDesc, prometheus.GaugeValue, float64(due), "due")

	ch <- prometheus.MustNewConstMetric(pendingDesc, prometheus.GaugeValue, float64(waiting), "waiting")
}
