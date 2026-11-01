// Package logging makes the SMSC's logger: JSON lines, each carrying the trace context of the span it was logged in,
// so that a log leads to its trace and a trace to its logs.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// The fields of a log's trace context, as OpenTelemetry names them in logs that are not OTLP: the hex trace ID, span
// ID and trace flags of the W3C Trace Context.
const (
	TraceIDKey    = "trace_id"
	SpanIDKey     = "span_id"
	TraceFlagsKey = "trace_flags"
)

// New returns a logger that writes JSON lines to w, with Ella Core's ts and lowercase level, and durations as "1.5s".
// A log made with a context of a span, as by InfoContext, carries that span's trace context.
func New(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(NewHandler(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch {
			case a.Key == slog.TimeKey:
				a.Key = "ts"
			case a.Key == slog.LevelKey:
				a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
			case a.Value.Kind() == slog.KindDuration:
				a.Value = slog.StringValue(a.Value.Duration().String())
			}

			return a
		},
	})))
}

// NewHandler returns a handler that adds to each record the trace context of the span in its context, when that span
// context is valid, and passes it to next. The fields go in the record's group, so a logger with a group, which the
// SMSC does not use, would nest them.
func NewHandler(next slog.Handler) slog.Handler {
	return traceHandler{next: next}
}

type traceHandler struct {
	next slog.Handler
}

func (h traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r = r.Clone()
		r.AddAttrs(
			slog.String(TraceIDKey, sc.TraceID().String()),
			slog.String(SpanIDKey, sc.SpanID().String()),
			slog.String(TraceFlagsKey, sc.TraceFlags().String()),
		)
	}

	return h.next.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{next: h.next.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{next: h.next.WithGroup(name)}
}
