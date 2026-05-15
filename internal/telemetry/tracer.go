// Package telemetry handles the tracing for the project. Uses OpenTelemetry.
package telemetry

import (
	"context"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.26.0"
)

// InitTracer creates the tracer that allows us to instrument our code and send to the exporter.
func InitTracer(ctx context.Context) func(context.Context) error {
	// This exports the final spans to the visualation backend
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint("localhost:4317"),
		otlptracegrpc.WithInsecure(),
	)
    if err != nil {
        log.Fatalf("failed to create exporter: %v", err)
    }

	// The main hub that creates spans and sends them to the exporter
	tp := trace.NewTracerProvider(
		trace.WithBatcher(exporter),
		trace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String("load-balancer"),
		)),
	)

	// Tell OpenTelemetry this is the provider to use
	otel.SetTracerProvider(tp)

	// Flush any spans still in the batcher
	return tp.Shutdown
}
