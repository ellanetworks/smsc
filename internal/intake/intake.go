package intake

import "github.com/prometheus/client_golang/prometheus"

const (
	OriginMobile = "mobile"
	OriginAPI    = "api"
	OriginSMPP   = "smpp"
)

const (
	Accepted  = "accepted"
	Rejected  = "rejected"
	Error     = "error"
	Throttled = "throttled"
)

type Received struct {
	messages *prometheus.CounterVec
}

func New() *Received {
	r := &Received{
		messages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellasmsc_messages_received_total",
			Help: "Short messages received, one per TPDU, by origin (mobile, api, smpp) and result (accepted, rejected, error, and throttled for smpp).",
		}, []string{"origin", "result"}),
	}

	for _, series := range [][2]string{
		{OriginMobile, Accepted},
		{OriginMobile, Rejected},
		{OriginMobile, Error},
		{OriginAPI, Accepted},
		{OriginAPI, Error},
		{OriginSMPP, Accepted},
		{OriginSMPP, Rejected},
		{OriginSMPP, Error},
		{OriginSMPP, Throttled},
	} {
		r.messages.WithLabelValues(series[0], series[1])
	}

	return r
}

func (r *Received) Collectors() []prometheus.Collector {
	return []prometheus.Collector{r.messages}
}

func (r *Received) Add(origin, result string, n int) {
	if r == nil || n <= 0 {
		return
	}

	r.messages.WithLabelValues(origin, result).Add(float64(n))
}
