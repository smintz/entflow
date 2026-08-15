package entflow

import (
	"errors"
	"fmt"
)

// ErrRequiresDurableRun is returned by Exec when a flow contains an Activity
// or Emit step (D-13). Phase 1 ships their builders and metadata, but an
// honest, loud refusal to execute them in-line is safer than a
// working-but-wrong path with no idempotency key and no persistence.
var ErrRequiresDurableRun = errors.New("entflow: flow requires a durable run (Activity/Emit steps are not executable in Phase 1)")

// ErrUnknownFlow is returned when a caller (entflow.Start, the worker) names
// a flow that has no RunStore registered on the Engine (D-24/D-25) — a
// programming-time wiring mistake (a flow declared but never
// Engine.Register'd), not a runtime data condition.
var ErrUnknownFlow = errors.New("entflow: unknown flow")

// ErrRunNotClaimable is returned when a run row loaded immediately after a
// successful claim is found NOT to be in a claimable state (D-32) — a
// defensive check against a ClaimStrategy/RunStore disagreeing about which
// states are claimable, which would otherwise silently execute a step
// against a terminal or already-claimed run.
var ErrRunNotClaimable = errors.New("entflow: run not claimable")

// ErrRunNotAdvanced is returned when a RunStore.Advance or RunStore.Fail
// conditional update's guard (FromState/FromStep) does not match the row's
// current values — the same run was concurrently advanced by another
// transaction between this claim's load and its write, which the guard
// exists to detect rather than silently overwrite (D-30).
var ErrRunNotAdvanced = errors.New("entflow: run advance guard did not match")

// ErrNoSelfLoader is returned by Self[T] when the current step's context
// carries no live self value — a flow that calls Self without declaring
// WithSelfLoader. Contrast Result[T]'s ErrUnknownStep (a step name never
// recorded) and a SelfWas condition's silent false-evaluation: Self is the
// one of the three seams that reports its own absence as an error a step
// author sees directly, since there is no sensible default live entity to
// hand back.
var ErrNoSelfLoader = errors.New("entflow: flow declares no WithSelfLoader (see entflow.WithSelfLoader)")

// StepError is the error every step failure surfaces as. It carries the
// failing step's identity alongside the underlying cause, and unwraps to that
// cause so errors.Is and errors.As reach through it — ent's own error types
// (a not-found error, a validation error raised by a hook) must remain
// reachable through a *StepError (D-09).
type StepError struct {
	// Step is the name of the step that failed.
	Step string
	// Kind is the failing step's safety class.
	Kind StepKind
	// Err is the underlying cause — either the error the step closure
	// returned, or (per D-16) a recovered panic value converted to an
	// error.
	Err error
	// Stack holds runtime/debug.Stack() output captured at the recover
	// site by the executor. Its purpose is to preserve diagnostic
	// information when a panic is converted to a returned error, so a
	// genuine runtime bug in a step body stays visible in logs rather than
	// being laundered into an ordinary step failure. Error() deliberately
	// does not render it inline — it is a field for observability code to
	// read on purpose, not incidental error-message noise.
	Stack []byte
}

// Error renders the step name, its kind, and the wrapped cause.
func (e *StepError) Error() string {
	return fmt.Sprintf("entflow: step %q (%s) failed: %v", e.Step, e.Kind, e.Err)
}

// Unwrap returns e.Err, so errors.Is and errors.As reach through a *StepError
// to its underlying cause.
func (e *StepError) Unwrap() error {
	return e.Err
}
