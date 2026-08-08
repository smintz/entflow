package entflow

import (
	"context"
	"fmt"
	"reflect"
)

// requireStepName panics with an error value naming constructor when name is
// empty. Declaration-time programming mistakes panic with an error VALUE
// (never a string) so a recovering context can errors.As them (D-16's
// discipline applied to declaration-time failures too).
func requireStepName(name, constructor string) {
	if name == "" {
		panic(fmt.Errorf("entflow: %s: step name must not be empty", constructor))
	}
}

// requireUniqueStepName panics naming both the existing and the redeclaring
// constructor when name already appears in existing. Step names are the key
// for After edges and Result[T] lookups, so a duplicate is unrecoverable
// ambiguity, not a runtime data condition.
func requireUniqueStepName(existing []*step, name, constructor string) {
	for _, s := range existing {
		if s.name == name {
			panic(fmt.Errorf("entflow: duplicate step name %q: already declared by %s, redeclared by %s", name, s.constructor, constructor))
		}
	}
}

// rejectIllegalTransition panics if s declares a Transition claim, naming
// name and constructor in the message. It is the single mechanism behind
// every non-mutating-DB-step-claim case that cannot legally claim a status
// transition: read-only DB constructors (Query, Check, checked by addDBStep)
// and steps that never touch the database directly at all (Activity, Emit,
// checked by activity.go) — centralized here so all four cases are covered by
// one helper instead of two independently-maintained checks (WR-05).
func rejectIllegalTransition(s *step, name, constructor string) {
	if s.transition != "" {
		panic(fmt.Errorf("entflow: step %q (%s) declares Transition(%q), but %s cannot legally claim a status transition", name, constructor, s.transition, constructor))
	}
}

// addDBStep is the single shared mechanism behind all seven DB-step
// constructors below (D-12): it validates the name, applies opts, rejects an
// illegal Transition claim on a read-only constructor, and appends the step.
// The seven exported constructors are thin typed wrappers that build a
// type-erased run adapter and delegate here — this is what makes D-12's
// "sugar over one mechanism" rationale true rather than aspirational.
func addDBStep[In any](
	f *FlowOf[In],
	name, constructor string,
	run func(ctx context.Context, tx any, in any) (any, error),
	opts []StepOption,
) *FlowOf[In] {
	requireStepName(name, constructor)
	requireUniqueStepName(f.steps, name, constructor)

	s := &step{
		name:        name,
		kind:        KindDB,
		constructor: constructor,
		run:         run,
	}
	for _, opt := range opts {
		opt(s)
	}
	if constructor == "Query" || constructor == "Check" {
		rejectIllegalTransition(s, name, constructor)
	}

	f.steps = append(f.steps, s)
	return f
}

// dbAdapter builds the type-erased run closure shared by every DB-step
// constructor except Check (whose closure returns only an error, see
// checkAdapter). Every type assertion uses the comma-ok form and reports both
// the actual and wanted types via %T on failure — never a bare panicking
// assertion.
func dbAdapter[In, TX, Ent any](name, constructor string, fn func(ctx context.Context, tx TX, in In) (Ent, error)) func(context.Context, any, any) (any, error) {
	return func(ctx context.Context, tx any, in any) (any, error) {
		typedTx, ok := tx.(TX)
		if !ok {
			return nil, fmt.Errorf("entflow: step %q (%s): tx has type %T, want %s", name, constructor, tx, reflect.TypeFor[TX]())
		}
		typedIn, ok := in.(In)
		if !ok {
			return nil, fmt.Errorf("entflow: step %q (%s): in has type %T, want %s", name, constructor, in, reflect.TypeFor[In]())
		}
		return fn(ctx, typedTx, typedIn)
	}
}

