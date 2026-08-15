package entflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// ErrNoResultStore is returned when Result[T] is called on a context that
// carries no per-execution result store at all — the executor never derived
// one via withResults.
var ErrNoResultStore = errors.New("entflow: no result store in context")

// ErrUnknownStep is returned when Result[T] is asked for a step name that has
// not (yet) recorded a value in the current execution.
var ErrUnknownStep = errors.New("entflow: unknown step")

// ErrResultTypeMismatch is returned when Result[T] is asked for a step whose
// recorded value does not decode into the requested type T.
var ErrResultTypeMismatch = errors.New("entflow: result type mismatch")

// resultsKey is the single, unexported context key type under which a
// *resultStore is stored — exactly one value is attached to the context per
// execution; the typed, per-step lookups happen inside the store itself.
type resultsKey struct{}

// resultStore holds every step's recorded return value for a single
// execution, keyed by step name, as JSON — never a live Go value. This is
// D-40: a step's result round-trips through JSON on EVERY path, including
// within a single uninterrupted worker pass, so a flow has exactly one
// behavior whether or not it crashed between claims. If the uninterrupted
// path returned a live value while the post-crash path returned a
// rehydrated one, every flow would have two behaviors and the crash harness
// would prove the wrong one.
type resultStore struct {
	mu      sync.Mutex
	results map[string]json.RawMessage
}

// withResults derives a child context carrying a fresh, empty *resultStore.
// It is called once per Exec/RunInTx invocation, and once per ExecStep call
// (via withResultsFrom) — a store is never shared across executions.
func withResults(ctx context.Context) (context.Context, *resultStore) {
	rs := &resultStore{results: map[string]json.RawMessage{}}
	return context.WithValue(ctx, resultsKey{}, rs), rs
}

// withResultsFrom derives a child context carrying a *resultStore
// pre-populated from results — a run row's persisted results column,
// already JSON. This is the hydration worker/dbstep.go performs at the
// start of every claim, via Runner.ExecStep, before the step closure runs:
// a step calling Result[T] for a prior step reads exactly this persisted
// JSON, whether or not the run crashed between claims (D-40). The values in
// results are stored as-is, with no re-marshal — they are already the exact
// bytes a previous claim's putResult produced (or, for the very first
// claim, an empty map).
func withResultsFrom(ctx context.Context, results map[string]json.RawMessage) context.Context {
	ctx, rs := withResults(ctx)
	for step, raw := range results {
		rs.results[step] = raw
	}
	return ctx
}

// marshalResult marshals v to JSON. v may be a true nil interface or a typed
// nil pointer (a step legitimately returning no entity) — both marshal to
// the JSON literal null via encoding/json's own behavior, so putResult never
// needs to special-case nil separately from any other value.
func marshalResult(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("entflow: marshaling result: %w", err)
	}
	return b, nil
}

// putResult marshals v to JSON (D-40 — every path round-trips through JSON,
// with no live-value shortcut) and records the bytes into rs under step.
// Called by the executor after a step completes successfully.
func putResult(rs *resultStore, step string, v any) error {
	raw, err := marshalResult(v)
	if err != nil {
		return fmt.Errorf("entflow: recording result for step %q: %w", step, err)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.results[step] = raw
	return nil
}

// Result retrieves the typed return value of a previously-executed step
// named step, from the result store carried on ctx, decoding the stored
// JSON bytes into T. It never panics: a missing store yields
// ErrNoResultStore, an unrecorded step name (never executed, or skipped by
// a false condition) yields ErrUnknownStep naming the step, and bytes that
// do not decode into T yield ErrResultTypeMismatch naming the step and the
// wanted type, wrapping the underlying decode error.
//
// A value obtained from Result[T] is inert data, not a live ent entity
// (contrast Self[T], which is live, and a SelfWas condition, which is a
// snapshot frozen at flow entry — three different guarantees, none
// accidental): unexported fields, the ent configuration handle, and loaded
// edge state do not survive the round trip through JSON. This is D-40's
// consequence, not a caveat — every path, including a single uninterrupted
// worker pass, produces this exact same inert value.
func Result[T any](ctx context.Context, step string) (T, error) {
	var zero T
	rs, ok := ctx.Value(resultsKey{}).(*resultStore)
	if !ok {
		return zero, ErrNoResultStore
	}
	rs.mu.Lock()
	raw, ok := rs.results[step]
	rs.mu.Unlock()
	if !ok {
		return zero, fmt.Errorf("%w: %q", ErrUnknownStep, step)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return zero, fmt.Errorf("%w: step %q: decoding into %s: %w", ErrResultTypeMismatch, step, reflect.TypeFor[T](), err)
	}
	return v, nil
}
