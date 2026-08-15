package entflow_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	// Registers the "sqlite" database/sql driver name for
	// newFileBackedSQLiteClient — a direct import, not relying on another
	// test file in this package happening to have imported it first
	// (mirrors worker/conformance_test.go's own blank import and its doc
	// comment on why).
	_ "modernc.org/sqlite"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/ent/schema"
	"github.com/smintz/entflow/internal/testdata/entclient"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
	"github.com/smintz/entflow/worker"
)

// newFileBackedSQLiteClient returns a file-backed (never shared-cache
// in-memory) SQLite client with both _txlock=immediate and
// _busy_timeout=5000 — the D-38-era conformance requirement
// (worker/conformance_test.go's own newSQLiteConformanceClient) for any
// test that needs a second transaction to genuinely BLOCK on, and then
// retry into, a row another transaction currently holds, rather than fail
// immediately with SQLITE_BUSY/SQLITE_LOCKED. entclient.New's shared-cache
// in-memory DSN does not carry _busy_timeout and is not certified for this
// blocking-retry behavior — see plan 02-03's own documented pitfall.
func newFileBackedSQLiteClient(t *testing.T) *ent.Client {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "entflow_cancel.db")
	dsn := "file:" + dbPath + "?_fk=1&_txlock=immediate&_busy_timeout=5000"

	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(drv))
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.Schema.Create(context.Background()))
	return client
}

// TestCancelPendingRunMovesToCancelledAndNeverClaimedAgain proves D-43's
// core contract: cancelling a pending run sets its state to cancelled, and
// a subsequent claim finds nothing — cancelled is absent from the claim
// predicate's state list, so this is structural, not a special case.
func TestCancelPendingRunMovesToCancelledAndNeverClaimedAgain(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	cancelOrder := cancelOrderFlowTyped(t)
	require.NoError(t, eng.Register(cancelOrder, store))

	owner, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, cancelOrder, &schema.CancelOrderRequest{OrderID: owner.ID})
	require.NoError(t, err)

	ok, err := eng.Cancel(ctx, cancelOrder.Name(), run.ID)
	require.NoError(t, err)
	require.True(t, ok, "cancelling a pending run must report the guard matched")

	persisted, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateCancelled, persisted.State)
	require.NotNil(t, persisted.FinishedAt)

	w, err := worker.New(eng, worker.Options{
		Dialect:       "sqlite",
		Concurrency:   1,
		ClaimStrategy: worker.SQLiteStrategy(),
	})
	require.NoError(t, err)
	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.False(t, claimed, "a cancelled run must never be claimed again")
}

// TestCancelTwiceIsIdempotent proves cancelling an already-cancelled run is
// a safe no-op: the second call reports the guard did not match, and the
// stored state and finished_at are byte-identical to what the first cancel
// left them as.
func TestCancelTwiceIsIdempotent(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	cancelOrder := cancelOrderFlowTyped(t)
	require.NoError(t, eng.Register(cancelOrder, store))

	owner, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, cancelOrder, &schema.CancelOrderRequest{OrderID: owner.ID})
	require.NoError(t, err)

	ok, err := eng.Cancel(ctx, cancelOrder.Name(), run.ID)
	require.NoError(t, err)
	require.True(t, ok)

	afterFirst, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)

	ok, err = eng.Cancel(ctx, cancelOrder.Name(), run.ID)
	require.NoError(t, err)
	require.False(t, ok, "a second cancel must report the guard did not match")

	afterSecond, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, afterFirst.State, afterSecond.State)
	require.Equal(t, afterFirst.FinishedAt, afterSecond.FinishedAt, "cancelling twice must leave finished_at byte-identical")
}

