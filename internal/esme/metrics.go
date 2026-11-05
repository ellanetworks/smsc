package esme

import (
	"context"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/prometheus/client_golang/prometheus"
)

type PendingCounter interface {
	CountPendingNotifications(ctx context.Context, now time.Time) (map[db.NotificationType]int, error)
}

const pendingTimeout = 2 * time.Second

type Metrics struct {
	sessions *prometheus.GaugeVec
	binds    *prometheus.CounterVec
	sent     *prometheus.CounterVec
	submit   prometheus.Histogram
	pending  *pendingCollector
}

func NewMetrics(store PendingCounter, leading func() bool) *Metrics {
	m := &Metrics{
		sessions: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ellasmsc_smpp_sessions",
			Help: "Bound SMPP sessions, by bind type (transmitter, receiver, transceiver).",
		}, []string{"bind_type"}),
		binds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellasmsc_smpp_binds_total",
			Help: "SMPP bind attempts, by result (accepted, rejected).",
		}, []string{"result"}),
		sent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellasmsc_smpp_notifications_sent_total",
			Help: "SMPP notifications that reached a final outcome, by type (receipt, sim_answer) and result " +
				"(acknowledged, rejected, expired, no_user_data).",
		}, []string{"type", "result"}),
		submit: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "ellasmsc_smpp_submit_duration_seconds",
			Help:    "Time from a submit_sm read to its submit_sm_resp written.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
		}),
		pending: &pendingCollector{
			store:   store,
			leading: leading,
			desc: prometheus.NewDesc("ellasmsc_smpp_notifications_pending",
				"SMPP notifications not yet sent and not expired, by type (receipt, sim_answer).", []string{"type"}, nil),
		},
	}

	for _, t := range []BindType{Transmitter, Receiver, Transceiver} {
		m.sessions.WithLabelValues(string(t))
	}

	m.binds.WithLabelValues("accepted")
	m.binds.WithLabelValues("rejected")

	for _, t := range []db.NotificationType{db.NotificationReceipt, db.NotificationSIMAnswer} {
		for _, r := range []db.NotificationResult{
			db.NotificationAcknowledged, db.NotificationRejected, db.NotificationExpired, db.NotificationNoUserData,
		} {
			m.sent.WithLabelValues(string(t), string(r))
		}
	}

	return m
}

func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.sessions, m.binds, m.sent, m.submit, m.pending}
}

func (m *Metrics) session(t BindType, delta float64) {
	if m != nil {
		m.sessions.WithLabelValues(string(t)).Add(delta)
	}
}

func (m *Metrics) bind(accepted bool) {
	if m == nil {
		return
	}

	result := "rejected"
	if accepted {
		result = "accepted"
	}

	m.binds.WithLabelValues(result).Inc()
}

func (m *Metrics) notified(t db.NotificationType, r db.NotificationResult, n int) {
	if m != nil && n > 0 {
		m.sent.WithLabelValues(string(t), string(r)).Add(float64(n))
	}
}

func (m *Metrics) submitted(d time.Duration) {
	if m != nil {
		m.submit.Observe(d.Seconds())
	}
}

type pendingCollector struct {
	store   PendingCounter
	leading func() bool
	desc    *prometheus.Desc
}

func (c *pendingCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *pendingCollector) Collect(ch chan<- prometheus.Metric) {
	if c.leading != nil && !c.leading() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), pendingTimeout)
	defer cancel()

	counts, err := c.store.CountPendingNotifications(ctx, time.Now())
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.desc, err)
		return
	}

	for t, n := range counts {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(n), string(t))
	}
}
