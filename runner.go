package entflow

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// Runner is the non-generic per-step execution seam the worker calls into.
// The worker holds a heterogeneous set of registered flows and can never
// name any of their In types, so every method here takes the flow's input as
// the codec bytes the run row already stores, rather than a typed In value.
// *FlowOf[In] implements Runner for every In (see the compile-time assertion
// below), decoding input through the flow's own codec inside each method.
type Runner interface {
	// StepOrder returns the names of f's DB steps only, in topological
	// order (After edges resolved, declaration order the tie-break) —
	// Activity and Emit steps are not yet executable durably in Phase 2
	// (D-13) and are omitted, not merely skipped, so "the step following
	// current_step" is always answerable without ever landing on a step
	// that must refuse to run.
	StepOrder() ([]string, error)

	// RequiresDurableRun reports, without executing anything, whether f has
	// no executable (DB) step for the worker to advance while still
	// declaring Activity or Emit steps — a flow that is entirely
	// not-yet-executable in Phase 2. Returns nil when f has at least one DB
	// step.
	RequiresDurableRun() error

	// EntrySelfStatus reads f's entry-time self-status snapshot (D-10/D-42)
	// by decoding input and invoking the flow's declared WithSelfStatus
	// reader against tx. Returns "", nil when no step declares a SelfWas
	// condition — the read never happens if nothing needs it.
	EntrySelfStatus(ctx context.Context, tx any, input []byte) (string, error)

	// OwnerRef resolves f's declared WithOwnerRef against the decoded
	// input, returning the D-26 lineage edge target boxed as any.
	OwnerRef(input []byte) (any, error)

	// ExecStep decodes input, evaluates the named step's conditions against
	// c.SelfWas, and — if they hold — executes exactly that one step's
	// closure against tx through the same recover boundary Exec uses.
	// Naming a step that is not KindDB returns an error wrapping
	// ErrRequiresDurableRun (D-13's honest refusal, alive inside a durable
	// run too).
	ExecStep(ctx context.Context, tx any, c StepCall) (StepOutcome, error)
}

// StepCall carries everything ExecStep needs to run exactly one step: the
// step's name, the flow's codec-encoded input, the entry-time self-status
// snapshot, and every prior step's persisted result. Results crosses this
// seam as json.RawMessage — never a live in-memory value — which is what
// makes D-40 (results always round-trip through JSON) structural rather
// than conventional: there is no in-memory shortcut for an uninterrupted
// worker pass to take.
type StepCall struct {
	Step    string
	Input   []byte
	SelfWas string
	Results map[string]json.RawMessage
}

// StepOutcome reports what ExecStep did. Ran is false when the step's
// conditions evaluated false — in that case Result and Transition are
// always zero. Result is the step's return value marshaled to JSON (nil for
// a Check step, which returns no entity).
type StepOutcome struct {
	Ran        bool
	Result     json.RawMessage
	Transition string
}

// Compile-time assertion that *FlowOf[In] implements Runner for every In —
// the worker never has to type-assert a Flow back to a concrete generic
// type to reach it.
var _ Runner = (*FlowOf[any])(nil)

// StepOrder implements Runner.
func (f *FlowOf[In]) StepOrder() ([]string, error) {
	order, err := topoOrder(f.steps)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(order))
	for _, s := range order {
		if s.kind != KindDB {
			continue
		}
		names = append(names, s.name)
	}
	return names, nil
}

// RequiresDurableRun implements Runner.
func (f *FlowOf[In]) RequiresDurableRun() error {
	order, err := f.StepOrder()
	if err != nil {
		return err
	}
	if len(order) > 0 {
		return nil
	}
	for _, s := range f.steps {
		if s.kind == KindActivity || s.kind == KindEmit {
			return fmt.Errorf("entflow: flow %q: declares no executable DB step yet (only %s steps): %w", f.name, s.kind, ErrRequiresDurableRun)
		}
	}
	return nil
}