// checkAdapter builds Check's type-erased run closure. Check's closure
// returns only an error; the adapter returns a nil result value alongside it
// so the executor's result-recording path stays uniform across step kinds —
// the executor records nothing for a Check step regardless (see exec.go).
func checkAdapter[In, TX any](name string, fn func(ctx context.Context, tx TX, in In) error) func(context.Context, any, any) (any, error) {
	return func(ctx context.Context, tx any, in any) (any, error) {
		typedTx, ok := tx.(TX)
		if !ok {
			return nil, fmt.Errorf("entflow: step %q (Check): tx has type %T, want %s", name, tx, reflect.TypeFor[TX]())
		}
		typedIn, ok := in.(In)
		if !ok {
			return nil, fmt.Errorf("entflow: step %q (Check): in has type %T, want %s", name, in, reflect.TypeFor[In]())
		}
		if err := fn(ctx, typedTx, typedIn); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

// Step is the generic escape hatch among the DB-step constructors: any DB
// mutation or read that does not fit CreateSelf/UpdateSelf/Create/Update/
// Query's naming. Its closure fn receives the caller-supplied transaction
// (typed TX, inferred from the closure literal) and the flow's input, and
// returns the resulting entity.
func Step[In, TX, Ent any](f *FlowOf[In], name string, fn func(ctx context.Context, tx TX, in In) (Ent, error), opts ...StepOption) *FlowOf[In] {
	return addDBStep(f, name, "Step", dbAdapter[In, TX, Ent](name, "Step", fn), opts)
}

// CreateSelf declares a DB step that creates the entity owning the flow.
func CreateSelf[In, TX, Ent any](f *FlowOf[In], name string, fn func(ctx context.Context, tx TX, in In) (Ent, error), opts ...StepOption) *FlowOf[In] {
	return addDBStep(f, name, "CreateSelf", dbAdapter[In, TX, Ent](name, "CreateSelf", fn), opts)
}

// UpdateSelf declares a DB step that mutates the entity owning the flow. Its
// closure fn receives the caller-supplied transaction (typed as TX, inferred
// from the closure literal — never named explicitly at the call site) and
// the flow's input, and returns the updated entity.
func UpdateSelf[In, TX, Ent any](f *FlowOf[In], name string, fn func(ctx context.Context, tx TX, in In) (Ent, error), opts ...StepOption) *FlowOf[In] {
	return addDBStep(f, name, "UpdateSelf", dbAdapter[In, TX, Ent](name, "UpdateSelf", fn), opts)
}

// Create declares a DB step that creates an entity other than the one
// owning the flow.
func Create[In, TX, Ent any](f *FlowOf[In], name string, fn func(ctx context.Context, tx TX, in In) (Ent, error), opts ...StepOption) *FlowOf[In] {
	return addDBStep(f, name, "Create", dbAdapter[In, TX, Ent](name, "Create", fn), opts)
}

// Update declares a DB step that mutates an entity other than the one
// owning the flow.
func Update[In, TX, Ent any](f *FlowOf[In], name string, fn func(ctx context.Context, tx TX, in In) (Ent, error), opts ...StepOption) *FlowOf[In] {
	return addDBStep(f, name, "Update", dbAdapter[In, TX, Ent](name, "Update", fn), opts)
}

// Query declares a read-only DB step. A Query step cannot legally declare a
// Transition — a read cannot claim a status change — so Transition(...) on a
// Query panics at declaration time.
func Query[In, TX, Ent any](f *FlowOf[In], name string, fn func(ctx context.Context, tx TX, in In) (Ent, error), opts ...StepOption) *FlowOf[In] {
	return addDBStep(f, name, "Query", dbAdapter[In, TX, Ent](name, "Query", fn), opts)
}

// Check declares a guard step: its closure returns only an error (no
// entity), and the executor records no result for it. Like Query, a Check
// step cannot legally declare a Transition.
func Check[In, TX any](f *FlowOf[In], name string, fn func(ctx context.Context, tx TX, in In) error, opts ...StepOption) *FlowOf[In] {
	return addDBStep(f, name, "Check", checkAdapter[In, TX](name, fn), opts)
}
