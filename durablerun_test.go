package entflow_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/ent/schema"
	"github.com/smintz/entflow/internal/testdata/entclient"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
	"github.com/smintz/entflow/worker"
)

// cancelOrderFlowTyped resolves the fixture CancelOrder flow (the same
// entflow.Flow interface value cancelOrderFlow (meta_test.go) returns) back
// to its concrete *entflow.FlowOf[*schema.CancelOrderRequest], which
// entflow.Start's generic signature requires.
func cancelOrderFlowTyped(t *testing.T) *entflow.FlowOf[*schema.CancelOrderRequest] {
	t.Helper()
	f, ok := cancelOrderFlow(t).(*entflow.FlowOf[*schema.CancelOrderRequest])
	require.True(t, ok, "CancelOrder flow is not *entflow.FlowOf[*schema.CancelOrderRequest]")
	return f
}

// fixtureInput is the input type for this file's own local, two-DB-step and
// all-skipped test flows — deliberately independent of
// schema.CancelOrderRequest, since these flows exist only to exercise
// worker/dbstep.go's multi-step and skip-to-done logic, not the CancelOrder
// worked example.
type fixtureInput struct {
	OrderID int
}

// newMultiStepFlow returns a two-DB-step, unconditioned flow sharing the
// CancelOrderFlowRun table (via a fresh entflowfixture.RunStore over the
// same client) — RunMixin's base four states (pending/running/done/
// cancelled) are the same regardless of which flow produced a row, so a
// second, unrelated flow can persist to the same physical table with no
// schema change. Both steps mutate the same Order row so a test can observe
// each one ran.
func newMultiStepFlow() *entflow.FlowOf[*fixtureInput] {
	f := entflow.New[*fixtureInput]("MultiStep",
		entflow.WithOwnerRef(func(in *fixtureInput) (int, error) {
			return in.OrderID, nil
		}),
	)
	entflow.Step(f, "step1", func(ctx context.Context, tx *ent.Tx, in *fixtureInput) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).SetPaymentIntentID("step1").Save(ctx)
	})
	entflow.Step(f, "step2", func(ctx context.Context, tx *ent.Tx, in *fixtureInput) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).SetPaymentIntentID("step2").Save(ctx)
	}, entflow.After("step1"))
	return f
}

// newAllSkippedFlow returns a flow whose two DB steps both declare a
// When(SelfWas(...)) condition against a status no fixture Order is ever
// created with, so both steps are always skipped for any input this file's
// tests supply.
func newAllSkippedFlow() *entflow.FlowOf[*fixtureInput] {
	f := entflow.New[*fixtureInput]("AllSkipped",
		entflow.WithOwnerRef(func(in *fixtureInput) (int, error) {
			return in.OrderID, nil
		}),
		entflow.WithSelfStatus(func(ctx context.Context, tx *ent.Tx, in *fixtureInput) (string, error) {
			row, err := tx.Order.Get(ctx, in.OrderID)
			if err != nil {
				return "", err
			}
			return string(row.Status), nil
		}),
	)
	entflow.Step(f, "skipA", func(ctx context.Context, tx *ent.Tx, in *fixtureInput) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).SetPaymentIntentID("skipA-ran").Save(ctx)
	}, entflow.When(entflow.SelfWas("never-this-status")))
	entflow.Step(f, "skipB", func(ctx context.Context, tx *ent.Tx, in *fixtureInput) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).SetPaymentIntentID("skipB-ran").Save(ctx)
	}, entflow.When(entflow.SelfWas("never-this-status")), entflow.After("skipA"))
	return f
}

// TestDurableRunReachesDone proves DUR-01/DUR-03/D-30 end to end: Start
// persists a run row a worker later claims, advances by exactly one DB
// step's closure inside one transaction, and carries to the done state,
// with the owning Order mutated.
func TestDurableRunReachesDone(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	cancelOrder := cancelOrderFlowTyped(t)
	require.NoError(t, eng.Register(cancelOrder, store))

	owner, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
	require.NoError(t, err)

	run, err := entflow.Start(ctx, eng, cancelOrder, &schema.CancelOrderRequest{OrderID: owner.ID})
	require.NoError(t, err)
	require.Equal(t, entflow.RunStatePending, run.State)
	require.NotEmpty(t, run.Input)

	persisted, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StatePending, persisted.State)
	require.NotEmpty(t, persisted.Input)
	persistedOwner, err := persisted.QueryOwner().Only(ctx)
	require.NoError(t, err)
	require.Equal(t, owner.ID, persistedOwner.ID)

	w, err := worker.New(eng, worker.Options{
		Dialect:       "sqlite",
		Concurrency:   1,
		PollInterval:  5 * time.Millisecond,
		ClaimStrategy: worker.SQLiteStrategy(),
	})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()

	require.Eventually(t, func() bool {
		row, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
		require.NoError(t, err)
		return row.State == cancelorderflowrun.StateDone
	}, 2*time.Second, 5*time.Millisecond, "run never reached done")

	cancel()
	<-done

	finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateDone, finalRun.State)
	require.Equal(t, "cancel", finalRun.CurrentStep)
	require.NotNil(t, finalRun.FinishedAt)

	finalOrder, err := client.Order.Get(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, order.StatusCancelled, finalOrder.Status)
}

