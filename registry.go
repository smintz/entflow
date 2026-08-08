package entflow

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// ErrNoRegistry is returned (and, from Use, panicked with) when a context
// carries no *Registry at all — the executor never called WithRegistry. It is
// distinct from ErrNotProvided so a caller can tell "nothing was wired up" apart
// from "a specific handle was forgotten".
var ErrNoRegistry = errors.New("entflow: no registry in context")

// ErrNotProvided is returned (and, from Use, panicked with) when a context
// carries a *Registry but no value has been Provide'd for the requested type.
var ErrNotProvided = errors.New("entflow: type not provided")

// registryKey is the single, unexported context key type under which a
// *Registry is stored. Exactly one value is attached to the context for the
// entire registry — the typed lookups for individual provided values happen
// inside the Registry itself, keyed by reflect.Type, never as one context
// value per concrete type (D-15).
type registryKey struct{}

// Registry is a scoped dependency-injection container: entflow.NewRegistry()
// constructs an independent instance, entflow.Provide registers a value on
// it, and entflow.WithRegistry injects it into a context.Context for step
// bodies to read via entflow.Use / entflow.TryUse. There is deliberately no
// package-level global registry (D-15) — parallel tests need independent
// mock sets, and a process-global registry would muddy Phase 2's
// multi-worker story.
type Registry struct {
	mu    sync.RWMutex
	items map[reflect.Type]any
}

// NewRegistry constructs an empty, independent Registry.
func NewRegistry() *Registry {
	return &Registry{items: map[reflect.Type]any{}}
}

// Provide registers v under the registry's key for type T. Keying on the type
// parameter T — via reflect.TypeFor[T](), not reflect.TypeOf(v) — is what
// makes interface-typed registration work: Provide[Refunder](reg, concrete)
// registers under the Refunder interface type, so it is retrievable via
// Use[Refunder] even though the concrete value's dynamic type differs.
func Provide[T any](r *Registry, v T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[reflect.TypeFor[T]()] = v
}

// WithRegistry returns a context carrying r, for step bodies executing inside
// it to read via Use / TryUse.
func WithRegistry(ctx context.Context, r *Registry) context.Context {
	return context.WithValue(ctx, registryKey{}, r)
}

// TryUse retrieves the value of type T provided on the registry carried by
// ctx. It never panics: a missing registry yields ErrNoRegistry, and a
// registry with no value provided for T yields ErrNotProvided naming the
// missing type.
func TryUse[T any](ctx context.Context) (T, error) {
	var zero T
	r, ok := ctx.Value(registryKey{}).(*Registry)
	if !ok {
		return zero, ErrNoRegistry
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.items[reflect.TypeFor[T]()]
	if !ok {
		return zero, fmt.Errorf("%w: %s", ErrNotProvided, reflect.TypeFor[T]())
	}
	return v.(T), nil
}

// Use retrieves the value of type T provided on the registry carried by ctx,
// panicking with the error value TryUse would have returned if none is
// available. This matches the design document's own example ergonomics
// (entflow.Use[*stripe.Client](ctx).Refunds.Create(...)).
//
// The panic value is always an error (never a bare string), which is
// load-bearing for D-16: Plan 03's executor recovers panics at every step
// boundary and, when the recovered value is already an error, threads it
// through as the %w-wrapped cause of a *StepError — so
// errors.Is(err, entflow.ErrNotProvided) still resolves after the panic has
// been converted to a returned error.
func Use[T any](ctx context.Context) T {
	v, err := TryUse[T](ctx)
	if err != nil {
		panic(err)
	}
	return v
}
