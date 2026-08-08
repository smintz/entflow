package entflow

import "context"

// StepKind identifies the safety class of a step. Each kind has a distinct
// closure signature and a distinct set of things it is permitted to touch —
// DB steps receive a transaction handle, Activities never do, and Emits are
// fire-and-forget outbox writes. See entflow.md §3.2.
type StepKind string

const (
	// KindDB identifies a step whose closure receives the caller-supplied
	// transaction handle and mutates the database inside it.
	KindDB StepKind = "db"
	// KindActivity identifies a step whose closure performs an external
	// call and must never receive a transaction handle.
	KindActivity StepKind = "activity"
	// KindEmit identifies a fire-and-forget outbox write, delivered
	// post-commit by the relay.
	KindEmit StepKind = "emit"
)

// step is the internal, unexported representation of a single declared step.
//
// Declaration DATA lives in plain fields that hold no function values — name,
// kind, constructor, dependency edges, conditions, the transition claimed,
// the emit topic, and the retry policy. The closure itself lives in exactly
// one, separate field: run. This separation is the structural half of
// D-19/CORE-11 — code that only ever touches the data fields (Meta(),
// Describe(), and later Phase 5's AST extraction) structurally cannot reach
// the closure, and a test asserts this invariant by counting invocations.
type step struct {
	name        string
	kind        StepKind
	constructor string
	dependsOn   []string
	conditions  []Condition
	transition  string
	emitTopic   string
	retry       *RetryPolicy

	// run is the type-erased adapter around the user's typed closure. It is
	// the ONLY field on step that holds a function value. For Activity and
	// Emit steps it is never invoked by Exec in Phase 1 (D-13) — see
	// activity.go.
	run func(ctx context.Context, tx any, in any) (any, error)
}

// StepOption configures a step at declaration time. The set of options ships
// incrementally across Phase 1's plans.
type StepOption func(*step)

// Transition declares the status value this step's mutation moves the owning
// entity to. It is claim-only in Phase 1 (SM-01/D-14) — the enforcing hook and
// the bidirectional cross-validator against a Transitions annotation arrive
// in Phase 6. Declaring Transition on a Query or Check step panics at
// declaration time (steps.go's addDBStep) — a read-only step cannot legally
// claim a status transition.
func Transition(to string) StepOption {
	return func(s *step) {
		s.transition = to
	}
}
