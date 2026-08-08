package entflow

import "context"

// Exec runs every step of f, in declaration order, against the
// caller-supplied transaction tx. Exec neither opens, commits, nor rolls back
// tx — that is the caller's responsibility. This is deliberate: it is the
// exact seam Phase 2's worker calls into, so its signature is a contract with
// the next phase, not a private implementation detail (D-08).
//
// On the first step error, Exec returns immediately without touching the
// transaction, leaving rollback to the caller. Ordering by After edges,
// condition evaluation, panic recovery, *StepError wrapping, and
// ErrRequiresDurableRun for Activity/Emit steps all arrive in later plans —
// this tracer executes, in declaration order, the single DB step kind it
// declares.
func (f *FlowOf[In]) Exec(ctx context.Context, tx any, in In) error {
	for _, s := range f.steps {
		if _, err := s.run(ctx, tx, in); err != nil {
			return err
		}
	}
	return nil
}
