package db

import (
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	duration  prometheus.Histogram
	errors    prometheus.Counter
	completed *prometheus.CounterVec
	purged    prometheus.Counter
}

var finalStatuses = map[MessageKind][]MessageStatus{
	KindMessage:      {StatusDelivered, StatusFailed, StatusExpired, StatusReplaced},
	KindStatusReport: {StatusDelivered, StatusFailed, StatusExpired},
}

func newMetrics() metrics {
	return metrics{
		completed: newCompleted(),
		purged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ellasmsc_messages_purged_total",
			Help: "Short messages deleted once their retention period passed, with their status reports and delivery attempts.",
		}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:                            "ellasmsc_database_query_duration_seconds",
			Help:                            "Duration of database calls, the wait for the connection, statements, transaction and row scanning included.",
			Buckets:                         []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 5},
			NativeHistogramBucketFactor:     1.1,
			NativeHistogramMaxBucketNumber:  100,
			NativeHistogramMinResetDuration: time.Hour,
		}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ellasmsc_database_query_errors_total",
			Help: "Database calls that failed. A record not found or a duplicate message is a result, not a failure.",
		}),
	}
}

func newCompleted() *prometheus.CounterVec {
	completed := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ellasmsc_messages_completed_total",
		Help: "Short messages and status reports that reached a final status, by type (message, status_report) and " +
			"status (delivered, failed, expired, and replaced for a message).",
	}, []string{"type", "status"})

	for kind, statuses := range finalStatuses {
		for _, status := range statuses {
			completed.WithLabelValues(string(kind), string(status))
		}
	}

	return completed
}

type completion struct {
	kind   MessageKind
	status MessageStatus
}

func (m metrics) count(completed []completion) {
	for _, done := range completed {
		m.completed.WithLabelValues(string(done.kind), string(done.status)).Inc()
	}
}

func (d *DB) observe(err *error) func() {
	start := time.Now()

	return func() {
		d.metrics.duration.Observe(time.Since(start).Seconds())

		if failed(*err) {
			d.metrics.errors.Inc()
		}
	}
}

func failed(err error) bool {
	return err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrDuplicate) &&
		!errors.Is(err, ErrAlreadyExists)
}

func (d *DB) Collectors() []prometheus.Collector {
	return []prometheus.Collector{
		d.metrics.duration, d.metrics.errors,
		storageCollector{path: d.path},
		d.metrics.completed,
		d.metrics.purged,
	}
}

var storageDesc = prometheus.NewDesc(
	"ellasmsc_database_storage_bytes",
	"Size of the database on disk, by file: the main file and its write-ahead log.",
	[]string{"file"}, nil,
)

type storageCollector struct {
	path string
}

func (c storageCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- storageDesc
}

func (c storageCollector) Collect(ch chan<- prometheus.Metric) {
	for _, f := range []struct{ label, path string }{{"main", c.path}, {"wal", c.path + "-wal"}} {
		size, err := fileSize(f.path)
		if err != nil {
			ch <- prometheus.NewInvalidMetric(storageDesc, err)
			continue
		}

		ch <- prometheus.MustNewConstMetric(storageDesc, prometheus.GaugeValue, float64(size), f.label)
	}
}

func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}

	if err != nil {
		return 0, err
	}

	return fi.Size(), nil
}
