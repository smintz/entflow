package entflow

import (
	"context"
	"fmt"
	"sync"
)

// registeredFlow pairs a registered Flow with the RunStore that persists its
// runs.
type registeredFlow struct {
	flow  Flow
	store RunStore
}

// Engine binds the flow registry, each flow's RunStore, and the DI registry
// at boot time (D-25). Constructed once via NewEngine and shared by every
// caller of Start and by the worker.
type Engine struct {
	mu    sync.RWMutex
	flows map[string]registeredFlow
	di    *Registry
	nudge chan struct{}
}

// EngineOption configures an Engine at construction time via NewEngine.
type EngineOption func(*Engine)

// WithDI supplies the DI registry (registry.go) activities and DB steps
// resolve dependencies from via entflow.Use[T]. Optional — an Engine
// constructed without one simply has no DI registry to hand to Use[T]
// callers. Named WithDI, not WithRegistry, because entflow.WithRegistry
// already names the unrelated function that injects a *Registry into a
// context.Context (registry.go) — reusing that identifier here for a
// same-package EngineOption would collide with it.
func WithDI(r *Registry) EngineOption {
	return func(e *Engine) {
		e.di = r
	}
}

// NewEngine constructs an Engine with no flows registered.
func NewEngine(opts ...EngineOption) *Engine {
	e := &Engine{
		flows: make(map[string]registeredFlow),
		nudge: make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Register binds f to the store that persists its runs. Rejects a nil flow,
// a flow with no declared name, a nil store, or registering the same flow
// name twice — all programming-time wiring mistakes caught at boot rather
// than the first time a run tries to persist.
func (e *Engine) Register(f Flow, s RunStore) error {
	if f == nil {
		return fmt.Errorf("entflow: Engine.Register: flow must not be nil")
	}
	name := f.Name()
	if name == "" {
		return fmt.Errorf("entflow: Engine.Register: flow declares no name")
	}
	if s == nil {
		return fmt.Errorf("entflow: Engine.Register: flow %q: store must not be nil", name)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.flows[name]; exists {
		return fmt.Errorf("entflow: Engine.Register: flow %q already registered", name)
	}
	e.flows[name] = registeredFlow{flow: f, store: s}
	return nil
}

// RunnerFor returns the Runner for the named flow. false when no flow of
// that name is registered, or (structurally unreachable for any *FlowOf[In])
// when the registered Flow does not implement Runner.
func (e *Engine) RunnerFor(name string) (Runner, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	rf, ok := e.flows[name]
	if !ok {
		return nil, false
	}
	r, ok := rf.flow.(Runner)
	return r, ok
}

// StoreFor returns the RunStore registered for the named flow.
func (e *Engine) StoreFor(name string) (RunStore, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	rf, ok := e.flows[name]
	if !ok {
		return nil, false
	}
	return rf.store, true
}

// Registry returns the DI registry supplied via WithDI, or nil if none was.
// The worker uses this to inject the registry into a step's context (via
// entflow.WithRegistry) before executing its closure, so entflow.Use[T]
// resolves the same way inside a durable run as it does under Exec/RunInTx.
func (e *Engine) Registry() *Registry {
	return e.di
}

// Flows returns every registered Flow. Order is unspecified.
func (e *Engine) Flows() []Flow {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Flow, 0, len(e.flows))
	for _, rf := range e.flows {
		out = append(out, rf.flow)
	}
	return out
}

// Notify wakes a co-located worker's poll loop early via a non-blocking
// send (D-33) — a full nudge channel means a wakeup is already pending, so a
// second Notify is a no-op rather than a blocking one.
func (e *Engine) Notify() {
	select {
	case e.nudge <- struct{}{}:
	default:
	}
}

// Nudges returns the channel a worker selects on alongside its poll ticker.
func (e *Engine) Nudges() <-chan struct{} {
	return e.nudge
}

// Start persists a new run row for f in the pending state and returns its
// hydrated handle. This is D-25's package-level generic function, following
// RunInTx's tx-open/commit/rollback/panic discipline including the separate
// recover site (exec.go) — Go methods cannot declare their own type
// parameters, so Start cannot be a method on Engine.
//
// D-25 deviation, recorded here per the phase summary requirement: DUR-01's
// literal call shape flow.Start(ctx, in) is unreachable in Phase 2 without
// an ambient global, which D-15 already rejected. Phase 6's codegen
// restores the design-document ergonomics as a generated typed facade
// (client.CancelOrder.Start(ctx, in)). This is a documented, deliberate gap,
// not an oversight.
func Start[In any](ctx context.Context, e *Engine, f *FlowOf[In], in In) (*Run, error) {
	store, ok := e.StoreFor(f.Name())
	if !ok {
		return nil, fmt.Errorf("entflow: Start: flow %q: %w", f.Name(), ErrUnknownFlow)
	}

	input, err := f.codecOf().Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("entflow: Start: flow %q: marshaling input: %w", f.Name(), err)
	}

	var ownerRef any
	if f.ownerRef != nil {
		ownerRef, err = f.ownerRef(in)
		if err != nil {
			return nil, fmt.Errorf("entflow: Start: flow %q: resolving owner ref: %w", f.Name(), err)
		}
	}

	txAny, err := store.BeginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("entflow: Start: opening transaction: %w", err)
	}
	tx, ok := txAny.(Tx)
	if !ok {
		return nil, fmt.Errorf("entflow: Start: transaction has type %T, want entflow.Tx", txAny)
	}

	defer func() {
		if r := recover(); r != nil {
			if rerr := tx.Rollback(); rerr != nil {
				panic(fmt.Errorf("entflow: Start: panic: %v (rollback also failed: %w)", r, rerr))
			}
			panic(r)
		}
	}()

	runID, err := store.Insert(ctx, txAny, InsertRun{
		State:    RunStatePending,
		Input:    input,
		OwnerRef: ownerRef,
	})
	if err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			return nil, fmt.Errorf("entflow: Start: inserting run: %v (rollback also failed: %w)", err, rerr)
		}
		return nil, fmt.Errorf("entflow: Start: inserting run: %w", err)
	}

	run, err := store.Load(ctx, txAny, runID)
	if err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			return nil, fmt.Errorf("entflow: Start: loading inserted run: %v (rollback also failed: %w)", err, rerr)
		}
		return nil, fmt.Errorf("entflow: Start: loading inserted run: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("entflow: Start: committing: %w", err)
	}

	e.Notify()
	return run, nil
}
