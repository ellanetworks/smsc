// Package intake counts the short messages the SMSC receives. Each entry point counts the messages it receives
// itself: the SGd handler those that phones send, and the API those that it submits, so that no message is counted
// twice.
package intake

import "github.com/prometheus/client_golang/prometheus"

// The entry points of a message, which label the messages received.
const (
	OriginMobile = "mobile"
	OriginAPI    = "api"
)

// The results of receiving a message: stored for delivery, refused, or not stored because the SMSC failed.
const (
	Accepted = "accepted"
	Rejected = "rejected"
	Error    = "error"
)

// Received counts the messages received. A nil Received counts nothing.
type Received struct {
	messages *prometheus.CounterVec
}

func New() *Received {
	r := &Received{
		messages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellasmsc_messages_received_total",
			Help: "Short messages received, one per TPDU, by origin (mobile, api) and result (accepted, rejected, error).",
		}, []string{"origin", "result"}),
	}

	// The API refuses an invalid request before it is split into messages, so it has no rejected messages.
	for _, series := range [][2]string{
		{OriginMobile, Accepted}, {OriginMobile, Rejected}, {OriginMobile, Error}, {OriginAPI, Accepted}, {OriginAPI, Error},
	} {
		r.messages.WithLabelValues(series[0], series[1])
	}

	return r
}

func (r *Received) Collectors() []prometheus.Collector {
	return []prometheus.Collector{r.messages}
}

// Add counts n messages of an origin, with a result.
func (r *Received) Add(origin, result string, n int) {
	if r == nil || n <= 0 {
		return
	}

	r.messages.WithLabelValues(origin, result).Add(float64(n))
}
