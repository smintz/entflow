package worker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/entclient"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
	"github.com/smintz/entflow/worker"
)

// retryInput is the input type this file's own local, single-DB-step flow
// uses — deliberately independent of schema.CancelOrderRequest, mirroring
// worker_test.go's own concurrencyInput convention.
type retryInput struct {
	OrderID int
}

// fakeSQLStateError satisfies the same single-method shape
// *pgconn.PgError does (SQLState() string) without entflow (or this test)
// importing pgx — proving classifyRetryable's errors.As reaches a
// structurally-matching type, exactly the way it will reach the real
// driver's error type in production.
type fakeSQLStateError struct {
	code string
}

func (e *fakeSQLStateError) Error() string    { return "fake driver error: " + e.code }
func (e *fakeSQLStateError) SQLState() string { return e.code }

// newRetryFlow returns a single-DB-step flow, named "cancel" so its
// failed:<step> state (failed:cancel) is a value CancelOrderFlowRun's own
// RunMixin-derived `state` enum already contains — this file's fixture
// flows share that table (entflowfixture.New) exactly like durablerun_
// test.go's and worker_test.go's local flows do, and an arbitrary step name
// would fail ent's own enum validator, not the classification logic this
// file is actually testing. errFn is called with the 1-based call number on
// every claim attempt; a nil return succeeds the step.
func newRetryFlow(errFn func(callNum int) error) *entflow.FlowOf[*retryInput] {
	f := entflow.New[*retryInput]("RetryFixture",
		entflow.WithOwnerRef(func(in *retryInput) (int, error) {
			return in.OrderID, nil
		}),
	)
	callNum := 0
	entflow.Step(f, "cancel", func(ctx context.Context, tx *ent.Tx, in *retryInput) (*ent.Order, error) {
		callNum++
		if err := errFn(callNum); err != nil {
			return nil, err
		}
		return tx.Order.UpdateOneID(in.OrderID).SetStatus(order.StatusCancelled).Save(ctx)
	})
	return f
}

// newRetryWorker returns a single-goroutine SQLite worker over eng —
// concurrency 1 keeps every test in this file single-threaded and
// deterministic, matching durablerun_test.go's and selfloader_test.go's own
// SQLite worker setup.
func newRetryWorker(t *testing.T, eng *entflow.Engine, opts worker.Options) *worker.Worker {
	t.Helper()
	opts.Dialect = "sqlite"
	opts.Concurrency = 1
	opts.ClaimStrategy = worker.SQLiteStrategy()
	w, err := worker.New(eng, opts)
	require.NoError(t, err)
	return w
}

// claimEventually retries ClaimOnce until it succeeds or timeout elapses —
// the mechanism a retry-scheduled run needs, since it is unclaimable again
// until its retry_after time (set by worker/retry.go's jittered backoff)
// has actually passed.
func claimEventually(t *testing.T, ctx context.Context, w *worker.Worker, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		claimed, err := w.ClaimOnce(ctx)
		require.NoError(t, err)
		return claimed
	}, timeout, 5*time.Millisecond, "claim never succeeded within %s", timeout)
}

// TestRetryScheduledOnRetryableError proves D-51's retry branch: a step
// error carrying a retryable SQLSTATE increments attempt, sets a non-null
// retry_after, and leaves state running with current_step unchanged (Fail
// has no CurrentStep field to touch), so the SAME step is retried on a
// later claim.
func TestRetryScheduledOnRetryableError(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	flow := newRetryFlow(func(int) error {
		return &fakeSQLStateError{code: "40001"} // serialization_failure
	})
	require.NoError(t, eng.Register(flow, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, flow, &retryInput{OrderID: owner.ID})
	require.NoError(t, err)

	w := newRetryWorker(t, eng, worker.Options{})

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed)

	finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateRunning, finalRun.State)
	require.Equal(t, 1, finalRun.Attempt)
	require.NotNil(t, finalRun.RetryAfter)
	require.Empty(t, finalRun.CurrentStep, "current_step must stay unchanged so the same step retries")
	require.Empty(t, finalRun.LastError)
}

// TestRetryFailsAtCeilingAfterExhaustingAttempts proves DUR-07's "after
// retries exhaust" sentence directly: the same retryable error, retried
// until DefaultMaxAttempts is reached, lands the run in failed:cancel with
// a non-empty last_error and a non-null finished_at — never retried
// forever.
func TestRetryFailsAtCeilingAfterExhaustingAttempts(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	flow := newRetryFlow(func(int) error {
		return &fakeSQLStateError{code: "40P01"} // deadlock_detected
	})
	require.NoError(t, eng.Register(flow, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, flow, &retryInput{OrderID: owner.ID})
	require.NoError(t, err)

	w := newRetryWorker(t, eng, worker.Options{})

	for i := 0; i < worker.DefaultMaxAttempts; i++ {
		claimEventually(t, ctx, w, 3*time.Second)
	}

	finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateFailedCancel, finalRun.State)
	require.NotEmpty(t, finalRun.LastError)
	require.Contains(t, finalRun.LastError, "cancel", "last_error comes from StepError's rendered message, which names the failing step")
	require.NotNil(t, finalRun.FinishedAt)
}

