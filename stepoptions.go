package entflow

// StepOption configures a step at declaration time. Every DB-step
// constructor, Activity, and Emit accepts a variadic list of StepOptions as
// their final argument.
type StepOption func(*step)

// After declares that a step must execute only once every named step has
// already executed. Multiple After calls (or a single call naming several
// steps) accumulate dependency edges; the executor resolves them into a
// topological order before running anything (exec.go).
func After(names ...string) StepOption {
	return func(s *step) {
		s.dependsOn = append(s.dependsOn, names...)
	}
}

// When attaches a condition that must evaluate true for the step to run. A
// step with no When option always runs (subject to its After ordering).
func When(c Condition) StepOption {
	return func(s *step) {
		s.conditions = append(s.conditions, c)
	}
}

// Transition declares the status value this step's mutation moves the owning
// entity to. It is claim-only in Phase 1 (SM-01/D-14) — the enforcing hook and
// the bidirectional cross-validator against a Transitions annotation arrive
// in Phase 6. Declaring Transition on a Query or Check step, or on an
// Activity or Emit step (steps.go's rejectIllegalTransition, called from
// addDBStep and activity.go), panics at declaration time — none of these four
// constructors mutate the database directly, so none can legally claim a
// status transition.
func Transition(to string) StepOption {
	return func(s *step) {
		s.transition = to
	}
}

// conditionKind identifies the kind of predicate a Condition evaluates. It
// is a plain string type, not an enum-like interface, so Condition stays a
// data-only struct Plan 04's metadata path can render without evaluating
// anything.
type conditionKind string

// conditionSelfWas identifies a Condition produced by SelfWas: true when the
// entry-time status snapshot (D-10) equals Condition.Value.
const conditionSelfWas conditionKind = "self_was"

// Condition is a DATA-ONLY predicate attached to a step via When — exported
// fields for the condition's kind and the value it compares against, and no
// function-typed field anywhere. This matters beyond style: Plan 04's meta
// package renders conditions into FlowMeta, and CORE-11 requires every
// codegen-needed fact to be readable without evaluating anything.
type Condition struct {
	Kind  string
	Value string
}

// SelfWas returns a Condition that is true when the owning entity's status,
// snapshotted once at flow entry (D-10 — not a live re-read, not the
// post-mutation value), equals status.
func SelfWas(status string) Condition {
	return Condition{Kind: string(conditionSelfWas), Value: status}
}