// EntrySelfStatus implements Runner.
//
// Scoped to f's DB steps only (not the full f.steps Exec checks): a flow
// like CancelOrder legitimately declares a SelfWas condition on an Activity
// step that Phase 2 simply never executes (D-13). Exec's identical check
// never has this problem — it refuses the whole flow via
// ErrRequiresDurableRun before ever reaching the self-status check — but
// ExecStep/EntrySelfStatus run in a world where a durable-refusing step can
// coexist with executable DB steps, so demanding a WithSelfStatus reader
// because of a condition on a step that will never run would be a false
// requirement.
func (f *FlowOf[In]) EntrySelfStatus(ctx context.Context, tx any, input []byte) (string, error) {
	dbSteps := make([]*step, 0, len(f.steps))
	for _, s := range f.steps {
		if s.kind == KindDB {
			dbSteps = append(dbSteps, s)
		}
	}
	if !needsSelfStatus(dbSteps) {
		return "", nil
	}
	if f.selfStatusReader == nil {
		return "", fmt.Errorf("entflow: flow %q: uses When(SelfWas(...)) but declares no WithSelfStatus reader (see entflow.WithSelfStatus)", f.name)
	}
	in, err := f.codec.Unmarshal(input)
	if err != nil {
		return "", fmt.Errorf("entflow: flow %q: decoding input for entry self status: %w", f.name, err)
	}
	return f.selfStatusReader(ctx, tx, in)
}

// OwnerRef implements Runner.
func (f *FlowOf[In]) OwnerRef(input []byte) (any, error) {
	if f.ownerRef == nil {
		return nil, fmt.Errorf("entflow: flow %q: declares no WithOwnerRef", f.name)
	}
	in, err := f.codec.Unmarshal(input)
	if err != nil {
		return nil, fmt.Errorf("entflow: flow %q: decoding input for owner ref: %w", f.name, err)
	}
	return f.ownerRef(in)
}

// EncodeInput marshals a value asserted to be f's In through f's codec, for
// callers (entflow.Start) that hold in as any because they are calling
// through a non-generic seam.
func (f *FlowOf[In]) EncodeInput(in any) ([]byte, error) {
	typed, ok := in.(In)
	if !ok {
		return nil, fmt.Errorf("entflow: flow %q: EncodeInput: in has type %T, want %s", f.name, in, reflect.TypeFor[In]())
	}
	return f.codec.Marshal(typed)
}

// DecodeInput unmarshals b through f's codec, returning the result as any
// for callers that cannot name In.
func (f *FlowOf[In]) DecodeInput(b []byte) (any, error) {
	v, err := f.codec.Unmarshal(b)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// ExecStep implements Runner.
func (f *FlowOf[In]) ExecStep(ctx context.Context, tx any, c StepCall) (StepOutcome, error) {
	var target *step
	for _, s := range f.steps {
		if s.name == c.Step {
			target = s
			break
		}
	}
	if target == nil {
		return StepOutcome{}, fmt.Errorf("entflow: flow %q: ExecStep: no step named %q", f.name, c.Step)
	}
	if target.kind != KindDB {
		return StepOutcome{}, fmt.Errorf("entflow: flow %q: step %q (%s): %w", f.name, target.name, target.kind, ErrRequiresDurableRun)
	}

	in, err := f.codec.Unmarshal(c.Input)
	if err != nil {
		return StepOutcome{}, fmt.Errorf("entflow: flow %q: ExecStep: decoding input: %w", f.name, err)
	}

	// Hydrate a fresh per-call result store from the persisted, JSON-shaped
	// results (D-40) — never from a live in-memory value, so a step calling
	// Result[T] for a prior step behaves identically whether or not the run
	// crashed between claims. The stored bytes are used as-is, with no
	// re-marshal: they are already exactly what a previous claim's
	// putResult produced.
	ctx = withResultsFrom(ctx, c.Results)

	result, ran, err := runOneStep(ctx, target, tx, in, c.SelfWas)
	if err != nil {
		return StepOutcome{}, err
	}
	if !ran {
		return StepOutcome{Ran: false}, nil
	}

	var raw json.RawMessage
	if target.constructor != "Check" && result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return StepOutcome{}, fmt.Errorf("entflow: flow %q: step %q: marshaling result: %w", f.name, target.name, err)
		}
		raw = b
	}

	return StepOutcome{Ran: true, Result: raw, Transition: target.transition}, nil
}
