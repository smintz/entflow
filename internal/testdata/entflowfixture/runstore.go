// Package entflowfixture is the hand-written stand-in for what Phase 6's
// codegen generates (D-24): a RunStore adapter over CancelOrderFlowRun, the
// run entity plan 02-01 hand-wrote from entflow.RunMixin. Its method bodies
// are the template target for the generated equivalent. Every method
// asserts the erased tx back to *ent.Tx with a comma-ok assertion reporting
// both the actual and wanted types, never a bare assertion.
package entflowfixture

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/predicate"
)

// RunStore implements entflow.RunStore over the generated *ent.Client's
// CancelOrderFlowRun builders.
type RunStore struct {
	client *ent.Client
}

// New returns a RunStore backed by client.
func New(client *ent.Client) *RunStore {
	return &RunStore{client: client}
}

var _ entflow.RunStore = (*RunStore)(nil)

// BeginTx implements entflow.RunStore.
func (s *RunStore) BeginTx(ctx context.Context) (any, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return tx, nil
}

// Table implements entflow.RunStore.
func (s *RunStore) Table() entflow.RunTable {
	return entflow.RunTable{
		Name:             cancelorderflowrun.Table,
		IDColumn:         cancelorderflowrun.FieldID,
		StateColumn:      cancelorderflowrun.FieldState,
		RetryAfterColumn: cancelorderflowrun.FieldRetryAfter,
	}
}

// asTx asserts tx back to *ent.Tx with a comma-ok assertion, reporting both
// the actual and wanted types on mismatch — never a bare panicking
// assertion.
func asTx(tx any) (*ent.Tx, error) {
	etx, ok := tx.(*ent.Tx)
	if !ok {
		return nil, fmt.Errorf("entflowfixture: tx has type %T, want *ent.Tx", tx)
	}
	return etx, nil
}

// asID asserts runID back to the generated client's int ID type. Accepts
// both int (the value RunStore.Insert itself returns) and int64 (the
// universal scan type worker.ClaimStrategy.Claim returns, since the worker
// never knows the application's generated ID type) — converting the latter
// is this adapter's job, not the worker's, exactly as D-24 intends: entflow
// core never names the application's ID type, but a hand-written (or
// generated) adapter always does.
func asID(runID any) (int, error) {
	switch v := runID.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	default:
		return 0, fmt.Errorf("entflowfixture: runID has type %T, want int or int64", runID)
	}
}

// Insert implements entflow.RunStore.
func (s *RunStore) Insert(ctx context.Context, tx any, in entflow.InsertRun) (any, error) {
	etx, err := asTx(tx)
	if err != nil {
		return nil, err
	}
	ownerID, ok := in.OwnerRef.(int)
	if !ok {
		return nil, fmt.Errorf("entflowfixture: Insert: OwnerRef has type %T, want int", in.OwnerRef)
	}
	row, err := etx.CancelOrderFlowRun.Create().
		SetState(cancelorderflowrun.State(in.State)).
		SetInput(in.Input).
		SetOwnerID(ownerID).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	return row.ID, nil
}

// Claim implements entflow.RunStore. Per the interface's own doc comment,
// this is a convenience, non-concurrency-safe resolution for callers that
// are not the worker's own claim loop — the worker's real mutual-exclusion
// guarantee comes exclusively from worker.ClaimStrategy running raw SQL
// directly against the transaction's RawQuerier (D-36); this method never
// duplicates that SQL.
func (s *RunStore) Claim(ctx context.Context, tx any, q entflow.ClaimQuery) (any, bool, error) {
	etx, err := asTx(tx)
	if err != nil {
		return nil, false, err
	}
	states := make([]cancelorderflowrun.State, len(q.States))
	for i, st := range q.States {
		states[i] = cancelorderflowrun.State(st)
	}
	row, err := etx.CancelOrderFlowRun.Query().
		Where(cancelorderflowrun.StateIn(states...)).
		Order(cancelorderflowrun.ByID()).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return row.ID, true, nil
}

// Load implements entflow.RunStore.
func (s *RunStore) Load(ctx context.Context, tx any, runID any) (*entflow.Run, error) {
	etx, err := asTx(tx)
	if err != nil {
		return nil, err
	}
	id, err := asID(runID)
	if err != nil {
		return nil, err
	}
	row, err := etx.CancelOrderFlowRun.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return toRun(row), nil
}

