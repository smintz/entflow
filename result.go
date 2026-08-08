package entflow

import (
	"context"
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
// recorded value does not have the requested type T.
var ErrResultTypeMismatch = errors.New("entflow: result type mismatch")

// resultsKey is the single, unexported context key type under which a
// *resultStore is stored — exactly one value is attached to the context per
// execution; the typed, per-step lookups happen inside the store itself.
type resultsKey struct{}

// resultStore holds every step's recorded return value for a single
// execution, keyed by step name. It is an implementation detail Phase 2
// swaps for the run row with no change to Result[T]'s signature.
type resultStore struct {
	mu      sync.Mutex
	results map[string]any
}

// withResults derives a child context carrying a fresh *resultStore. It is
// called once per Exec/RunInTx invocation — a store is never shared across
// executions.
func withResults(ctx context.Context) (context.Context, *resultStore) {
	rs := &resultStore{results: map[string]any{}}
	return context.WithValue(ctx, resultsKey{}, rs), rs
}

// putResult records step's return value v into rs. Called by the executor
// after a step completes successfully.
func putResult(rs *resultStore, step string, v any) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.results[step] = v
}

// Result retrieves the typed return value of a previously-executed step named
// step, from the result store carried on ctx. It never panics: a missing
// store yields ErrNoResultStore, an unrecorded step name yields
// ErrUnknownStep naming the step, and a stored value whose type does not
// match T yields ErrResultTypeMismatch naming the step, the actual type, and
// the wanted type. Every assertion on this path uses the comma-ok form — a
// bare panicking assertion here would reintroduce exactly the stringly-typed
// decay failure D-11 exists to prevent.
func Result[T any](ctx context.Context, step string) (T, error) {
	var zero T
	rs, ok := ctx.Value(resultsKey{}).(*resultStore)
	if !ok {
		return zero, ErrNoResultStore
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	v, ok := rs.results[step]
	if !ok {
		return zero, fmt.Errorf("%w: %q", ErrUnknownStep, step)
	}
	typed, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("%w: step %q produced %T, want %s", ErrResultTypeMismatch, step, v, reflect.TypeFor[T]())
	}
	return typed, nil
}
