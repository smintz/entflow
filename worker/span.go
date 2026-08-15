// Package worker's span.go declares D-58's span naming/attribute convention
// and D-59's trace-context persistence-across-the-resume-boundary mechanism
// in one place, so no call site inside worker/dbstep.go can drift from
// either.
//
// This file is also where D-57's dependency exception is spent: it imports
// exactly go.opentelemetry.io/otel/trace (plus its dependency-free
// attribute/codes/trace/noop sub-packages) and nothing else — never the
// root go.opentelemetry.io/otel convenience package, and never the ambient
// global tracer (otel.GetTracerProvider()). deps_test.go's
// TestTracingDependencyNarrowness makes that boundary a fact, not a
// convention.
package worker

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Attribute keys carried on every span D-58 requires, under the
// entflow.-prefixed namespace. Identifiers live here, never interpolated
// into a span name — see SpanName's own doc comment for why that
// distinction is load-bearing, not stylistic (T-02-16: span-name
// cardinality explodes in every tracing backend the moment an identifier
// joins the name).
const (
	AttrRunID   = attribute.Key("entflow.run_id")
	AttrFlow    = attribute.Key("entflow.flow")
	AttrStep    = attribute.Key("entflow.step")
	AttrAttempt = attribute.Key("entflow.attempt")
	AttrState   = attribute.Key("entflow.state")
)

// SpanName returns the canonical name for a step's child span (D-58): the
// literal prefix "workflow." followed by flow, a dot, and step — nothing
// else. This is the single place that produces a step span's name; no other
// call site in this module constructs one directly.
func SpanName(flow, step string) string {
	return "workflow." + flow + "." + step
}

// RootSpanName returns the canonical name for a run's root span: the
// literal prefix "workflow." followed by flow, with no step segment and no
// identifier — a run's root span represents the run as a whole, not any one
// step.
func RootSpanName(flow string) string {
	return "workflow." + flow
}

// StepAttributes returns the D-58 attribute set a step's child span
// carries: run ID, flow, step, attempt, and state, under the entflow.
// namespace. runID is rendered with fmt.Sprint since entflow core never
// knows the application's generated ID type (int, uuid.UUID, ...).
//
// PROHIBITION (OPS-05, T-02-03): a run's serialized input and its persisted
// step results must never be rendered into a span attribute, a span event,
// or a log line — the run entity's Policy() governs who may read the row,
// and a trace backend is entirely outside that boundary. No field here, and
// no future addition to this function, may carry either.
func StepAttributes(runID any, flow, step string, attempt int, state string) []attribute.KeyValue {
	return []attribute.KeyValue{
		AttrRunID.String(fmt.Sprint(runID)),
		AttrFlow.String(flow),
		AttrStep.String(step),
		AttrAttempt.Int(attempt),
		AttrState.String(state),
	}
}

// RunAttributes returns the attribute set a run's root span carries at
// start: run ID and flow. Carries the same PROHIBITION StepAttributes does.
func RunAttributes(runID any, flow string) []attribute.KeyValue {
	return []attribute.KeyValue{
		AttrRunID.String(fmt.Sprint(runID)),
		AttrFlow.String(flow),
	}
}

// DefaultTracerProvider returns the zero-cost no-op TracerProvider
// worker.Options.TracerProvider defaults to when unset (D-57/D-58): an
// application that wires nothing pays nothing and pulls nothing. Built from
// the trace module's own noop sub-package — never from the ambient global
// otel.GetTracerProvider(), which would require importing the root
// go.opentelemetry.io/otel package and, with it, five additional external
// modules (logging facade, metrics API, propagation, auto-instrumentation
// SDK) that RESEARCH.md's "Load-Bearing Correction to D-57" measured and
// this plan deliberately declines.
func DefaultTracerProvider() trace.TracerProvider {
	return noop.NewTracerProvider()
}

// ResolveTracerProvider returns tp if non-nil, else DefaultTracerProvider().
// The single place worker/dbstep.go resolves Options.TracerProvider, so an
// application that passes nil pays exactly DefaultTracerProvider's cost and
// nothing more.
func ResolveTracerProvider(tp trace.TracerProvider) trace.TracerProvider {
	if tp == nil {
		return DefaultTracerProvider()
	}
	return tp
}

// traceContextSep separates the hex-encoded trace and span identifiers in
// the string persisted to a run row's trace_context column (D-59).
const traceContextSep = "-"

// EncodeTraceContext renders sc's trace and span identifiers into the
// delimited hex string persisted to a run row's trace_context column:
// "<32-hex-trace-id>-<16-hex-span-id>". Uses the trace package's own hex
// rendering (TraceID.String/SpanID.String) exclusively — no dependency on
// the propagation package, keeping D-57's exception to exactly the trace
// API's own closure (see RESEARCH.md Pattern 5). An invalid span context
// (the zero value, or one built from a run claimed before this feature
// existed) encodes to the empty string.
func EncodeTraceContext(sc trace.SpanContext) string {
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String() + traceContextSep + sc.SpanID().String()
}

// DecodeTraceContext parses a value stored in a run row's trace_context
// column (as produced by EncodeTraceContext) back into a trace.SpanContext
// marked remote. ok is false for an empty or malformed stored value — "no
// parent" — never an error: a run written before this feature existed, or
// whose column is otherwise corrupted, must still execute rather than have
// its claim fail. This function deliberately has no error return, mirroring
// codec.go's discipline the other direction: with no error value to
// construct, no call site is ever tempted to render the malformed stored
// string into a diagnostic.
func DecodeTraceContext(stored string) (sc trace.SpanContext, ok bool) {
	if stored == "" {
		return trace.SpanContext{}, false
	}
	parts := strings.SplitN(stored, traceContextSep, 2)
	if len(parts) != 2 {
		return trace.SpanContext{}, false
	}
	traceID, err := trace.TraceIDFromHex(parts[0])
	if err != nil {
		return trace.SpanContext{}, false
	}
	spanID, err := trace.SpanIDFromHex(parts[1])
	if err != nil {
		return trace.SpanContext{}, false
	}
	restored := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
		Remote:  true,
	})
	if !restored.IsValid() {
		return trace.SpanContext{}, false
	}
	return restored, true
}

// WithRestoredParent decodes stored (as produced by EncodeTraceContext) and,
// if valid, attaches it to ctx as a remote parent span context (D-59) — so a
// span later started from the returned context becomes a child of the
// original run's root trace even though a different process, and a
// different claim transaction, is starting it. An empty or malformed stored
// value returns ctx unchanged: the new span becomes its own trace root
// rather than failing the claim.
func WithRestoredParent(ctx context.Context, stored string) context.Context {
	sc, ok := DecodeTraceContext(stored)
	if !ok {
		return ctx
	}
	return trace.ContextWithRemoteSpanContext(ctx, sc)
}
