// Package crashpoint is the tier-1 logical crash-point registry (D-52):
// a single, build-tag-free seam the production claim path
// (worker/dbstep.go) calls at every named boundary of the claim-execute-
// advance-commit transaction (D-30). With no hook installed, At is a single
// RLock plus a map lookup returning nil — nothing a production deployment
// has to know about, opt out of, or gate behind a compile-time flag.
//
// A test simulates a crash by installing a hook that returns a non-nil
// error at a named boundary: the error propagates out of the claim path
// exactly like any other claim-time error, rolling the transaction back —
// which is exactly what a real process crash produces, since a crash never
// gets a chance to commit either.
package crashpoint

import (
	"context"
	"fmt"
	"sync"
)

// registry is the package-level hook table. A RWMutex, not an atomic value,
// because Install/Clear (rare, test-only writes) and At (the hot, concurrent
// read every claim goroutine performs) have very different frequency
// profiles — RWMutex lets N claim goroutines call At concurrently without
// contending each other while a test installs or clears a hook.
var registry = struct {
	mu    sync.RWMutex
	hooks map[string]func() error
}{hooks: make(map[string]func() error)}

// At looks up an installed hook for name and, if one exists, invokes it and
// returns its result. With no hook installed for name — the production
// default — At returns nil immediately after one RLock-guarded map lookup:
// no allocation, no compile-time tag, nothing a deployment has to configure. A
// hook returning a non-nil error is how a test simulates a crash at this
// boundary: the caller (worker/dbstep.go's claimOnce) propagates the error
// out of the claim transaction exactly as any other failure would, rolling
// the transaction back — the same outcome a real crash produces, since a
// real crash never reaches the commit either.
func At(_ context.Context, name string) error {
	registry.mu.RLock()
	fn := registry.hooks[name]
	registry.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// Install registers fn to fire the next time (and every subsequent time,
// until uninstalled) At is called with name, and returns an uninstall
// function that removes exactly this hook, restoring At(ctx, name)'s inert,
// nil-returning default behavior. Concurrency-safe: a claim goroutine
// calling At while a test calls Install (or the returned uninstall
// function) observes either the hook fully installed or fully absent, never
// a partial state — proven race-clean by TestConcurrentAtDuringInstall.
func Install(name string, fn func() error) func() {
	registry.mu.Lock()
	registry.hooks[name] = fn
	registry.mu.Unlock()

	return func() {
		registry.mu.Lock()
		delete(registry.hooks, name)
		registry.mu.Unlock()
	}
}

// Clear removes every installed hook, restoring At's inert default
// behavior for every name. Test-only cleanup convenience — production code
// never calls this.
func Clear() {
	registry.mu.Lock()
	registry.hooks = make(map[string]func() error)
	registry.mu.Unlock()
}

// Boundary names one phase within a single claim's claim-execute-advance-
// commit transaction (D-30). Every constant below corresponds to exactly
// one crashpoint.At call site in worker/dbstep.go, named identically via
// Name — a boundary declared here and not called from dbstep.go (or vice
// versa) is a coverage hole TestMatrixNamesReachableInClaimPath catches.
type Boundary string

const (
	// AfterClaim is the instant right after the claim query (SKIP LOCKED)
	// has returned a row's ID — before anything about that row has been
	// read. In worker/dbstep.go this check is fired together with
	// AfterHydrate, immediately after store.Load, because store.Load is a
	// read with no durability consequence of its own: firing both checks
	// at that single point in the code produces an identical observable
	// outcome (a rollback with zero effect and zero progress-pointer
	// change) as firing AfterClaim strictly before the Load call would.
	// See dbstep.go's own comment at that call site for the full
	// reasoning.
	AfterClaim Boundary = "after-claim"
	// AfterHydrate is the instant right after the claimed run row has been
	// loaded (hydrated) from the database, before any step closure runs.
	AfterHydrate Boundary = "after-hydrate"
	// BeforeStep is the instant right before the step closure for a given
	// step is invoked.
	BeforeStep Boundary = "before-step"
	// AfterStep is the instant right after a step's closure has returned
	// successfully, before its result and the run's progress pointer are
	// written.
	AfterStep Boundary = "after-step"
	// AfterAdvance is the instant right after the progress-pointer write
	// (RunStore.Advance) has returned successfully, before the transaction
	// commits.
	AfterAdvance Boundary = "after-advance"
	// BeforeCommit is the instant immediately before the transaction
	// commits — the last possible point at which a crash can still roll
	// the whole claim back.
	BeforeCommit Boundary = "before-commit"
)

// PreClaimName is the one boundary Matrix includes exactly once per flow,
// never per step (per the plan's "include the pre-claim boundary once"):
// the instant immediately before the claim query itself runs, before any
// row has been claimed and therefore before any step is even known.
const PreClaimName = "pre-claim"

// Name renders the step-scoped crash-point name for boundary b at step —
// the same construction both Matrix (enumeration) and worker/dbstep.go
// (instrumentation) use, so the two can never name a boundary differently
// from each other.
func Name(step string, b Boundary) string {
	return step + ":" + string(b)
}

// boundaryOrder is every per-step Boundary, in the fixed order Matrix
// enumerates them for a given step — the literal sequence the claim path
// executes them in.
var boundaryOrder = []Boundary{
	AfterClaim,
	AfterHydrate,
	BeforeStep,
	AfterStep,
	AfterAdvance,
	BeforeCommit,
}

// Matrix enumerates every crash point the claim path produces for a flow
// named flow whose executable DB steps are steps, in step order (D-56):
// the flow-scoped PreClaimName once, then, for each step, the six
// boundaries in boundaryOrder — Name(step, AfterClaim), Name(step,
// AfterHydrate), and so on. The returned order is stable across calls with
// identical arguments (deterministic iteration over steps and
// boundaryOrder, no map involved), so a failure at crash point N names the
// same boundary on every run.
//
// Matrix panics if steps is empty. This is deliberate, not a stray
// oversight: an empty matrix is the one crash-simulation-harness failure
// mode that can silently masquerade as a pass — a test suite that ranges
// over zero subtests reports zero failures — so Matrix refuses to produce
// one rather than let a caller's own "did I get anything back" check be the
// only thing standing between a real gap and a false "the harness passed"
// claim (T-02-20). A flow with no executable step is a caller error: every
// flow this harness is asked to certify must have declared at least one DB
// step before Matrix is ever called for it.
func Matrix(flow string, steps []string) []string {
	if len(steps) == 0 {
		panic(fmt.Errorf("crashpoint: Matrix(%q, ...): flow declares no executable steps — a crash-point matrix with nothing in it would let a harness run report success while certifying nothing (T-02-20)", flow))
	}

	names := make([]string, 0, 1+len(boundaryOrder)*len(steps))
	names = append(names, PreClaimName)
	for _, step := range steps {
		for _, b := range boundaryOrder {
			names = append(names, Name(step, b))
		}
	}
	return names
}
