package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/prometheus/client_golang/prometheus"
)

// failingStore fails to store messages.
type failingStore struct {
	*db.DB
}

func (failingStore) CreateMessages(context.Context, []db.NewMessage) ([]int64, error) {
	return nil, errors.New("disk full")
}

func newMetricsAPI(t *testing.T, failStore bool) http.Handler {
	t.Helper()

	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = database.Close() })

	var store api.Store = database
	if failStore {
		store = failingStore{database}
	}

	received := intake.New()
	registry := prometheus.NewRegistry()
	registry.MustRegister(received.Collectors()...)

	return api.NewHandler(api.Config{
		Store:    store,
		Diameter: fakeDiameter{},
		Settings: settings.NewLive(database, testSettings(t, database)),
		Notify:   func() {},
		Received: received,
		Metrics:  registry,
		Now:      func() time.Time { return testNow },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
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

func post(t *testing.T, h http.Handler, body string) int {
	t.Helper()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/messages",
		strings.NewReader(body)))

	return rec.Code
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
	h := newMetricsAPI(t, false)

	// A long text is stored as two parts, each counted, and an invalid request is not a message.
	long := strings.Repeat("日本語", 30)
	if code := post(t, h, `{"from": "+15550001111", "to": "+15551230002", "text": "`+long+`"}`); code != http.StatusCreated {
		t.Fatalf("POST long text: %d", code)
	}

	if code := post(t, h, `{"from": "+15550001111", "to": "+15551230002"}`); code != http.StatusBadRequest {
		t.Fatalf("POST without text: %d", code)
	}

	wantSeries(t, scrape(t, h),
		`ellasmsc_messages_received_total{origin="api",result="accepted"} 2`,
		`ellasmsc_messages_received_total{origin="api",result="error"} 0`,
	)
}

func TestMetricsCountStoreFailures(t *testing.T) {
	h := newMetricsAPI(t, true)

	if code := post(t, h, `{"from": "+15550001111", "to": "+15551230002", "text": "hi"}`); code != http.StatusInternalServerError {
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
