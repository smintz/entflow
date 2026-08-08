package entflow

import (
	"context"
	"fmt"
)

// Activity declares a step whose closure performs an external call. Its type
// parameter list deliberately contains NO transaction type, and its closure
// parameter list deliberately contains NO transaction parameter — that
// omission IS the entire CORE-04 guarantee. It is not a runtime check that
// could be bypassed; it is an absence the type checker enforces, proven by
// internal/testdata/compilefail/activity_tx.go (a build that fails).
//
// Activity is declarable in Phase 1 but not executable: Exec refuses any
// flow containing an Activity or Emit step with ErrRequiresDurableRun,
// before running anything (D-13). Activities become executable in Phase 3.
func Activity[In, Ent, Out any](f *FlowOf[In], name string, fn func(ctx context.Context, self Ent, att Attempt) (JSON[Out], error), opts ...StepOption) *FlowOf[In] {
	requireStepName(name, "Activity")
	requireUniqueStepName(f.steps, name, "Activity")

	s := &step{
		name:        name,
		kind:        KindActivity,
		constructor: "Activity",
	}
	for _, opt := range opts {
		opt(s)
	}
	// The run field carries the same DB-step-shaped signature purely so
	// declaration data stays uniform across step kinds — it is never
	// invoked. Exec refuses any flow containing an Activity or Emit step
	// (step 2 of its phased validation, exec.go) before executing any
	// step's run, so this body is unreachable in ordinary operation; it
	// panics rather than silently misbehaving if that invariant is ever
	// violated by a future bug.
	s.run = func(ctx context.Context, tx any, in any) (any, error) {
		panic(fmt.Errorf("entflow: Activity step %q invoked directly; Exec refuses durable-only flows before executing any step (D-13)", name))
	}

	f.steps = append(f.steps, s)
	return f
}

// Emit declares a fire-and-forget outbox write. Unlike every DB-step
// constructor and Activity, Emit takes no closure at all — it is pure
// declaration. topic doubles as the step's name (the identifier After edges
// and Result[T] lookups key on) and as the recorded emit topic CORE-11
// requires to be present in the builder data.
//
// Like Activity, Emit is declarable but not executable in Phase 1 — Exec
// refuses any flow containing one with ErrRequiresDurableRun (D-13). Emits
// become executable in Phase 4.
func Emit[In any](f *FlowOf[In], topic string, opts ...StepOption) *FlowOf[In] {
	requireStepName(topic, "Emit")
	requireUniqueStepName(f.steps, topic, "Emit")

	s := &step{
		name:        topic,
		kind:        KindEmit,
		constructor: "Emit",
		emitTopic:   topic,
	}
	for _, opt := range opts {
		opt(s)
	}
	// See Activity's identical comment above — unreachable in ordinary
	// operation, guarded defensively rather than left silently misbehaving.
	s.run = func(ctx context.Context, tx any, in any) (any, error) {
		panic(fmt.Errorf("entflow: Emit step %q invoked directly; Exec refuses durable-only flows before executing any step (D-13)", topic))
	}

	f.steps = append(f.steps, s)
	return f
}
