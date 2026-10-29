package tracing

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestTraceParentRoundTrip checks that the traceparent of a recorded span links back to it, and that a span the SMSC
// does not record, such as a peer's trace context while tracing is off, gives none.
func TestTraceParentRoundTrip(t *testing.T) {
	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "span")
	defer span.End()

	links := Links(TraceParent(ctx))
	if len(links) != 1 || links[0].SpanContext.TraceID() != span.SpanContext().TraceID() ||
		links[0].SpanContext.SpanID() != span.SpanContext().SpanID() {
		t.Fatalf("Links(TraceParent) = %+v, want one to %v", links, span.SpanContext())
	}

	remote := trace.ContextWithRemoteSpanContext(context.Background(), span.SpanContext())
	untraced, _ := noop.NewTracerProvider().Tracer("test").Start(remote, "span")

	for name, ctx := range map[string]context.Context{
		"no span":       context.Background(),
		"remote parent": remote,
		"not recorded":  untraced,
	} {
		if got := TraceParent(ctx); got != "" {
			t.Errorf("TraceParent with %s = %q, want none", name, got)
		}
	}

	for _, invalid := range []string{"", "garbage", "00-00000000000000000000000000000000-b7ad6b7169203331-01"} {
		if links := Links(invalid); links != nil {
			t.Errorf("Links(%q) = %+v, want none", invalid, links)
		}
	}
}

// TestNewProvider checks that the spans name the SMSC and its version, and that the OTEL_* environment variables do
// not apply: the configuration file is the only one.
func TestNewProvider(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "smsc-west")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")

	// No collector listens on port 1, so that the test exports nothing.
	tp, err := NewProvider(t.Context(), "127.0.0.1:1", "v1.2.3", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	rec := tracetest.NewSpanRecorder()
	tp.RegisterSpanProcessor(rec)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		_ = tp.Shutdown(ctx)
	})

	_, span := tp.Tracer("test").Start(t.Context(), "span")
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}

	res := attribute.NewSet(spans[0].Resource().Attributes()...)
	service, _ := res.Value("service.name")
	version, _ := res.Value("service.version")

	if service.AsString() != ServiceName || version.AsString() != "v1.2.3" {
		t.Fatalf("service %q version %q, want %q v1.2.3", service.AsString(), version.AsString(), ServiceName)
	}
}