// TestNonRetryableErrorFailsImmediately proves the other half of D-51: an
// error classifyRetryable does not recognize fails the run to
// failed:<step> on the FIRST attempt, without ever incrementing attempt —
// retrying a business-logic error would just burn the ceiling for nothing.
func TestNonRetryableErrorFailsImmediately(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	flow := newRetryFlow(func(int) error {
		return errors.New("business rule violated: order already shipped")
	})
	require.NoError(t, eng.Register(flow, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, flow, &retryInput{OrderID: owner.ID})
	require.NoError(t, err)

	w := newRetryWorker(t, eng, worker.Options{})

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed)

	finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateFailedCancel, finalRun.State)
	require.Equal(t, 0, finalRun.Attempt, "a non-retryable error must not burn the attempt counter")
	require.Contains(t, finalRun.LastError, "business rule violated")
	require.NotNil(t, finalRun.FinishedAt)
}

// TestDoneRunHasEmptyLastError proves the empty edge DUR-07 names
// explicitly: a run that reaches done never has anything in last_error.
func TestDoneRunHasEmptyLastError(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	flow := newRetryFlow(func(int) error { return nil })
	require.NoError(t, eng.Register(flow, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, flow, &retryInput{OrderID: owner.ID})
	require.NoError(t, err)

	w := newRetryWorker(t, eng, worker.Options{})

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed)

	finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateDone, finalRun.State)
	require.Empty(t, finalRun.LastError)
}

// TestRetryableErrorHookClassifiesUnrecognizedError proves Options.
// RetryableError is consulted for an error neither built-in layer
// recognizes — the seam a MySQL deployment (or any application) uses to
// classify more without entflow importing another driver.
func TestRetryableErrorHookClassifiesUnrecognizedError(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	sentinel := errors.New("a MySQL-style deadlock number, unclassified by entflow's own built-ins")
	flow := newRetryFlow(func(int) error { return sentinel })
	require.NoError(t, eng.Register(flow, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, flow, &retryInput{OrderID: owner.ID})
	require.NoError(t, err)

	w := newRetryWorker(t, eng, worker.Options{
		RetryableError: func(err error) bool {
			return errors.Is(err, sentinel)
		},
	})

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed)

	finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateRunning, finalRun.State,
		"the RetryableError hook must classify this as retryable, scheduling a retry rather than a terminal failure")
	require.Equal(t, 1, finalRun.Attempt)
	require.NotNil(t, finalRun.RetryAfter)
}

// TestGuardMissDoesNotOverwriteRun proves the guard every failure write
// shares with Advance (D-30): a write guarded on stale FromState/FromStep
// values — captured before a concurrent transaction changed the run,
// exactly what claimOnce's own load-then-write window is vulnerable to —
// reports the guard miss (ok=false, err=nil) rather than overwriting
// whatever the concurrent transaction committed. Driven directly through
// RunStore.Fail, the exact primitive worker/dbstep.go's failStep calls,
// mirroring durablerun_test.go's TestClaimRollbackLeavesRunClaimable's
// style of exercising the seam by hand rather than through the full worker.
func TestGuardMissDoesNotOverwriteRun(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	flow := newRetryFlow(func(int) error { return errors.New("boom") })
	require.NoError(t, eng.Register(flow, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, flow, &retryInput{OrderID: owner.ID})
	require.NoError(t, err)

	// Simulate a concurrent cancellation (D-43) landing between a claim's
	// load and its failure write: cancel the run in its own, already
	// committed transaction first.
	cancelTx, err := client.Tx(ctx)
	require.NoError(t, err)
	cancelled, err := store.Cancel(ctx, cancelTx, run.ID)
	require.NoError(t, err)
	require.True(t, cancelled)
	require.NoError(t, cancelTx.Commit())

	// Now attempt exactly the stale failure write claimOnce would have
	// issued had it loaded the run BEFORE the cancellation above — guarded
	// on the run still being pending, which it no longer is.
	failTx, err := client.Tx(ctx)
	require.NoError(t, err)
	failed, err := store.Fail(ctx, failTx, run.ID, entflow.Fail{
		FromState: entflow.RunStatePending,
		FromStep:  "",
		ToState:   entflow.RunStateFailed("cancel"),
		LastError: "boom",
		Attempt:   1,
	})
	require.NoError(t, err)
	require.False(t, failed, "the guard must not match a run that was concurrently cancelled")
	require.NoError(t, failTx.Commit())

	finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateCancelled, finalRun.State,
		"the cancellation must not be overwritten by the stale, guard-missed failure write")
}
