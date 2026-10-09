package server

import (
	"context"
	"errors"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/prometheus/client_golang/prometheus"
)

// The interfaces to the peers, which label the requests to them: S6c to the HSS, SGd to the MME or the AMF.
const (
	interfaceS6c = "s6c"
	interfaceSGd = "sgd"
)

// The results of a request to a peer: answered with success, answered with a failure, answered that the phone is
// unreachable, not answered, or not answered in time.
const (
	peerSuccess    = "success"
	peerFailure    = "failure"
	peerAbsentUser = "absent_user"
	peerError      = "error"
	peerTimeout    = "timeout"
)

var (
	peerInterfaces = []string{interfaceS6c, interfaceSGd}
	peerResults    = []string{peerSuccess, peerFailure, peerAbsentUser, peerError, peerTimeout}
)

// interfaceOf is the interface of an application, if it is one of the SMSC's.
func interfaceOf(applicationID uint32) (string, bool) {
	switch applicationID {
	case s6c.ApplicationID:
		return interfaceS6c, true
	case sgd.ApplicationID:
		return interfaceSGd, true
	default:
		return "", false
	}
}

type peerMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newPeerMetrics() *peerMetrics {
	m := &peerMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellasmsc_peer_requests_total",
			Help: "Requests to the HSS (s6c) and to the MME or AMF (sgd), by interface and result: success, failure " +
				"(answered with an error), absent_user (the phone is unreachable), error (not answered) and timeout.",
		}, []string{"interface", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "ellasmsc_peer_request_duration_seconds",
			Help: "Duration of requests to the HSS and to the MME or AMF, by interface.",
			// Up past the 30 s attempt timeout, so that the requests that time out are in a bucket.
			Buckets:                         []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60},
			NativeHistogramBucketFactor:     1.1,
			NativeHistogramMaxBucketNumber:  100,
			NativeHistogramMinResetDuration: time.Hour,
		}, []string{"interface"}),
	}

	for _, iface := range peerInterfaces {
		m.duration.WithLabelValues(iface)

		for _, result := range peerResults {
			m.requests.WithLabelValues(iface, result)
		}
	}

	return m
}

func (m *peerMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{m.requests, m.duration}
}

// request counts a request to a peer, of a result, and times it.
func (m *peerMetrics) request(iface, result string, elapsed time.Duration) {
	m.requests.WithLabelValues(iface, result).Inc()
	m.duration.WithLabelValues(iface).Observe(elapsed.Seconds())
}

func peerResult(ans *diameter.Message, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return peerTimeout
	case err != nil:
		return peerError
	}

	r, err := tgpp.ParseResult(ans)

	switch {
	case err != nil:
		return peerError
	case r.Success():
		return peerSuccess
	case r.IsExperimental(tgpp.ResultErrorAbsentUser):
		return peerAbsentUser
	default:
		return peerFailure
	}
}

var hssPeersDesc = prometheus.NewDesc(
	"ellasmsc_hss_peers",
	"Open Diameter peers in the operator's realm that serve S6c, which the SMSC asks to route messages. At 0, "+
		"messages to phones wait.",
	nil, nil,
)

// hssPeersCollector counts the HSSs the SMSC can route through when scraped.
type hssPeersCollector struct {
	count func() int
}

func (c hssPeersCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- hssPeersDesc
}

func (c hssPeersCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(hssPeersDesc, prometheus.GaugeValue, float64(c.count()))
}
