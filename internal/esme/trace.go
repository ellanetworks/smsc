package esme

import (
	"context"

	"github.com/ellanetworks/smsc/smpp"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	tracerName = "github.com/ellanetworks/smsc/internal/esme"
	rpcSystem  = "smpp"
)

func (s *Service) tracer() trace.Tracer {
	tp := s.TracerProvider
	if tp == nil {
		tp = noop.NewTracerProvider()
	}

	return tp.Tracer(tracerName, trace.WithSchemaURL(semconv.SchemaURL))
}

// startSpan starts the span of an SMPP operation with the ESME of a session: a server span for a request of the ESME,
// a client span for one of the SMSC. Each starts a trace, since an ESME sends no trace context.
func (s *Service) startSpan(ctx context.Context, kind trace.SpanKind, id smpp.CommandID, sess *Session,
	links ...trace.Link,
) (context.Context, trace.Span) {
	method := id.String()

	return s.tracer().Start(ctx, method,
		trace.WithNewRoot(),
		trace.WithSpanKind(kind),
		trace.WithLinks(links...),
		trace.WithAttributes(
			semconv.RPCSystemNameKey.String(rpcSystem),
			semconv.RPCMethod(method),
			semconv.NetworkPeerAddress(sess.Remote.Addr().String()),
			semconv.NetworkPeerPort(int(sess.Remote.Port())),
			semconv.NetworkTransportTCP,
		),
	)
}

// endSpan ends the span of an SMPP operation with the command_status of its response, if any. The operation failed
// when failure is set, or when err, which is then the cause.
func endSpan(span trace.Span, status *smpp.CommandStatus, failure string, err error) {
	defer span.End()

	if status != nil {
		span.SetAttributes(semconv.RPCResponseStatusCode(status.String()))
	}

	switch {
	case err != nil:
		span.SetAttributes(semconv.ErrorTypeKey.String(failure))
		span.SetStatus(codes.Error, err.Error())
	case failure != "":
		span.SetAttributes(semconv.ErrorTypeKey.String(failure))
		span.SetStatus(codes.Error, "")
	}
}
