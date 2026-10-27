package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/prometheus/client_golang/prometheus"
)

// failingStore fails to store messages.
type failingStore struct {
	*db.DB
}

func (failingStore) CreateMessages(context.Context, []db.NewMessage) ([]int64, error) {
	return nil, errors.New("disk full")
}

// newMetricsAPI returns an API with metrics, and the access token of its admin.
func newMetricsAPI(t *testing.T, failStore bool) (http.Handler, string) {
	t.Helper()

	database := openStore(t)
	cfg := testConfig(t, database)

	if failStore {
		cfg.Store = failingStore{database}
	}

	cfg.Received = intake.New()
	registry := prometheus.NewRegistry()
	registry.MustRegister(cfg.Received.Collectors()...)
	cfg.Metrics = registry

	h := api.NewHandler(cfg)

	return h, initialize(t, h)
}

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics: %d", rec.Code)
	}

	return rec.Body.String()
}

func post(t *testing.T, h http.Handler, token, body string) int {
	t.Helper()

	return serve(h, request(http.MethodPost, "/api/v1/messages", body, token)).Code
}

func wantSeries(t *testing.T, body string, series ...string) {
	t.Helper()

	for _, s := range series {
		if !strings.Contains(body, "\n"+s+"\n") {
			t.Errorf("metrics do not have %q", s)
		}
	}
}

func TestMetricsCountMessagesPerPart(t *testing.T) {
	h, token := newMetricsAPI(t, false)

	// A long text is stored as two parts, each counted, and an invalid request is not a message.
	long := strings.Repeat("日本語", 30)
	if code := post(t, h, token, `{"from": "+15550001111", "to": "+15551230002", "text": "`+long+`"}`); code != http.StatusCreated {
		t.Fatalf("POST long text: %d", code)
	}

	if code := post(t, h, token, `{"from": "+15550001111", "to": "+15551230002"}`); code != http.StatusBadRequest {
		t.Fatalf("POST without text: %d", code)
	}

	wantSeries(t, scrape(t, h),
		`ellasmsc_messages_received_total{origin="api",result="accepted"} 2`,
		`ellasmsc_messages_received_total{origin="api",result="error"} 0`,
	)
}

func TestMetricsCountStoreFailures(t *testing.T) {
	h, token := newMetricsAPI(t, true)

	if code := post(t, h, token, `{"from": "+15550001111", "to": "+15551230002", "text": "hi"}`); code != http.StatusInternalServerError {
		t.Fatalf("POST: %d", code)
	}

	wantSeries(t, scrape(t, h),
		`ellasmsc_messages_received_total{origin="api",result="accepted"} 0`,
		`ellasmsc_messages_received_total{origin="api",result="error"} 1`,
	)
}

func TestMetricsWithoutRegistry(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	if body := scrape(t, a.handler); body != "" {
		t.Fatalf("metrics without a registry = %q, want none", body)
	}
}
