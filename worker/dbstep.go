package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/smintz/entflow"
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
func claimOnce(ctx context.Context, store entflow.RunStore, runner entflow.Runner, strategy ClaimStrategy) (claimed bool, err error) {
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

	ranStep := ""
	for i := nextIdx; i < len(stepOrder); i++ {
		name := stepOrder[i]
		outcome, execErr := runner.ExecStep(ctx, txAny, entflow.StepCall{
			Step:    name,
			Input:   run.Input,
			SelfWas: selfWas,
			Results: run.Results,
		})
		if execErr != nil {
			return false, fmt.Errorf("entflow/worker: executing step %q: %w", name, execErr)
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
