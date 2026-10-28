/*
Package tracing implements OpenTelemetry distributed tracing for the Meridian GPU
fleet control plane.

Spans link the complete workload lifecycle:
  admission → gate release → scheduling cycle → permit wait → bind → container start

All events and spans are keyed by the GPUWorkload UID and carry a monotonically
increasing sequence number (`seq`) so that causal ordering is unambiguous across
distributed components and asynchronous controller events.
*/
package tracing

import (
	"context"
	"fmt"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	// InstrumentationName is the OpenTelemetry tracer identifier for Meridian.
	InstrumentationName = "github.com/tijani-web/meridian"

	// AttributeWorkloadUID identifies the GPUWorkload UID across all trace spans.
	AttributeWorkloadUID = attribute.Key("meridian.workload.uid")
	// AttributeWorkloadPhase identifies the target phase of the workload transition.
	AttributeWorkloadPhase = attribute.Key("meridian.workload.phase")
	// AttributeEventSeq identifies the monotonically increasing sequence number.
	AttributeEventSeq = attribute.Key("meridian.event.seq")
	// AttributeQueueName identifies the queue associated with the workload.
	AttributeQueueName = attribute.Key("meridian.queue.name")
	// AttributeNodeName identifies the assigned or evaluated node name.
	AttributeNodeName = attribute.Key("meridian.node.name")
)

// Config configures the TracerProvider.
type Config struct {
	ServiceName string
	Environment string
	Endpoint    string // Optional OTLP gRPC endpoint (e.g., localhost:4317)
}

// SequenceTracker tracks monotonic event numbers for a workload.
type SequenceTracker struct {
	current atomic.Int64
}

// Next increments and returns the next monotonic sequence number.
func (st *SequenceTracker) Next() int64 {
	return st.current.Add(1)
}

// Current returns the latest issued sequence number.
func (st *SequenceTracker) Current() int64 {
	return st.current.Load()
}

// InitTracer initializes a standard TracerProvider. If endpoint is empty or in test mode,
// it uses an in-memory or stdout exporter.
func InitTracer(ctx context.Context, cfg Config) (*sdktrace.TracerProvider, error) {
	if cfg.ServiceName == "" {
		cfg.ServiceName = "meridian-control-plane"
	}

	// For local test and default environment, initialize a clean stdout/noop exporter
	exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, fmt.Errorf("failed to initialize trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(
			nil,
		),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp, nil
}

// StartWorkloadSpan starts an OpenTelemetry span for a lifecycle phase with sequence attribution.
func StartWorkloadSpan(
	ctx context.Context,
	tracer trace.Tracer,
	spanName string,
	workloadUID string,
	phase string,
	seq int64,
	attrs ...attribute.KeyValue,
) (context.Context, trace.Span) {
	baseAttrs := []attribute.KeyValue{
		AttributeWorkloadUID.String(workloadUID),
		AttributeWorkloadPhase.String(phase),
		AttributeEventSeq.Int64(seq),
	}
	baseAttrs = append(baseAttrs, attrs...)

	return tracer.Start(ctx, spanName, trace.WithAttributes(baseAttrs...))
}

// WorkloadSpanAttributes generates standard attributes for structured logging or events.
func WorkloadSpanAttributes(workloadUID, phase string, seq int64) []attribute.KeyValue {
	return []attribute.KeyValue{
		AttributeWorkloadUID.String(workloadUID),
		AttributeWorkloadPhase.String(phase),
		AttributeEventSeq.Int64(seq),
		semconv.ServiceNameKey.String("meridian"),
	}
}
