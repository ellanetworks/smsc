package server_test

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/linxGnu/gosmpp"
	"github.com/linxGnu/gosmpp/pdu"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func spanNamed(spans []sdktrace.ReadOnlySpan, name string, kind trace.SpanKind) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan

	for _, s := range spans {
		if s.Name() == name && s.SpanKind() == kind {
			out = append(out, s)
		}
	}

	return out
}

// TestMobileOriginatedMessageIsTraced follows a message from an MS through the SMSC and back to the core: the core's
// request carries its trace context, which the MO-Forward-Short-Message span continues, though the core is not a
// configured peer; the delivery attempt starts a trace that links to that span, and its routing lookup and delivery,
// which the core continues in turn, are part of it.
func TestMobileOriginatedMessageIsTraced(t *testing.T) {
	coreSpans, smscSpans := tracetest.NewSpanRecorder(), tracetest.NewSpanRecorder()

	core := newTracedFakeCore(t, coreHost, realm, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(coreSpans)), alice, bob)
	cfg := testConfig(t, filepath.Join(t.TempDir(), "smsc.db"))
	cfg.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(smscSpans))
	s := startSMSC(t, cfg)
	core.connect(s)

	if rc := core.submit(alice, bob, "hello bob"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusDelivered)

	var deliver []sdktrace.ReadOnlySpan

	eventually(t, "the delivery span", func() bool {
		deliver = spanNamed(smscSpans.Ended(), "deliver", trace.SpanKindConsumer)
		return len(deliver) == 1
	})

	mo := spanNamed(coreSpans.Ended(), "SGd/MO-Forward-Short-Message", trace.SpanKindClient)
	received := spanNamed(smscSpans.Ended(), "SGd/MO-Forward-Short-Message", trace.SpanKindServer)

	if len(mo) != 1 || len(received) != 1 {
		t.Fatalf("%d MO-Forward-Short-Message client spans and %d server spans, want 1 and 1", len(mo), len(received))
	}

	if r := received[0]; r.Parent().SpanID() != mo[0].SpanContext().SpanID() || len(r.Links()) != 0 {
		t.Fatalf("MO server span parent %v, links %+v; want a child of the core's span", r.Parent(), r.Links())
	}

	d := deliver[0]
	if d.Parent().IsValid() || len(d.Links()) != 1 || d.Links()[0].SpanContext.SpanID() != received[0].SpanContext().SpanID() {
		t.Fatalf("delivery span parent %v, links %+v; want a link to the MO span", d.Parent(), d.Links())
	}

	attrs := attribute.NewSet(d.Attributes()...)
	if outcome, _ := attrs.Value("ella.sms.delivery.outcome"); outcome.AsString() != "delivered" {
		t.Errorf("delivery outcome = %q", outcome.AsString())
	}

	for _, name := range []string{"S6c/Send-Routing-Info-for-SM", "SGd/MT-Forward-Short-Message"} {
		var children []sdktrace.ReadOnlySpan

		for _, c := range spanNamed(smscSpans.Ended(), name, trace.SpanKindClient) {
			if c.Parent().SpanID() == d.SpanContext().SpanID() {
				children = append(children, c)
			}
		}

		if len(children) != 1 {
			t.Errorf("%d %s spans in the delivery attempt, want 1", len(children), name)
			continue
		}

		served := spanNamed(coreSpans.Ended(), name, trace.SpanKindServer)
		if len(served) != 1 || served[0].Parent().SpanID() != children[0].SpanContext().SpanID() {
			t.Errorf("the core's %s spans %d, want one, a child of the SMSC's request", name, len(served))
		}
	}
}

// TestSMPPMessageIsTraced follows a message from an ESME: the submit_sm has a server span that its delivery links to,
// and so does the receipt's deliver_sm.
func TestSMPPMessageIsTraced(t *testing.T) {
	rec := tracetest.NewSpanRecorder()

	_, s := startSMPPSMSC(t, func(cfg *smscConfig) {
		cfg.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	})

	esme := bindESME(t, s, trx, gosmpp.NonTLSDialer)
	id := submitted(t, esme, otaSubmit(t, 0x01))

	next[*pdu.DeliverSM](t, esme)

	var submit, deliver, receipt []sdktrace.ReadOnlySpan

	eventually(t, "the spans", func() bool {
		spans := rec.Ended()
		submit = spanNamed(spans, "submit_sm", trace.SpanKindServer)
		deliver = spanNamed(spans, "deliver", trace.SpanKindConsumer)
		receipt = spanNamed(spans, "deliver_sm", trace.SpanKindClient)

		return len(submit) == 1 && len(deliver) == 1 && len(receipt) == 1
	})

	attrs := attribute.NewSet(submit[0].Attributes()...)
	for key, want := range map[attribute.Key]string{
		"rpc.system.name":          "smpp",
		"rpc.method":               "submit_sm",
		"rpc.response.status_code": "ESME_ROK",
		"network.peer.address":     "127.0.0.1",
	} {
		if v, _ := attrs.Value(key); v.AsString() != want {
			t.Errorf("submit_sm %s = %q, want %q", key, v.AsString(), want)
		}
	}

	deliverAttrs := attribute.NewSet(deliver[0].Attributes()...)
	if m, _ := deliverAttrs.Value("messaging.message.id"); m.AsString() != strconv.FormatInt(id, 10) {
		t.Errorf("delivery of message %q, want %d", m.AsString(), id)
	}

	for name, s := range map[string]sdktrace.ReadOnlySpan{"delivery": deliver[0], "receipt": receipt[0]} {
		if l := s.Links(); len(l) != 1 || l[0].SpanContext.SpanID() != submit[0].SpanContext().SpanID() {
			t.Errorf("%s links %+v, want one to the submit_sm", name, l)
		}
	}
}
