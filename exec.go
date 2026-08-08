package entflow

import (
	"context"
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"
)

// Tx is the minimal transaction contract RunInTx needs: a caller-supplied
// transaction opener must produce a value satisfying this interface.
// *ent.Tx satisfies it (Commit() error, Rollback() error) with no adapter.
type Tx interface {
	Commit() error
	Rollback() error
}

// TxOpener opens a transaction of type T. *ent.Client satisfies
// TxOpener[*ent.Tx] with no adapter — its Tx(ctx) (*Tx, error) method
// matches exactly.
type TxOpener[T Tx] interface {
	Tx(context.Context) (T, error)
}

// WithSelfStatus supplies the explicit mechanism by which a flow declares
// HOW to read its owning entity's status — required because entflow core
// can never name the application's generated entity type. fn follows the
// same erasure pattern as the step constructors: this generic function
// captures the typed closure and stores a type-erased adapter on the flow's
// config, exactly like WithCodec (codec.go). Making the reader an explicit
// declared argument, rather than something inferred from the *Self steps,
// is required by the "codegen must never infer facts by inspecting closure
// bodies" invariant.
//
// Like WithCodec, the reader's In type is recorded (as cfg.selfStatusInType)
// so New[In] can eagerly reject a mismatch at the flow's declaration site —
// a caller who accidentally supplies a reader built for a different flow's
// input type gets a panic from New, not a runtime error deferred until a
// SelfWas-gated step is actually reached (WR-04).
func WithSelfStatus[In, TX any](fn func(ctx context.Context, tx TX, in In) (string, error)) FlowOption {
	return func(cfg *flowConfig) {
		cfg.selfStatusInType = reflect.TypeFor[In]()
		cfg.selfStatus = func(ctx context.Context, tx any, in any) (string, error) {
			typedTx, ok := tx.(TX)
			if !ok {
				return "", fmt.Errorf("entflow: WithSelfStatus: tx has type %T, want %s", tx, reflect.TypeFor[TX]())
			}
			typedIn, ok := in.(In)
			if !ok {
				return "", fmt.Errorf("entflow: WithSelfStatus: in has type %T, want %s", in, reflect.TypeFor[In]())
			}
			return fn(ctx, typedTx, typedIn)
		}
	}
}

// Exec runs f's DB steps, in dependency order, against the caller-supplied
// transaction tx. Exec neither opens, commits, nor rolls back tx — that is
// the caller's responsibility (D-08). This is the exact seam Phase 2's
// worker calls into.
//
// Exec runs these phases in order:
//  1. Validate and order (After edges resolved into a topological order,
//     declaration order the tie-break among independent steps) before
//     touching the transaction at all.
//  2. Refuse durable-only flows: any Activity or Emit step makes Exec
//     return an error wrapping ErrRequiresDurableRun, before executing
//     anything (D-13).
//  3. Take the entry snapshot: if any step declares a SelfWas condition,
//     invoke the WithSelfStatus reader once and hold its result for the
//     whole execution (D-10).
//  4. Derive a fresh per-execution result store on ctx.
//  5. Execute each step in order, evaluating its conditions against the
//     snapshot; a false condition skips the step and records no result.
//     Each step invocation is wrapped in its own recover boundary,
//     converting a panic into a *StepError without losing the original
//     panic value's error identity or its stack.
//  6. Record each successful step's return value for a later step's
//     Result[T]; on the first step error, return immediately.
func (f *FlowOf[In]) Exec(ctx context.Context, tx any, in In) error {
	order, err := topoOrder(f.steps)
	if err != nil {
		return err
	}

	for _, s := range f.steps {
		if s.kind == KindActivity || s.kind == KindEmit {
			return fmt.Errorf("entflow: flow %q: step %q (%s): %w", f.name, s.name, s.kind, ErrRequiresDurableRun)
		}
	}

	var selfStatus string
	if needsSelfStatus(f.steps) {
		if f.selfStatusReader == nil {
			return fmt.Errorf("entflow: flow %q: uses When(SelfWas(...)) but declares no WithSelfStatus reader (see entflow.WithSelfStatus)", f.name)
		}
		status, err := f.selfStatusReader(ctx, tx, in)
		if err != nil {
			return fmt.Errorf("entflow: flow %q: reading entry self status: %w", f.name, err)
		}
		selfStatus = status
	}

	ctx, rs := withResults(ctx)

	for _, s := range order {
		if !evalConditions(s.conditions, selfStatus) {
			continue
		}
		result, err := runStep(ctx, s, tx, in)
		if err != nil {
			return err
		}
		if s.constructor != "Check" {
			putResult(rs, s.name, result)
		}
	}
	return nil
}

