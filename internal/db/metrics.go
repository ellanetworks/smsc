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
}

// finalStatuses are the statuses that each kind of message ends with. Only a short message is replaced.
var finalStatuses = map[MessageKind][]MessageStatus{
	KindMessage:      {StatusDelivered, StatusFailed, StatusExpired, StatusReplaced},
	KindStatusReport: {StatusDelivered, StatusFailed, StatusExpired},
}

func newMetrics() metrics {
	return metrics{
		completed: newCompleted(),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "ellasmsc_database_query_duration_seconds",
			Help: "Duration of database calls, the wait for the connection, statements, transaction and row scanning included.",
			// Most statements take well under a millisecond, and a statement waits up to the 5 s busy timeout.
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

// newCompleted returns the count of the messages that reached a final status, which the database counts once the
// transaction that stores the status commits.
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

// completion is a final status that a transaction stores, which the metrics count once it commits.
type completion struct {
	kind   MessageKind
	status MessageStatus
}

// count counts the final statuses that a transaction stored, once it committed.
func (m metrics) count(completed []completion) {
	for _, done := range completed {
		m.completed.WithLabelValues(string(done.kind), string(done.status)).Inc()
	}
}

// observe starts timing a call, and returns the function that ends it, which counts *err if it is a failure:
// defer d.observe(&err)().
func (d *DB) observe(err *error) func() {
	start := time.Now()

	return func() {
		d.metrics.duration.Observe(time.Since(start).Seconds())

		if failed(*err) {
			d.metrics.errors.Inc()
		}
	}
}

// failed reports whether err is a failure of the database rather than an answer to the call.
func failed(err error) bool {
	return err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrDuplicate)
}

// Collectors are the metrics of the database: its calls, its size on disk, and what the messages it stores become. A
// call's duration includes the wait for the connection, which every call shares.
func (d *DB) Collectors() []prometheus.Collector {
	return []prometheus.Collector{
		d.metrics.duration, d.metrics.errors, storageCollector{path: d.path}, d.metrics.completed,
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

// fileSize is the size of the file, and 0 if there is none: SQLite removes the write-ahead log when the last
// connection closes.
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
