package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestSequenceTracker(t *testing.T) {
	st := &SequenceTracker{}
	if st.Current() != 0 {
		t.Fatalf("expected initial seq 0, got %d", st.Current())
	}

	s1 := st.Next()
	if s1 != 1 {
		t.Fatalf("expected seq 1, got %d", s1)
	}

	s2 := st.Next()
	if s2 != 2 {
		t.Fatalf("expected seq 2, got %d", s2)
	}

	if st.Current() != 2 {
		t.Fatalf("expected current seq 2, got %d", st.Current())
	}
}

func TestWorkloadSpans(t *testing.T) {
	ctx := context.Background()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
	)
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(ctx)

	tracer := tp.Tracer(InstrumentationName)
	st := &SequenceTracker{}

	// Lifecycle Spans
	// 1. Admission
	seq := st.Next()
	ctx, span1 := StartWorkloadSpan(ctx, tracer, "AdmissionCycle", "uid-1234", "Admitted", seq,
		AttributeQueueName.String("team-alpha"),
	)
	span1.End()

	// 2. Scheduling
	seq = st.Next()
	ctx, span2 := StartWorkloadSpan(ctx, tracer, "SchedulingCycle", "uid-1234", "Scheduling", seq,
		AttributeNodeName.String("worker-gpu-1"),
	)
	span2.End()

	// 3. Permit
	seq = st.Next()
	_, span3 := StartWorkloadSpan(ctx, tracer, "PermitWait", "uid-1234", "Running", seq)
	span3.End()

	spans := exp.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("expected 3 recorded spans, got %d", len(spans))
	}

	// Verify sequence ordering and attributes
	expectedNames := []string{"AdmissionCycle", "SchedulingCycle", "PermitWait"}
	expectedSeqs := []int64{1, 2, 3}

	for i, sp := range spans {
		if sp.Name != expectedNames[i] {
			t.Errorf("span %d: expected name %s, got %s", i, expectedNames[i], sp.Name)
		}

		var foundUID, foundSeq bool
		for _, attr := range sp.Attributes {
			if attr.Key == AttributeWorkloadUID && attr.Value.AsString() == "uid-1234" {
				foundUID = true
			}
			if attr.Key == AttributeEventSeq && attr.Value.AsInt64() == expectedSeqs[i] {
				foundSeq = true
			}
		}
		if !foundUID {
			t.Errorf("span %d missing workload UID attribute", i)
		}
		if !foundSeq {
			t.Errorf("span %d missing expected seq %d attribute", i, expectedSeqs[i])
		}
	}
}

func TestWorkloadSpanAttributes(t *testing.T) {
	attrs := WorkloadSpanAttributes("uid-xyz", "Queued", 42)
	var foundPhase, foundSeq bool
	for _, a := range attrs {
		if a.Key == AttributeWorkloadPhase && a.Value.AsString() == "Queued" {
			foundPhase = true
		}
		if a.Key == AttributeEventSeq && a.Value.AsInt64() == 42 {
			foundSeq = true
		}
	}
	if !foundPhase || !foundSeq {
		t.Fatalf("expected phase and seq attributes in WorkloadSpanAttributes, got %+v", attrs)
	}
}
