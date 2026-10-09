package delivery

import (
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/prometheus/client_golang/prometheus"
)

// finalStatuses are the statuses a message ends with, which label the messages completed.
var finalStatuses = []db.MessageStatus{db.StatusDelivered, db.StatusFailed, db.StatusExpired}

// Metrics are the deliverer's. A nil Metrics records nothing.
type Metrics struct {
	completed *prometheus.CounterVec
	duration  prometheus.Histogram
}

func NewMetrics() *Metrics {
	m := &Metrics{
		completed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellasmsc_messages_completed_total",
			Help: "Short messages that reached a final status, by status (delivered, failed, expired).",
		}, []string{"status"}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "ellasmsc_message_delivery_duration_seconds",
			Help: "Time from submission to delivery of the short messages delivered, the time the phone was " +
				"unreachable included.",
			// From a delivery on the first attempt to a phone that is on, which takes under a second, through the retry
			// intervals, up to the default validity of 7 days.
			Buckets:                         []float64{.1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300, 900, 3600, 21600, 86400, 604800},
			NativeHistogramBucketFactor:     1.1,
			NativeHistogramMaxBucketNumber:  100,
			NativeHistogramMinResetDuration: time.Hour,
		}),
	}

	for _, status := range finalStatuses {
		m.completed.WithLabelValues(string(status))
	}

	return m
}

func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.completed, m.duration}
}

// complete counts a message that reached a final status, and times it if delivered.
func (m *Metrics) complete(msg db.Message, status db.MessageStatus, at time.Time) {
	if m == nil {
		return
	}

	m.completed.WithLabelValues(string(status)).Inc()

	if status == db.StatusDelivered {
		m.duration.Observe(at.Sub(msg.SubmittedAt).Seconds())
	}
}
