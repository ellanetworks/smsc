package delivery

import (
	"context"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/tgpp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const (
	creationTraceID = "0af7651916cd43dd8448eb211c80319c"
	creationSpanID  = "b7ad6b7169203331"
)

// tracedRouter and tracedSender record the span of each request they get.
type tracedRouter struct {
	*fakeRouter
	spans []trace.SpanContext
}

func (r *tracedRouter) SendRoutingInfoForSM(ctx context.Context, req s6c.RoutingRequest) (s6c.Routing, string, error) {
	r.spans = append(r.spans, trace.SpanContextFromContext(ctx))
	return r.fakeRouter.SendRoutingInfoForSM(ctx, req)
}

type tracedSender struct {
	*fakeSender
	spans []trace.SpanContext
}

func (s *tracedSender) Do(ctx context.Context, peer string, req *diameter.Message) (*diameter.Message, error) {
	s.spans = append(s.spans, trace.SpanContextFromContext(ctx))
	return s.fakeSender.Do(ctx, peer, req)
}

// TestDeliveryAttemptSpans checks that each delivery attempt has a span of its own, in a trace of its own that links
// to the request that created the message, which its Diameter requests are part of, with the outcome of the attempt.
func TestDeliveryAttemptSpans(t *testing.T) {
	permanent := &diameter.Message{AVPs: []diameter.AVP{
		diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, 5004),
	}}

	tests := map[string]struct {
		answer      *diameter.Message
		traceParent string
		outcome     string
		failed      bool
	}{
		"delivered":       {answer: success(), traceParent: "00-" + creationTraceID + "-" + creationSpanID + "-01", outcome: "delivered"},
		"retried":         {answer: experimental(tgpp.ResultErrorAbsentUser), outcome: "retry"},
		"failed":          {answer: permanent, outcome: "failed", failed: true},
		"invalid context": {answer: success(), traceParent: "garbage", outcome: "delivered"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			m := pendingMessage(t)
			m.TraceParent = tc.traceParent

			router := &tracedRouter{fakeRouter: &fakeRouter{routing: mmeRouting(), hss: "hss.example.org"}}
			sender := &tracedSender{fakeSender: &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": tc.answer}}}
			rec := tracetest.NewSpanRecorder()

			d := newDeliverer(newFakeStore(m), router, sender)
			d.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

			process(t, d)

			spans := rec.Ended()
			if len(spans) != 1 {
				t.Fatalf("ended %d spans, want 1", len(spans))
			}

			s := spans[0]
			if s.Name() != "deliver" || s.SpanKind() != trace.SpanKindConsumer || s.Parent().IsValid() {
				t.Fatalf("span %q kind %v parent %v, want a deliver consumer span without a parent", s.Name(), s.SpanKind(), s.Parent())
			}

			attrs := attribute.NewSet(s.Attributes()...)
			for key, want := range map[attribute.Key]string{
				"messaging.system":           "ella_smsc",
				"messaging.destination.name": "smsc.example.org",
				"messaging.operation.name":   "deliver",
				"messaging.operation.type":   "process",
				"messaging.message.id":       "1",
				"ella.sms.delivery.outcome":  tc.outcome,
			} {
				if v, _ := attrs.Value(key); v.AsString() != want {
					t.Errorf("%s = %q, want %q", key, v.AsString(), want)
				}
			}

			errorType, _ := attrs.Value("error.type")
			if failed := s.Status().Code == codes.Error; failed != tc.failed || (tc.failed && errorType.AsString() != tc.outcome) {
				t.Errorf("status %v, error.type %q; want failed %v", s.Status(), errorType.AsString(), tc.failed)
			}

			wantLinks := 0
			if tc.traceParent != "" && tc.traceParent != "garbage" {
				wantLinks = 1
			}

			if links := s.Links(); len(links) != wantLinks ||
				wantLinks == 1 && (links[0].SpanContext.TraceID().String() != creationTraceID || links[0].SpanContext.SpanID().String() != creationSpanID) {
				t.Errorf("links = %+v, want %d to the creation context", links, wantLinks)
			}

			for _, sc := range append(router.spans, sender.spans...) {
				if sc.SpanID() != s.SpanContext().SpanID() {
					t.Errorf("a request is in span %v, want the attempt's %v", sc, s.SpanContext())
				}
			}

			if len(router.spans) == 0 || len(sender.spans) == 0 {
				t.Errorf("%d routing and %d delivery requests, want both", len(router.spans), len(sender.spans))
			}
		})
	}
}

// TestDeliveryAttemptSpansPerMessage checks that the messages of a recipient that one delivery announced, sent in turn
// without another routing lookup, each have a span of their own.
func TestDeliveryAttemptSpansPerMessage(t *testing.T) {
	first, second := pendingMessage(t), pendingMessage(t)
	second.ID = 2
	second.SubmittedAt = second.SubmittedAt.Add(1)

	router := &tracedRouter{fakeRouter: &fakeRouter{routing: mmeRouting(), hss: "hss.example.org"}}
	sender := &tracedSender{fakeSender: &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}}}
	rec := tracetest.NewSpanRecorder()

	d := newDeliverer(newFakeStore(first, second), router, sender)
	d.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

	process(t, d)

	spans := rec.Ended()
	if len(spans) != 2 || len(sender.spans) != 2 {
		t.Fatalf("ended %d spans for %d deliveries, want 2 for 2", len(spans), len(sender.spans))
	}

	for i, s := range spans {
		attrs := attribute.NewSet(s.Attributes()...)
		id, _ := attrs.Value("messaging.message.id")

		if want := []string{"1", "2"}[i]; id.AsString() != want || sender.spans[i].SpanID() != s.SpanContext().SpanID() {
			t.Errorf("span %d is of message %q, want %q, with its delivery request", i, id.AsString(), want)
		}
	}

	if spans[0].SpanContext().TraceID() == spans[1].SpanContext().TraceID() {
		t.Error("the messages share a trace")
	}
}
