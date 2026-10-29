package api_test

import (
	"context"
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/ellanetworks/smsc/internal/api"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestAPISpans checks that a request to the API has a server span named by its route, that links to the caller's
// trace context rather than continue it, and that a message it creates keeps the span as its creation context. The UI,
// probes, metrics and profiles are not traced.
func TestAPISpans(t *testing.T) {
	store := openStore(t)
	rec := tracetest.NewSpanRecorder()

	cfg := testConfig(t, store)
	cfg.Frontend = fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}
	cfg.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	a := &testAPI{store: store, handler: api.NewHandler(cfg), now: testNow}
	a.token = initialize(t, a.handler)

	for _, path := range []string{"/", "/api/v1/livez", "/api/v1/readyz", "/api/v1/metrics", "/api/v1/pprof/heap"} {
		serve(a.handler, request(http.MethodGet, path, "", a.token))
	}

	before := len(rec.Ended())

	req := request(http.MethodPost, "/api/v1/messages", `{"from": "+15550001111", "to": "+15551230002", "text": "hi"}`, a.token)
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")

	if rec := serve(a.handler, req); rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/messages = %d %s", rec.Code, rec.Body)
	}

	spans := rec.Ended()
	for _, s := range spans[:before] {
		if s.Name() != "POST /api/v1/init" {
			t.Errorf("unexpected span %q before the message", s.Name())
		}
	}

	if len(spans) != before+1 {
		t.Fatalf("ended %d spans for the message, want 1", len(spans)-before)
	}

	s := spans[before]
	if s.Name() != "POST /api/v1/messages" || s.SpanKind() != trace.SpanKindServer || s.Parent().IsValid() {
		t.Fatalf("span %q kind %v parent %v, want a server span without a parent", s.Name(), s.SpanKind(), s.Parent())
	}

	if links := s.Links(); len(links) != 1 || links[0].SpanContext.TraceID().String() != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("links = %+v, want one to the caller's trace context", links)
	}

	attrs := attribute.NewSet(s.Attributes()...)
	if route, _ := attrs.Value("http.route"); route.AsString() != "/api/v1/messages" {
		t.Errorf("http.route = %q", route.AsString())
	}

	if code, _ := attrs.Value("http.response.status_code"); code.AsInt64() != http.StatusCreated {
		t.Errorf("http.response.status_code = %v", code.AsInt64())
	}

	m, err := store.GetMessage(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}

	sc := s.SpanContext()
	if want := "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"; m.TraceParent != want {
		t.Errorf("message traceparent = %q, want the span's %q", m.TraceParent, want)
	}
}
