package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/wfmarker"
)

// claimStates are the two run states a claim query considers (D-32). done,
// cancelled, and every failed:<step> value are terminal and structurally
// unclaimable.
var claimStates = []string{entflow.RunStatePending, entflow.RunStateRunning}

// claimOnce is the phase's whole safety argument (D-30): it opens exactly
// one transaction, claims at most one run via strategy, executes exactly
// one DB step's closure against that transaction, writes the step's effect
// and the run's progress pointer, and commits — all before returning. A
// crash or panic anywhere in this window rolls the whole transaction back,
// leaving the row exactly as claimable as it was.
//
// claimed is false with a nil error when nothing was claimable — not an
// error condition.
func claimOnce(ctx context.Context, store entflow.RunStore, runner entflow.Runner, strategy ClaimStrategy, opts Options) (claimed bool, err error) {
	txAny, err := store.BeginTx(ctx)
	if err != nil {
		return false, fmt.Errorf("entflow/worker: opening claim transaction: %w", err)
	}
	tx, ok := txAny.(entflow.Tx)
	if !ok {
		return false, fmt.Errorf("entflow/worker: claim transaction has type %T, want entflow.Tx", txAny)
	}

	committed := false
	defer func() {
		if !committed {
			// A no-op rollback after a successful commit is impossible here
			// because every return path below either commits and sets
			// committed=true, or returns before committing — so this defer
			// always rolls back an uncommitted transaction, never a
			// committed one.
			_ = tx.Rollback()
		}
	}()

	// The claim transaction's context derivation (D-44): the workflow
	// marker is applied exactly once, here, immediately after the
	// transaction opens and before the run is hydrated — so every hook,
	// privacy rule and step closure that runs inside this claim sees it,
	// and nothing outside a claim does. This is the ONLY call to the
	// internal package's setter anywhere in the module, enforced by
	// marker_test.go's AST scan (TestWorkflowMarkerHasExactlyOneSetter).
	ctx = wfmarker.Set(ctx)

	q, ok := txAny.(entflow.RawQuerier)
	if !ok {
		return false, fmt.Errorf("entflow/worker: claim transaction has type %T, does not satisfy entflow.RawQuerier", txAny)
	}

	id, ok, err := strategy.Claim(ctx, q, store.Table(), claimStates)
	if err != nil {
		return false, fmt.Errorf("entflow/worker: claim query: %w", err)
	}
	if !ok {
		return false, nil
	}

	run, err := store.Load(ctx, txAny, id)
	if err != nil {
		return false, fmt.Errorf("entflow/worker: loading claimed run %v: %w", id, err)
	}

	claimable := false
	for _, s := range claimStates {
		if run.State == s {
			claimable = true
			break
		}
	}
	if !claimable {
		return false, fmt.Errorf("entflow/worker: run %v: %w: state is %q", id, entflow.ErrRunNotClaimable, run.State)
	}

	stepOrder, err := runner.StepOrder()
	if err != nil {
		return false, fmt.Errorf("entflow/worker: resolving step order: %w", err)
	}

	// selfWas is the D-42 entry-time self-status snapshot, persisted on the
	// run row's self_was column: taken once, on the first claim of a run
	// (run.CurrentStep == ""), and read back from run.SelfWas (the value
	// RunStore.Load hydrated from that same self_was column) on every later
	// claim — never recomputed against the entity's live status, which is
	// exactly what keeps a SelfWas condition's D-10 entry semantics stable
	// across a crash and across processes.
	selfWas := run.SelfWas
	nextIdx := 0
	if run.CurrentStep == "" {
		selfWas, err = runner.EntrySelfStatus(ctx, txAny, run.Input)
		if err != nil {
			return false, fmt.Errorf("entflow/worker: reading entry self status: %w", err)
		}
	} else {
		found := false
		for i, name := range stepOrder {
			if name == run.CurrentStep {
				nextIdx = i + 1
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Errorf("entflow/worker: run %v: current_step %q not found in flow's step order", id, run.CurrentStep)
		}
	}

	adv := entflow.Advance{
		FromState:    run.State,
		FromStep:     run.CurrentStep,
		CurrentStep:  run.CurrentStep,
		Attempt:      run.Attempt,
		Results:      run.Results,
		TraceContext: run.TraceContext,
		SelfWas:      selfWas,
	}

	// Load the current claim's live self value once, before executing any
	// step — a fresh read on every claim, inside this claim's own
	// transaction, never cached across claims and never rehydrated from
	// persisted JSON (D-41). A flow that declares no WithSelfLoader gets
	// ErrNoSelfLoader here, which is not a claim failure: self simply stays
	// nil, and Self[T] inside a step reports that same sentinel.
	self, err := runner.LoadSelf(ctx, txAny, run.Input)
	if err != nil && !errors.Is(err, entflow.ErrNoSelfLoader) {
		return false, fmt.Errorf("entflow/worker: loading self for run %v: %w", id, err)
	}

	ranStep := ""
	failingStep := ""
	var stepErr error
	for i := nextIdx; i < len(stepOrder); i++ {
		name := stepOrder[i]
		outcome, execErr := runner.ExecStep(ctx, txAny, entflow.StepCall{
			Step:    name,
			Input:   run.Input,
			SelfWas: selfWas,
			Results: run.Results,
			Self:    self,
		})
		if execErr != nil {
			failingStep = name
			stepErr = execErr
			break
		}
		if !outcome.Ran {
			continue
		}
		ranStep = name
		if adv.Results == nil {
			adv.Results = map[string]json.RawMessage{}
		}
		if outcome.Result != nil {
			adv.Results[name] = outcome.Result
		}
		break
	}

	// DUR-07: make "after retries exhaust" literally true. A retryable
	// driver error (D-51) schedules a retry by incrementing attempt and
	// setting retry_after, leaving state running and current_step
	// unchanged, so the same step is retried on a later claim — until the
	// attempt ceiling is reached, at which point (or immediately, for a
	// non-retryable error, which never burns the counter) the run fails to
	// failed:<step> with the error recorded. Both writes go through
	// RunStore.Fail, guarded exactly like Advance: a guard miss means
	// another transaction already touched this run (most importantly,
	// cancelled it, D-43) between this claim's load and this write, and
	// must be handled — not ignored — by abandoning the claim rather than
	// resurrecting a run that is no longer ours.
	if stepErr != nil {
		return failStep(ctx, store, txAny, tx, id, run, failingStep, stepErr, opts, &committed)
	}

	if ranStep != "" {
		adv.CurrentStep = ranStep
		if len(stepOrder) > 0 && stepOrder[len(stepOrder)-1] == ranStep {
			adv.ToState = entflow.RunStateDone
			adv.Finished = true
		} else {
			adv.ToState = entflow.RunStateRunning
		}
	} else {
		// Every remaining step's conditions evaluated false: walk straight
		// to done without having invoked a closure or recorded a result.
		adv.ToState = entflow.RunStateDone
		adv.Finished = true
	}

	advanced, err := store.Advance(ctx, txAny, id, adv)
	if err != nil {
		return false, fmt.Errorf("entflow/worker: advancing run %v: %w", id, err)
	}
	if !advanced {
		return false, fmt.Errorf("entflow/worker: advancing run %v: %w", id, entflow.ErrRunNotAdvanced)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("entflow/worker: committing claim transaction: %w", err)
	}
	committed = true
	return true, nil
}

// failStep is claimOnce's DUR-07 terminal-states/retry branch, split out
// only for readability — it shares claimOnce's transaction (txAny/tx) and
// its committed flag (via pointer, so the shared defer in claimOnce still
// rolls back exactly once on any early return).
//
// classifyRetryable (retry.go) decides retryable vs. not. A retryable error
// below the attempt ceiling (DefaultMaxAttempts) schedules a retry: attempt
// increments, retry_after is set from the database-independent process
// clock via nextRetryAfter's exponential-with-jitter schedule, state stays
// running, and current_step is left untouched by RunStore.Fail (it has no
// CurrentStep field to set) — so the SAME step is retried on a later claim.
// Everything else — a retryable error at the ceiling, or any non-retryable
// error — fails the run to failed:<failingStep> immediately, recording
// stepErr's rendered message (StepError.Error(), which names the step and
// kind and never embeds the run's input bytes, T-02-03) in last_error. A
// non-retryable error never increments attempt at all — retrying a
// business-logic error would just burn the ceiling for no reason.
func failStep(ctx context.Context, store entflow.RunStore, txAny any, tx entflow.Tx, id any, run *entflow.Run, failingStep string, stepErr error, opts Options, committed *bool) (bool, error) {
	f := entflow.Fail{
		FromState: run.State,
		FromStep:  run.CurrentStep,
	}

	retryable := classifyRetryable(stepErr, opts)
	newAttempt := run.Attempt
	if retryable {
		newAttempt = run.Attempt + 1
	}

	if retryable && newAttempt < DefaultMaxAttempts {
		retryAt := nextRetryAfter(time.Now(), newAttempt)
		f.ToState = entflow.RunStateRunning
		f.Attempt = newAttempt
		f.RetryAfter = &retryAt
	} else {
		f.ToState = entflow.RunStateFailed(failingStep)
		f.Attempt = newAttempt
		f.LastError = stepErr.Error()
	}

	failed, ferr := store.Fail(ctx, txAny, id, f)
	if ferr != nil {
		return false, fmt.Errorf("entflow/worker: failing run %v: %w", id, ferr)
	}
	if !failed {
		// The guard (FromState/FromStep) did not match — another
		// transaction already touched this run (e.g. cancelled it, D-43)
		// since this claim's load. Report it and let the caller's deferred
		// rollback abandon the claim; never overwrite.
		return false, fmt.Errorf("entflow/worker: failing run %v: %w", id, entflow.ErrRunNotAdvanced)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("entflow/worker: committing claim transaction: %w", err)
	}
	*committed = true
	return true, nil
}
