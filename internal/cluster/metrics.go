package cluster

import (
	gometrics "github.com/hashicorp/go-metrics"
	metricsprom "github.com/hashicorp/go-metrics/prometheus"
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	joinRequests *prometheus.CounterVec
	foreignPeers prometheus.Gauge
	locked       prometheus.Gauge
	keyAvailable prometheus.Gauge
}

func newMetrics() *metrics {
	return &metrics{
		joinRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellasmsc_cluster_join_requests_total",
			Help: "Join and hello requests received from other nodes, by result.",
		}, []string{"result"}),
		foreignPeers: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ellasmsc_cluster_foreign_peers",
			Help: "Nodes behind cluster.join that are members of another cluster.",
		}),
		locked: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ellasmsc_encryption_node_locked",
			Help: "1 when this node cannot unlock the encryption keys.",
		}),
		keyAvailable: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ellasmsc_encryption_key_available",
			Help: "1 when this node can use its configured encryption key, checked every minute.",
		}),
	}
}

func (m *metrics) setEncryption(s EncryptionStatus) {
	locked := 0.0
	if s.Locked {
		locked = 1
	}

	m.locked.Set(locked)
}

func (m *metrics) setKeyAvailable(available bool) {
	value := 0.0
	if available {
		value = 1
	}

	m.keyAvailable.Set(value)
}

func RegisterRaftMetrics(reg prometheus.Registerer) error {
	sink, err := metricsprom.NewPrometheusSinkFrom(metricsprom.PrometheusOpts{Registerer: reg})
	if err != nil {
		return err
	}

	conf := gometrics.DefaultConfig("")
	conf.EnableHostname = false
	conf.EnableHostnameLabel = false
	conf.EnableRuntimeMetrics = false

	_, err = gometrics.NewGlobal(conf, sink)

	return err
}

func (n *Node) Collectors() []prometheus.Collector {
	return []prometheus.Collector{
		n.metrics.joinRequests, n.metrics.foreignPeers, n.metrics.locked, n.metrics.keyAvailable,
		toleranceCollector{n},
	}
}

var toleranceDesc = prometheus.NewDesc("ellasmsc_cluster_failure_tolerance",
	"Voters that can fail without stopping writes. Exported by the leader.", nil, nil)

type toleranceCollector struct{ n *Node }

func (c toleranceCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- toleranceDesc
}

func (c toleranceCollector) Collect(ch chan<- prometheus.Metric) {
	if leading, _ := c.n.Leader(); !leading {
		return
	}

	if h := c.n.health.snapshot(); h.Servers != nil {
		ch <- prometheus.MustNewConstMetric(toleranceDesc, prometheus.GaugeValue, float64(h.FailureTolerance))
	}
}
