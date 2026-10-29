// Package tracing exports the SMSC's traces over OTLP, and carries a trace context across the messages that the SMSC
// stores and delivers later.
package tracing

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const ServiceName = "ella-smsc"

// MessagingSystem is the messaging.system of the spans that process a stored message.
const MessagingSystem = "ella_smsc"

// Propagator is the only trace context the SMSC reads and writes: W3C Trace Context, without baggage.
var Propagator = propagation.TraceContext{}

// NewProvider returns a provider that exports to an OTLP/gRPC collector at endpoint, a host:port. The OTEL_*
// environment variables of the SDK, such as OTEL_RESOURCE_ATTRIBUTES or OTEL_TRACES_SAMPLER, apply.
func NewProvider(ctx context.Context, endpoint, version string, logger *slog.Logger) (*sdktrace.TracerProvider, error) {
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(semconv.ServiceName(ServiceName), semconv.ServiceVersion(version)),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Warn("OpenTelemetry error", slog.Any("error", err))
	}))

	return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res)), nil
}

// TraceParent returns the W3C traceparent of the span in ctx, to store with a message that ctx creates, or "" when
// ctx has no valid span.
func TraceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	Propagator.Inject(ctx, carrier)

	return carrier.Get("traceparent")
}

// Links returns a link to the span of a stored traceparent, for a span that processes the message stored with it, or
// none when traceparent is empty or invalid.
func Links(traceparent string) []trace.Link {
	if traceparent == "" {
		return nil
	}

	sc := trace.SpanContextFromContext(Propagator.Extract(context.Background(), propagation.MapCarrier{"traceparent": traceparent}))
	if !sc.IsValid() {
		return nil
	}

	return []trace.Link{{SpanContext: sc}}
}
