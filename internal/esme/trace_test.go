package esme

import (
	"context"
	"errors"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/smpp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestDeliverSMSpans checks that each deliver_sm has a client span, in a trace of its own that links to the
// submit_sm of its message, and that only an acknowledged one is no error.
func TestDeliverSMSpans(t *testing.T) {
	tests := []struct {
		name      string
		status    smpp.CommandStatus
		err       error
		code      string
		errorType string
	}{
		{"acknowledged", smpp.StatusOK, nil, "ESME_ROK", ""},
		{"rejected", smpp.StatusXPAppn, nil, "ESME_RX_P_APPN", "ESME_RX_P_APPN"},
		{"no response", 0, smpp.ErrResponseTimeout, "", "timeout"},
		{"closed", 0, smpp.ErrClosed, "", "session_closed"},
		{"canceled", 0, context.Canceled, "", "canceled"},
		{"vendor status", 0x400, nil, "vendor(0x00000400)", "_OTHER"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, nil)
			rec := tracetest.NewSpanRecorder()
			env.service.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
			c := env.run(t)

			conn := newFakeConn()
			conn.answer = func(*smpp.DeliverSM) (smpp.CommandStatus, error) { return tc.status, tc.err }
			remote := netip.MustParseAddrPort("192.0.2.7:40000")
			env.service.attach(&Session{ID: 1, ESME: env.esme, UserID: env.esme.UserID, BindType: Receiver, Remote: remote}, conn)

			submit, submitSpan := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "submit_sm")
			defer submitSpan.End()

			submitTrace := submitSpan.SpanContext().TraceID()

			id, status := env.service.SubmitSM(submit, env.session(Transceiver), &smpp.SubmitSM{
				Message: with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.RegisteredDelivery = 0x01 }),
			})
			if status != smpp.StatusOK {
				t.Fatalf("submit_sm = %s", status)
			}

			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				t.Fatal(err)
			}

			env.complete(t, n, db.StatusDelivered, 0, "", "", c.Now())
			conn.next(t)

			var spans []sdktrace.ReadOnlySpan

			for deadline := time.Now().Add(5 * time.Second); len(spans) == 0 && time.Now().Before(deadline); {
				spans = rec.Ended()

				time.Sleep(10 * time.Millisecond)
			}

			if len(spans) != 1 {
				t.Fatalf("ended %d spans, want 1", len(spans))
			}

			s := spans[0]
			if s.Name() != "deliver_sm" || s.SpanKind() != trace.SpanKindClient || s.Parent().IsValid() {
				t.Fatalf("span %q kind %v parent %v, want a deliver_sm client span without a parent", s.Name(), s.SpanKind(), s.Parent())
			}

			if links := s.Links(); len(links) != 1 || links[0].SpanContext.TraceID() != submitTrace {
				t.Errorf("links = %+v, want one to the submit_sm", links)
			}

			attrs := attribute.NewSet(s.Attributes()...)
			for key, want := range map[attribute.Key]any{
				"rpc.system.name":          "smpp",
				"rpc.method":               "deliver_sm",
				"network.peer.address":     "192.0.2.7",
				"network.peer.port":        int64(40000),
				"network.transport":        "tcp",
				"rpc.response.status_code": tc.code,
				"error.type":               tc.errorType,
			} {
				v, ok := attrs.Value(key)
				if got := v.AsInterface(); (want != "" || ok) && got != want {
					t.Errorf("%s = %v, want %v", key, got, want)
				}
			}

			if failed := s.Status().Code == codes.Error; failed != (tc.errorType != "") {
				t.Errorf("status %v, want failed %v", s.Status(), tc.errorType != "")
			}
		})
	}
}

type panickingStore struct{}

func (panickingStore) CreateMessages(context.Context, []db.NewMessage) ([]int64, error) {
	panic("store bug")
}

// TestSubmitSMSpans checks that a submit_sm's span ends once its response is written, and that only a failure of the
// SMSC is an error, a panic included, which the session answers without writing through the Response.
func TestSubmitSMSpans(t *testing.T) {
	tests := []struct {
		name      string
		submit    smpp.Message
		store     bool
		writeErr  error
		code      string
		errorType string
	}{
		{"stored", baseSubmit(0x04, "aabb"), true, nil, "ESME_ROK", ""},
		{"refused", with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.Destination.Addr = "x" }), true, nil, "ESME_RINVDSTADR", ""},
		{"not written", baseSubmit(0x04, "aabb"), true, errors.New("broken pipe"), "ESME_ROK", "_OTHER"},
		{"panic", baseSubmit(0x04, "aabb"), false, nil, "ESME_RSYSERR", "ESME_RSYSERR"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, nil)
			rec := tracetest.NewSpanRecorder()
			env.service.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

			if !tc.store {
				env.service.Submitter.Store = panickingStore{}
			}

			sess := env.session(Transceiver)
			sess.Remote = netip.MustParseAddrPort("192.0.2.7:40000")

			func() {
				defer func() {
					if v := recover(); (v != nil) == tc.store {
						t.Fatalf("panic = %v", v)
					}
				}()

				resp := env.service.serveSubmit(context.Background(), sess, &smpp.SubmitSM{Message: tc.submit}, testNow)

				if len(rec.Ended()) != 0 {
					t.Fatal("the span ended before the response was written")
				}

				resp.Written(tc.writeErr)
			}()

			spans := rec.Ended()
			if len(spans) != 1 {
				t.Fatalf("ended %d spans, want 1", len(spans))
			}

			s := spans[0]
			if s.Name() != "submit_sm" || s.SpanKind() != trace.SpanKindServer || s.Parent().IsValid() {
				t.Fatalf("span %q kind %v parent %v, want a submit_sm server span without a parent", s.Name(), s.SpanKind(), s.Parent())
			}

			attrs := attribute.NewSet(s.Attributes()...)
			for key, want := range map[attribute.Key]any{
				"rpc.system.name":          "smpp",
				"rpc.method":               "submit_sm",
				"network.peer.address":     "192.0.2.7",
				"rpc.response.status_code": tc.code,
				"error.type":               tc.errorType,
			} {
				v, ok := attrs.Value(key)
				if got := v.AsInterface(); (want != "" || ok) && got != want {
					t.Errorf("%s = %v, want %v", key, got, want)
				}
			}

			if failed := s.Status().Code == codes.Error; failed != (tc.errorType != "") {
				t.Errorf("status %v, want failed %v", s.Status(), tc.errorType != "")
			}
		})
	}
}
