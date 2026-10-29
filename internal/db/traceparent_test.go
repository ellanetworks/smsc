package db

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
)

func tracedContext(t *testing.T, traceID, spanID string) (context.Context, string) {
	t.Helper()

	tid, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		t.Fatal(err)
	}

	sid, err := trace.SpanIDFromHex(spanID)
	if err != nil {
		t.Fatal(err)
	}

	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled})

	return trace.ContextWithSpanContext(context.Background(), sc), "00-" + traceID + "-" + spanID + "-01"
}

// TestMessagesStoreTheirCreationContext checks that a message, and a status report, keep the trace context of the
// request that stores them, and that one stored outside a trace has none.
func TestMessagesStoreTheirCreationContext(t *testing.T) {
	d := openTestDB(t)
	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	submit, submitParent := tracedContext(t, "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331")

	id, err := d.CreateMessage(submit, testMessage(7, false, []byte{0x01}, at))
	if err != nil {
		t.Fatal(err)
	}

	untraced, err := d.CreateMessage(context.Background(), testMessage(8, false, []byte{0x01}, at))
	if err != nil {
		t.Fatal(err)
	}

	deliver, deliverParent := tracedContext(t, "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")

	if err := d.CompleteMessage(deliver, id, StatusDelivered, at.Add(time.Minute), testReport(at.Add(time.Minute)), 0); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		id   int64
		want string
	}{
		{"traced", id, submitParent},
		{"untraced", untraced, ""},
	} {
		m, err := d.GetMessage(context.Background(), tc.id)
		if err != nil || m.TraceParent != tc.want {
			t.Errorf("%s message traceparent = %q, %v; want %q", tc.name, m.TraceParent, err, tc.want)
		}
	}

	reports, err := listStatusReports(context.Background(), d, id)
	if err != nil || len(reports) != 1 || reports[0].TraceParent != deliverParent {
		t.Fatalf("reports = %+v, %v; want one with traceparent %q", reports, err, deliverParent)
	}
}