// TestClaimRollbackLeavesRunClaimable proves D-30's absence claim: rolling
// back a claim transaction after its step closure ran, but before Advance
// and Commit, leaves BOTH the step's effect (the Order mutation) and the
// run's progress pointer exactly as they were — never one without the
// other. Driven manually through the same exported primitives
// worker/dbstep.go composes, so the test can stop short of the commit
// dbstep.go always performs.
func TestClaimRollbackLeavesRunClaimable(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	cancelOrder := cancelOrderFlowTyped(t)
	require.NoError(t, eng.Register(cancelOrder, store))

	owner, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
	require.NoError(t, err)

	run, err := entflow.Start(ctx, eng, cancelOrder, &schema.CancelOrderRequest{OrderID: owner.ID})
	require.NoError(t, err)

	runner, ok := eng.RunnerFor(cancelOrder.Name())
	require.True(t, ok)

	txAny, err := store.BeginTx(ctx)
	require.NoError(t, err)
	tx, ok := txAny.(*ent.Tx)
	require.True(t, ok)

	q, ok := txAny.(entflow.RawQuerier)
	require.True(t, ok)
	strategy := worker.SQLiteStrategy()
	claimedID, ok, err := strategy.Claim(ctx, q, store.Table(), []string{entflow.RunStatePending, entflow.RunStateRunning})
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, run.ID, claimedID)

	loaded, err := store.Load(ctx, txAny, int(claimedID))
	require.NoError(t, err)
	require.Equal(t, entflow.RunStatePending, loaded.State)

	outcome, err := runner.ExecStep(ctx, txAny, entflow.StepCall{
		Step:  "cancel",
		Input: loaded.Input,
	})
	require.NoError(t, err)
	require.True(t, outcome.Ran)

	// Crash simulation: roll back instead of writing Advance and
	// committing.
	require.NoError(t, tx.Rollback())

	finalOrder, err := client.Order.Get(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, order.StatusPaid, finalOrder.Status, "the step's effect must not survive the rollback")

	finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StatePending, finalRun.State, "the progress pointer must not survive the rollback")
	require.Empty(t, finalRun.CurrentStep)
}

// TestRunAdvancesOneStepPerClaim proves DUR-06's boundary edge: a two-DB-
// step flow needs exactly two successful claims to reach done, and the run
// is claimable between them.
func TestRunAdvancesOneStepPerClaim(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	multiStep := newMultiStepFlow()
	require.NoError(t, eng.Register(multiStep, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)

	run, err := entflow.Start(ctx, eng, multiStep, &fixtureInput{OrderID: owner.ID})
	require.NoError(t, err)

	w, err := worker.New(eng, worker.Options{
		Dialect:       "sqlite",
		Concurrency:   1,
		ClaimStrategy: worker.SQLiteStrategy(),
	})
	require.NoError(t, err)

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed, "first claim should succeed")

	afterFirst, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateRunning, afterFirst.State, "run must still be claimable after one step")
	require.Equal(t, "step1", afterFirst.CurrentStep)

	claimed, err = w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed, "second claim should succeed")

	afterSecond, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateDone, afterSecond.State)
	require.Equal(t, "step2", afterSecond.CurrentStep)

	claimed, err = w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.False(t, claimed, "a done run must never be claimed again")
}

// TestAllStepsSkippedReachesDone proves DUR-06's empty edge: a run whose
// every remaining step's condition evaluates false advances to done without
// invoking any step closure and records no result.
func TestAllStepsSkippedReachesDone(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	allSkipped := newAllSkippedFlow()
	require.NoError(t, eng.Register(allSkipped, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)

	run, err := entflow.Start(ctx, eng, allSkipped, &fixtureInput{OrderID: owner.ID})
	require.NoError(t, err)

	w, err := worker.New(eng, worker.Options{
		Dialect:       "sqlite",
		Concurrency:   1,
		ClaimStrategy: worker.SQLiteStrategy(),
	})
	require.NoError(t, err)

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed)

	finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateDone, finalRun.State)
	require.Empty(t, finalRun.Results, "no step ran, so no result should be recorded")

	finalOrder, err := client.Order.Get(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, order.StatusDraft, finalOrder.Status, "neither step's closure should have run")
}
