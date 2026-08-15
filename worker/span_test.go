package worker_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/smintz/entflow/worker"
)

// validSpanContext builds a valid, remote-marked trace.SpanContext from
// fixed, known-good hex identifiers — a fixture the round-trip and
// restore-as-parent tests share.
func validSpanContext(t *testing.T) trace.SpanContext {
	t.Helper()
	traceID, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0102030405060708")
	require.NoError(t, err)
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	})
}

// TestTraceContextRoundTrip proves a span context encodes and decodes to an
// equal value with identical trace and span identifiers (D-59).
func TestTraceContextRoundTrip(t *testing.T) {
	sc := validSpanContext(t)
	stored := worker.EncodeTraceContext(sc)
	require.NotEmpty(t, stored)

	decoded, ok := worker.DecodeTraceContext(stored)
	require.True(t, ok)
	require.Equal(t, sc.TraceID(), decoded.TraceID())
	require.Equal(t, sc.SpanID(), decoded.SpanID())
	require.True(t, decoded.IsRemote(), "a decoded trace context must be marked remote")
}

// TestTraceContextEmptyDecodesToNoParent proves an empty stored value
// decodes to "no parent" with no error, so a run written before this
// feature existed still executes.
func TestTraceContextEmptyDecodesToNoParent(t *testing.T) {
	decoded, ok := worker.DecodeTraceContext("")
	require.False(t, ok)
	require.False(t, decoded.IsValid())
}

// TestTraceContextMalformedDecodesToNoParent proves a malformed stored
// value decodes to "no parent" — never an error — and that DecodeTraceContext
// carries no diagnostic that could echo the malformed input (the function
// has no error return at all, by construction).
func TestTraceContextMalformedDecodesToNoParent(t *testing.T) {
	malformed := "not-a-real-trace-context;;;garbage=='"
	decoded, ok := worker.DecodeTraceContext(malformed)
	require.False(t, ok)
	require.False(t, decoded.IsValid())
}

// TestWithRestoredParentBecomesParent proves a decoded, remote-marked trace
// context becomes the parent of a span started from the returned context —
// exercised against the same no-op TracerProvider worker.Options defaults
// to (DefaultTracerProvider), so the assertion holds even under the
// zero-cost default an application that wires nothing pays.
func TestWithRestoredParentBecomesParent(t *testing.T) {
	sc := validSpanContext(t)
	stored := worker.EncodeTraceContext(sc)

	ctx := worker.WithRestoredParent(context.Background(), stored)

	tracer := worker.DefaultTracerProvider().Tracer("test")
	_, span := tracer.Start(ctx, worker.SpanName("CancelOrder", "cancel"))
	defer span.End()

	require.Equal(t, sc.TraceID(), span.SpanContext().TraceID(),
		"a span started from a restored parent context must share the parent's trace identifier")
}

// TestSpanNameHelper proves SpanName produces exactly the expected string
// for the fixture flow's DB step, with no identifier interpolated into it.
func TestSpanNameHelper(t *testing.T) {
	require.Equal(t, "workflow.CancelOrder.cancel", worker.SpanName("CancelOrder", "cancel"))
}

// TestRootSpanNameHelper proves RootSpanName produces the flow-only prefix,
// with no step segment.
func TestRootSpanNameHelper(t *testing.T) {
	require.Equal(t, "workflow.CancelOrder", worker.RootSpanName("CancelOrder"))
}

// TestResolveTracerProviderDefaultsToNoop proves a nil TracerProvider
// resolves to the zero-cost no-op default rather than panicking or
// returning nil.
func TestResolveTracerProviderDefaultsToNoop(t *testing.T) {
	tp := worker.ResolveTracerProvider(nil)
	require.NotNil(t, tp)
	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "workflow.Test.step")
	defer span.End()
	require.False(t, span.IsRecording(), "the default provider must be the no-op provider")
}
