package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/logging"
	"go.opentelemetry.io/otel/trace"
)

const (
	traceID = "0af7651916cd43dd8448eb211c80319c"
	spanID  = "b7ad6b7169203331"
)

func spanContext(t *testing.T, flags trace.TraceFlags) context.Context {
	t.Helper()

	tid, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		t.Fatal(err)
	}

	sid, err := trace.SpanIDFromHex(spanID)
	if err != nil {
		t.Fatal(err)
	}

	return trace.ContextWithSpanContext(context.Background(),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: flags}))
}

func record(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log %q is not a JSON object: %v", buf.String(), err)
	}

	return got
}

// TestTraceContext checks that a log carries the trace context of the span in its context, as OpenTelemetry names
// its fields, and none without one.
func TestTraceContext(t *testing.T) {
	tests := map[string]struct {
		ctx  context.Context
		want map[string]any
	}{
		"sampled": {
			ctx:  spanContext(t, trace.FlagsSampled),
			want: map[string]any{"trace_id": traceID, "span_id": spanID, "trace_flags": "01"},
		},
		"not sampled": {
			ctx:  spanContext(t, 0),
			want: map[string]any{"trace_id": traceID, "span_id": spanID, "trace_flags": "00"},
		},
		"no span":      {ctx: context.Background()},
		"invalid span": {ctx: trace.ContextWithSpanContext(context.Background(), trace.SpanContext{})},
		"no context":   {ctx: nil},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer

			logger := logging.New(&buf, slog.LevelInfo).With(slog.Int64("message_id", 7))

			if tc.ctx == nil {
				logger.Info("no context")
			} else {
				logger.InfoContext(tc.ctx, "with context")
			}

			got := record(t, &buf)

			if got["message_id"] != float64(7) {
				t.Errorf("message_id = %v, want 7", got["message_id"])
			}

			for _, key := range []string{logging.TraceIDKey, logging.SpanIDKey, logging.TraceFlagsKey} {
				want, ok := tc.want[key]

				switch v, has := got[key]; {
				case ok && v != want:
					t.Errorf("%s = %v, want %v", key, v, want)
				case !ok && has:
					t.Errorf("%s = %v, want none", key, v)
				}
			}
		})
	}
}

// TestFormat checks Ella Core's fields: ts, a lowercase level, and durations in words.
func TestFormat(t *testing.T) {
	var buf bytes.Buffer

	logger := logging.New(&buf, slog.LevelDebug)
	logger.WarnContext(spanContext(t, trace.FlagsSampled), "slow", slog.Duration("took", 1500*time.Millisecond))

	got := record(t, &buf)

	if _, ok := got["ts"]; !ok {
		t.Error("no ts")
	}

	if got["level"] != "warn" {
		t.Errorf("level = %v, want warn", got["level"])
	}

	if got["took"] != "1.5s" {
		t.Errorf("took = %v, want 1.5s", got["took"])
	}
}

// TestLevel checks that the handler keeps the level of the one it wraps.
func TestLevel(t *testing.T) {
	var buf bytes.Buffer

	logging.New(&buf, slog.LevelInfo).DebugContext(spanContext(t, trace.FlagsSampled), "hidden")

	if buf.Len() != 0 {
		t.Errorf("logged %q below the level", buf.String())
	}
}
