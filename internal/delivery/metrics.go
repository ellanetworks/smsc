package delivery

import (
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	duration prometheus.Histogram
}

func NewMetrics() *Metrics {
	return &Metrics{
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "ellasmsc_message_delivery_duration_seconds",
			Help: "Time from submission to delivery of the short messages delivered, the time the phone was " +
				"unreachable included. Status reports are not short messages, and are left out.",
			Buckets:                         []float64{.1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300, 900, 3600, 21600, 86400, 604800},
			NativeHistogramBucketFactor:     1.1,
			NativeHistogramMaxBucketNumber:  100,
			NativeHistogramMinResetDuration: time.Hour,
		}),
	}
}

func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.duration}
}

func (m *Metrics) delivered(msg db.Message, at time.Time) {
	if m == nil || msg.Kind != db.KindMessage {
		return
	}

	m.duration.Observe(at.Sub(msg.SubmittedAt).Seconds())
}