func toRun(row *ent.CancelOrderFlowRun) *entflow.Run {
	return &entflow.Run{
		ID:           row.ID,
		State:        string(row.State),
		Input:        row.Input,
		CurrentStep:  row.CurrentStep,
		Attempt:      row.Attempt,
		LastError:    row.LastError,
		Results:      row.Results,
		RetryAfter:   row.RetryAfter,
		TraceContext: row.TraceContext,
		SelfWas:      row.SelfWas,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
		StartedAt:    row.StartedAt,
		FinishedAt:   row.FinishedAt,
	}
}

// currentStepPredicate matches a run row whose current_step equals step.
// current_step is an optional, non-nillable string column (D-28): ent
// stores "never set" as SQL NULL and scans it back as Go's zero value "",
// so an empty step must be matched via CurrentStepIsNil, not
// CurrentStepEQ("").
func currentStepPredicate(step string) predicate.CancelOrderFlowRun {
	if step == "" {
		return cancelorderflowrun.CurrentStepIsNil()
	}
	return cancelorderflowrun.CurrentStepEQ(step)
}

// Advance implements entflow.RunStore. The conditional update is guarded by
// adv.FromState and adv.FromStep matching the row's current values; ok
// reports whether the guard matched, distinguishing "another transaction
// already advanced this run" (ok=false, err=nil) from a genuine error.
func (s *RunStore) Advance(ctx context.Context, tx any, runID any, adv entflow.Advance) (bool, error) {
	etx, err := asTx(tx)
	if err != nil {
		return false, err
	}
	id, err := asID(runID)
	if err != nil {
		return false, err
	}

	upd := etx.CancelOrderFlowRun.UpdateOneID(id).
		Where(
			cancelorderflowrun.StateEQ(cancelorderflowrun.State(adv.FromState)),
			currentStepPredicate(adv.FromStep),
		).
		SetState(cancelorderflowrun.State(adv.ToState)).
		SetAttempt(adv.Attempt).
		SetResults(adv.Results).
		SetSelfWas(adv.SelfWas).
		SetTraceContext(adv.TraceContext)

	if adv.CurrentStep != "" {
		upd = upd.SetCurrentStep(adv.CurrentStep)
	}
	if adv.RetryAfter != nil {
		upd = upd.SetRetryAfter(*adv.RetryAfter)
	} else {
		upd = upd.ClearRetryAfter()
	}
	// started_at is set exactly once, on the advance that moves the run out
	// of pending — the same write that first claims it (D-28).
	if adv.FromState == entflow.RunStatePending {
		upd = upd.SetStartedAt(time.Now())
	}
	if adv.Finished {
		upd = upd.SetFinishedAt(time.Now())
	}

	_, err = upd.Save(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Fail implements entflow.RunStore. Guarded identically to Advance.
func (s *RunStore) Fail(ctx context.Context, tx any, runID any, f entflow.Fail) (bool, error) {
	etx, err := asTx(tx)
	if err != nil {
		return false, err
	}
	id, err := asID(runID)
	if err != nil {
		return false, err
	}

	upd := etx.CancelOrderFlowRun.UpdateOneID(id).
		Where(
			cancelorderflowrun.StateEQ(cancelorderflowrun.State(f.FromState)),
			currentStepPredicate(f.FromStep),
		).
		SetState(cancelorderflowrun.State(f.ToState)).
		SetAttempt(f.Attempt).
		SetLastError(f.LastError)

	if f.RetryAfter != nil {
		upd = upd.SetRetryAfter(*f.RetryAfter)
	} else {
		upd = upd.ClearRetryAfter()
	}
	if strings.HasPrefix(f.ToState, "failed:") {
		upd = upd.SetFinishedAt(time.Now())
	}

	_, err = upd.Save(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Cancel implements entflow.RunStore — a state transition (D-43), guarded
// so only a pending or running run can be cancelled.
func (s *RunStore) Cancel(ctx context.Context, tx any, runID any) (bool, error) {
	etx, err := asTx(tx)
	if err != nil {
		return false, err
	}
	id, err := asID(runID)
	if err != nil {
		return false, err
	}

	_, err = etx.CancelOrderFlowRun.UpdateOneID(id).
		Where(cancelorderflowrun.StateIn(cancelorderflowrun.StatePending, cancelorderflowrun.StateRunning)).
		SetState(cancelorderflowrun.StateCancelled).
		SetFinishedAt(time.Now()).
		Save(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