// needsSelfStatus reports whether any step in steps declares a SelfWas
// condition, in which case Exec must read the entry-time status snapshot.
func needsSelfStatus(steps []*step) bool {
	for _, s := range steps {
		for _, c := range s.conditions {
			if c.Kind == string(conditionSelfWas) {
				return true
			}
		}
	}
	return false
}

// evalConditions reports whether every condition in conditions holds against
// the entry-time selfStatus snapshot. A step with no conditions always runs.
func evalConditions(conditions []Condition, selfStatus string) bool {
	for _, c := range conditions {
		if c.Kind == string(conditionSelfWas) && selfStatus != c.Value {
			return false
		}
	}
	return true
}

// runStep invokes s's adapter with the innermost recover boundary around
// exactly one closure call — never around the whole Exec loop, so a panic
// in one step cannot also swallow a bookkeeping bug in the executor's own
// step-to-step transition logic (D-16). If the recovered value is already
// an error it is threaded through as the %w-wrapped cause, so errors.Is
// still reaches sentinels like ErrNotProvided after conversion; otherwise
// it is formatted into one. debug.Stack() is always captured at the recover
// site so the original panic's call site stays visible in logs even though
// the panic has been converted into a returned error.
func runStep(ctx context.Context, s *step, tx any, in any) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			var cause error
			if e, ok := r.(error); ok {
				cause = e
			} else {
				cause = fmt.Errorf("panic: %v", r)
			}
			err = &StepError{
				Step:  s.name,
				Kind:  s.kind,
				Err:   cause,
				Stack: debug.Stack(),
			}
		}
	}()

	v, stepErr := s.run(ctx, tx, in)
	if stepErr != nil {
		return nil, &StepError{Step: s.name, Kind: s.kind, Err: stepErr}
	}
	return v, nil
}

// topoOrder resolves steps' After edges into a topological order, using
// each step's position in steps as the deterministic tie-break among
// independent steps. It returns a descriptive error, naming the steps
// involved, for an After edge naming an unknown step or for a dependency
// cycle — both are returned before Exec touches the transaction or executes
// anything.
func topoOrder(steps []*step) ([]*step, error) {
	index := make(map[string]int, len(steps))
	for i, s := range steps {
		index[s.name] = i
	}
	for _, s := range steps {
		for _, dep := range s.dependsOn {
			if _, ok := index[dep]; !ok {
				return nil, fmt.Errorf("entflow: step %q declares After(%q), but no step named %q exists", s.name, dep, dep)
			}
		}
	}

	const (
		white = iota
		gray
		black
	)
	state := make([]int, len(steps))
	order := make([]*step, 0, len(steps))
	var stack []string

	var visit func(i int) error
	visit = func(i int) error {
		switch state[i] {
		case black:
			return nil
		case gray:
			cyclePath := append(append([]string{}, stack...), steps[i].name)
			return fmt.Errorf("entflow: cycle detected in step dependencies: %s", strings.Join(cyclePath, " -> "))
		}
		state[i] = gray
		stack = append(stack, steps[i].name)
		for _, dep := range steps[i].dependsOn {
			if err := visit(index[dep]); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[i] = black
		order = append(order, steps[i])
		return nil
	}

	for i := range steps {
		if state[i] == white {
			if err := visit(i); err != nil {
				return nil, err
			}
		}
	}
	return order, nil
}

// RunInTx is D-08's sugar entry point: it opens a transaction via c, calls
// Exec, and commits or rolls back — following ent's own documented
// commit/rollback/panic pattern. Its transaction-level recover is a
// SEPARATE site from runStep's per-step recover above: this one protects
// Commit/Rollback bookkeeping (a panic escaping Exec itself, not caught by
// any per-step boundary), the other protects user closure panics. Collapsing
// them into one defer would lose both jobs.
func RunInTx[In any, T Tx](ctx context.Context, f *FlowOf[In], c TxOpener[T], in In) error {
	tx, err := c.Tx(ctx)
	if err != nil {
		return fmt.Errorf("entflow: opening transaction: %w", err)
	}

	defer func() {
		if r := recover(); r != nil {
			if rerr := tx.Rollback(); rerr != nil {
				panic(fmt.Errorf("entflow: panic escaped Exec: %v (rollback also failed: %w)", r, rerr))
			}
			panic(r)
		}
	}()

	if execErr := f.Exec(ctx, tx, in); execErr != nil {
		if rerr := tx.Rollback(); rerr != nil {
			return fmt.Errorf("%w: rolling back transaction: %v", execErr, rerr)
		}
		return execErr
	}

	return tx.Commit()
}
