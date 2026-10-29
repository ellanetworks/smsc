package tracing

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceParentRoundTrip(t *testing.T) {
	const traceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

	links := Links(traceparent)
	if len(links) != 1 {
		t.Fatalf("Links = %+v, want one", links)
	}

	if got := TraceParent(trace.ContextWithSpanContext(context.Background(), links[0].SpanContext)); got != traceparent {
		t.Fatalf("TraceParent = %q, want %q", got, traceparent)
	}

	if got := TraceParent(context.Background()); got != "" {
		t.Fatalf("TraceParent without a span = %q", got)
	}

	for _, invalid := range []string{"", "garbage", "00-00000000000000000000000000000000-b7ad6b7169203331-01"} {
		if links := Links(invalid); links != nil {
			t.Errorf("Links(%q) = %+v, want none", invalid, links)
		}
	}
}

// TestNewProviderResource checks that the spans name the SMSC and its version, and that the SDK's environment
// variables apply over them.
func TestNewProviderResource(t *testing.T) {
	for name, env := range map[string]string{"default": "", "environment": "smsc-west"} {
		t.Run(name, func(t *testing.T) {
			want := ServiceName

			if env != "" {
				t.Setenv("OTEL_SERVICE_NAME", env)
				want = env
			}

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

			res := attribute.NewSet(rec.Ended()[0].Resource().Attributes()...)
			service, _ := res.Value("service.name")
			version, _ := res.Value("service.version")

			if service.AsString() != want || version.AsString() != "v1.2.3" {
				t.Fatalf("service %q version %q, want %q v1.2.3", service.AsString(), version.AsString(), want)
			}
		})
	}
}
