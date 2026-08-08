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