// TestCancelDoneRunReportsNoMatch proves cancelling a run already in the
// done state is a no-op, exactly like cancelling an already-cancelled one —
// any terminal state, not only cancelled itself, fails the guard.
func TestCancelDoneRunReportsNoMatch(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	cancelOrder := cancelOrderFlowTyped(t)
	require.NoError(t, eng.Register(cancelOrder, store))

	owner, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, cancelOrder, &schema.CancelOrderRequest{OrderID: owner.ID})
	require.NoError(t, err)

	w, err := worker.New(eng, worker.Options{
		Dialect:       "sqlite",
		Concurrency:   1,
		ClaimStrategy: worker.SQLiteStrategy(),
	})
	require.NoError(t, err)
	// CancelOrder declares exactly one DB step, so a single successful
	// claim carries the run to done.
	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed)

	before, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateDone, before.State)

	ok, err := eng.Cancel(ctx, cancelOrder.Name(), run.ID)
	require.NoError(t, err)
	require.False(t, ok, "cancelling a done run must report the guard did not match")

	after, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateDone, after.State)
}

// TestCancelWhileStepInFlightAppliesAfterCommit proves D-43's concurrency
// edge against a real blocking-retry SQLite setup: a Cancel issued while a
// step's own claim transaction is mid-flight blocks on that transaction's
// row lock (D-30) until it commits, then applies — the in-flight step's
// effect is present, the run reaches cancelled, and the SECOND step of a
// two-step flow never executes.
func TestCancelWhileStepInFlightAppliesAfterCommit(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := newFileBackedSQLiteClient(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	stepEntered := make(chan struct{})
	releaseStep := make(chan struct{})

	flow := entflow.New[*fixtureInput]("CancelMidStep",
		entflow.WithOwnerRef(func(in *fixtureInput) (int, error) {
			return in.OrderID, nil
		}),
	)
	entflow.Step(flow, "step1", func(ctx context.Context, tx *ent.Tx, in *fixtureInput) (*ent.Order, error) {
		row, err := tx.Order.UpdateOneID(in.OrderID).SetPaymentIntentID("step1-committed").Save(ctx)
		if err != nil {
			return nil, err
		}
		close(stepEntered)
		<-releaseStep
		return row, nil
	})
	entflow.Step(flow, "step2", func(ctx context.Context, tx *ent.Tx, in *fixtureInput) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).SetPaymentIntentID("step2-must-not-run").Save(ctx)
	}, entflow.After("step1"))
	require.NoError(t, eng.Register(flow, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, flow, &fixtureInput{OrderID: owner.ID})
	require.NoError(t, err)

	w, err := worker.New(eng, worker.Options{
		Dialect:       "sqlite",
		Concurrency:   1,
		ClaimStrategy: worker.SQLiteStrategy(),
	})
	require.NoError(t, err)

	claimDone := make(chan error, 1)
	go func() {
		_, claimErr := w.ClaimOnce(ctx)
		claimDone <- claimErr
	}()

	<-stepEntered

	cancelDone := make(chan error, 1)
	go func() {
		_, cancelErr := eng.Cancel(ctx, "CancelMidStep", run.ID)
		cancelDone <- cancelErr
	}()

	// Give the Cancel goroutine time to actually reach its own BEGIN
	// IMMEDIATE and block on the claim transaction's still-held write
	// lock, before releasing the step — asserting the ordering property
	// this test exists for (cancel observed as pending, then the step
	// commits, then cancel applies) rather than a race where cancel simply
	// never got a chance to run first.
	time.Sleep(50 * time.Millisecond)
	close(releaseStep)

	require.NoError(t, <-claimDone)
	require.NoError(t, <-cancelDone)

	persistedRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateCancelled, persistedRun.State,
		"cancel must have applied once the in-flight step's transaction committed")

	persistedOrder, err := client.Order.Get(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, "step1-committed", persistedOrder.PaymentIntentID,
		"the in-flight step's effect must be present — cancel never aborts a step mid-flight")

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.False(t, claimed, "a cancelled run is structurally unclaimable — step2 must never run")

	finalOrder, err := client.Order.Get(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, "step1-committed", finalOrder.PaymentIntentID, "step2 must never have executed")
}
