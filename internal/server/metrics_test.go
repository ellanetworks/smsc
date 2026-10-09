package server_test

import (
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/prometheus/client_golang/prometheus/testutil/promlint"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func TestMetrics(t *testing.T) {
	skipIfNoSCTP(t)

	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))

	body := scrape(t, s)

	problems, err := promlint.New(bytes.NewReader(body)).Lint()
	if err != nil {
		t.Fatalf("lint: %v", err)
	}

	for _, p := range problems {
		t.Errorf("lint: %s: %s", p.Metric, p.Text)
	}

	families := parseFamilies(t, body)

	for _, name := range []string{
		"go_goroutines",
		"process_start_time_seconds",
		"ellasmsc_build_info",
		"ellasmsc_database_query_duration_seconds",
		"ellasmsc_database_query_errors_total",
		"ellasmsc_database_storage_bytes",
		"ellasmsc_messages_received_total",
		"ellasmsc_messages_completed_total",
		"ellasmsc_messages_pending",
		"ellasmsc_message_delivery_duration_seconds",
		"ellasmsc_peer_requests_total",
		"ellasmsc_peer_request_duration_seconds",
		"ellasmsc_hss_peers",
	} {
		if _, ok := families[name]; !ok {
			t.Errorf("%s is missing", name)
		}
	}

	if got := labels(families["ellasmsc_build_info"].GetMetric()[0])["version"]; got == "" {
		t.Error("ellasmsc_build_info has no version")
	}

	// The SMSC has read its settings at start, so the database has timed a call.
	if n := families["ellasmsc_database_query_duration_seconds"].GetMetric()[0].GetHistogram().GetSampleCount(); n == 0 {
		t.Error("no database call was timed")
	}

	if n := len(families["ellasmsc_database_storage_bytes"].GetMetric()); n != 2 {
		t.Errorf("ellasmsc_database_storage_bytes has %d series, want 2", n)
	}
}

func TestMetricsOfMessages(t *testing.T) {
	core, s := newSMSC(t, alice, bob)

	// One message from a phone is delivered, and one to a phone the HSS does not know fails.
	if rc := core.submit(alice, bob, "hello bob"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusDelivered)

	if rc := core.submit(alice, subscriber{imsi: "001010000000009", msisdn: "15551230009"}, "anyone?"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 2, db.StatusFailed)

	want := map[string]float64{
		`ellasmsc_messages_received_total{origin="mobile",result="accepted"}`: 2,
		`ellasmsc_messages_received_total{origin="mobile",result="rejected"}`: 0,
		`ellasmsc_messages_completed_total{status="delivered"}`:               1,
		`ellasmsc_messages_completed_total{status="failed"}`:                  1,
		`ellasmsc_messages_completed_total{status="expired"}`:                 0,
		`ellasmsc_messages_pending{state="due"}`:                              0,
		`ellasmsc_messages_pending{state="waiting"}`:                          0,
		`ellasmsc_message_delivery_duration_seconds_count`:                    1,
	}

	// A message's status is stored before it is counted, so the counts may lag the status by a moment.
	eventually(t, "message metrics", func() bool {
		got := values(parseFamilies(t, scrape(t, s)))
		for k, v := range want {
			if got[k] != v {
				return false
			}
		}

		return true
	})
}

func TestMetricsOfPeers(t *testing.T) {
	core, s := newSMSC(t, alice, bob)

	// A message delivered asks the HSS for a route, and the MME to deliver it.
	if rc := core.submit(alice, bob, "hello bob"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusDelivered)

	// A message to a phone the HSS says is unreachable stays pending.
	core.setAbsent(bob.msisdn, true)

	if rc := core.submit(alice, bob, "are you there?"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	want := map[string]float64{
		`ellasmsc_peer_requests_total{interface="s6c",result="success"}`:     1,
		`ellasmsc_peer_requests_total{interface="s6c",result="absent_user"}`: 1,
		`ellasmsc_peer_requests_total{interface="sgd",result="success"}`:     1,
		`ellasmsc_peer_requests_total{interface="sgd",result="failure"}`:     0,
		`ellasmsc_peer_request_duration_seconds_count{interface="s6c"}`:      2,
		`ellasmsc_peer_request_duration_seconds_count{interface="sgd"}`:      1,
		`ellasmsc_hss_peers`:                         1,
		`ellasmsc_messages_pending{state="due"}`:     0,
		`ellasmsc_messages_pending{state="waiting"}`: 1,
	}

	eventually(t, "peer metrics", func() bool {
		got := values(parseFamilies(t, scrape(t, s)))
		for k, v := range want {
			if got[k] != v {
				return false
			}
		}

		return true
	})

	// Without the core, there is no HSS to route through.
	if err := core.node.SetPeers(nil); err != nil {
		t.Fatalf("SetPeers: %v", err)
	}

	eventually(t, "no HSS", func() bool {
		return values(parseFamilies(t, scrape(t, s)))[`ellasmsc_hss_peers`] == 0
	})
}

// values are the samples of the families by series, counters, gauges and histogram counts.
func values(families map[string]*dto.MetricFamily) map[string]float64 {
	out := map[string]float64{}

	for name, f := range families {
		for _, m := range f.GetMetric() {
			var pairs []string
			for _, l := range m.GetLabel() {
				pairs = append(pairs, l.GetName()+`="`+l.GetValue()+`"`)
			}

			suffix := ""
			if len(pairs) > 0 {
				suffix = "{" + strings.Join(pairs, ",") + "}"
			}

			switch {
			case m.Counter != nil:
				out[name+suffix] = m.GetCounter().GetValue()
			case m.Gauge != nil:
				out[name+suffix] = m.GetGauge().GetValue()
			case m.Histogram != nil:
				out[name+"_count"+suffix] = float64(m.GetHistogram().GetSampleCount())
			}
		}
	}

	return out
}

func labels(m *dto.Metric) map[string]string {
	l := map[string]string{}
	for _, p := range m.GetLabel() {
		l[p.GetName()] = p.GetValue()
	}

	return l
}

func parseFamilies(t *testing.T, body []byte) map[string]*dto.MetricFamily {
	t.Helper()

	parser := expfmt.NewTextParser(model.UTF8Validation)

	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	return families
}

func scrape(t *testing.T, s *smsc) []byte {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://"+s.server.APIAddr().String()+"/api/v1/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics: %s", res.Status)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}

	return body
}
