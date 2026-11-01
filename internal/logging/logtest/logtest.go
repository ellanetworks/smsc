// Package logtest records the logs of the SMSC's logger in tests.
package logtest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	"github.com/ellanetworks/smsc/internal/logging"
	"go.opentelemetry.io/otel/trace"
)

// Recorder records the logs of a logger as the SMSC's, at level debug.
type Recorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Logger returns a logger as the SMSC's that logs to r.
func (r *Recorder) Logger() *slog.Logger {
	return logging.New(r, slog.LevelDebug)
}

func (r *Recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.buf.Write(p)
}

// Records returns the logs so far, each a decoded JSON object.
func (r *Recorder) Records(t *testing.T) []map[string]any {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	var out []map[string]any

	sc := bufio.NewScanner(bytes.NewReader(r.buf.Bytes()))
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("log %q is not a JSON object: %v", sc.Text(), err)
		}

		out = append(out, rec)
	}

	return out
}

// Messages returns the logs with a msg.
func (r *Recorder) Messages(t *testing.T, msg string) []map[string]any {
	t.Helper()

	var out []map[string]any

	for _, rec := range r.Records(t) {
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}

	return out
}

// InSpan fails the test unless the log carries the trace context of the span.
func InSpan(t *testing.T, rec map[string]any, span trace.SpanContext) {
	t.Helper()

	want := map[string]string{
		logging.TraceIDKey:    span.TraceID().String(),
		logging.SpanIDKey:     span.SpanID().String(),
		logging.TraceFlagsKey: span.TraceFlags().String(),
	}

	for key, v := range want {
		if rec[key] != v {
			t.Errorf("log %q: %s = %v, want %s", rec["msg"], key, rec[key], v)
		}
	}
}

// NoSpan fails the test if the log carries a trace context.
func NoSpan(t *testing.T, rec map[string]any) {
	t.Helper()

	for _, key := range []string{logging.TraceIDKey, logging.SpanIDKey, logging.TraceFlagsKey} {
		if v, ok := rec[key]; ok {
			t.Errorf("log %q: %s = %v, want none", rec["msg"], key, v)
		}
	}
}
