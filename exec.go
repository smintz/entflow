package entflow

import (
	"context"
	"fmt"
)

// Exec runs every step of f, in declaration order, against the
// caller-supplied transaction tx. Exec neither opens, commits, nor rolls back
// tx — that is the caller's responsibility. This is deliberate: it is the
// exact seam Phase 2's worker calls into, so its signature is a contract with
// the next phase, not a private implementation detail (D-08).
//
// Before executing anything, Exec refuses any flow containing an Activity or
// Emit step, returning an error wrapping ErrRequiresDurableRun (D-13) — a
// naive in-line execution path for them would have no idempotency key and no
// persistence, exactly the unsafe route this project exists to eliminate.
//
// On the first step error, Exec returns immediately without touching the
// transaction, leaving rollback to the caller. Ordering by After edges,
// condition evaluation, and panic recovery into *StepError all arrive in
// this plan's next task — this checkpoint executes, in declaration order,
// the DB steps a durable-only flow declares.
func (f *FlowOf[In]) Exec(ctx context.Context, tx any, in In) error {
	for _, s := range f.steps {
		if s.kind == KindActivity || s.kind == KindEmit {
			return fmt.Errorf("entflow: flow %q: step %q (%s): %w", f.name, s.name, s.kind, ErrRequiresDurableRun)
		}
	}

	for _, s := range f.steps {
		if _, err := s.run(ctx, tx, in); err != nil {
			return err
		}
	}
	return nil
}
