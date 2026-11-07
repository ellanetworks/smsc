package cluster

import (
	gometrics "github.com/hashicorp/go-metrics"
	metricsprom "github.com/hashicorp/go-metrics/prometheus"
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	mismatches      prometheus.Counter
	formationState  *prometheus.GaugeVec
	discoveredPeers prometheus.Gauge
	joinAttempts    *prometheus.CounterVec
	joinRequests    *prometheus.CounterVec
	foreignPeers    prometheus.Gauge
}

func newMetrics() *metrics {
	return &metrics{
		mismatches: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ellasmsc_cluster_state_mismatches_total",
			Help: "Members whose database was found to differ from the leader's.",
		}),
		formationState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ellasmsc_cluster_formation_state",
			Help: "1 for the node's cluster formation state (forming, joining, member, removed), 0 for the others.",
		}, []string{"state"}),
		discoveredPeers: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ellasmsc_cluster_discovered_peers",
			Help: "Other nodes behind cluster.join that answered the last hello of this forming node.",
		}),
		joinAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellasmsc_cluster_join_attempts_total",
			Help: "Attempts of this node to join a cluster through cluster.join, by result.",
		}, []string{"result"}),
		joinRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ellasmsc_cluster_join_requests_total",
			Help: "Join and hello requests received from other nodes, by result.",
		}, []string{"result"}),
		foreignPeers: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ellasmsc_cluster_foreign_peers",
			Help: "Nodes behind cluster.join that are members of another cluster.",
		}),
	}
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
		n.metrics.mismatches, n.metrics.formationState, n.metrics.discoveredPeers, n.metrics.joinAttempts,
		n.metrics.joinRequests, n.metrics.foreignPeers,
		toleranceCollector{n},
		lagCollector{n},
	}
}

var lagDesc = prometheus.NewDesc("ellasmsc_cluster_apply_lag_entries",
	"Log entries committed by the cluster leader but not yet applied to this node's database. Absent while the "+
		"leader's commit index is unknown.", nil, nil)

type lagCollector struct{ n *Node }

func (c lagCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- lagDesc
}

func (c lagCollector) Collect(ch chan<- prometheus.Metric) {
	if lag, known := c.n.ApplyLag(); known {
		ch <- prometheus.MustNewConstMetric(lagDesc, prometheus.GaugeValue, float64(lag))
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
