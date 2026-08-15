package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/entclient"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
)

// newSlowFlow returns a single-DB-step flow named name whose step closure
// blocks — signaling started (a buffered channel; sent on, never closed, so
// a run reclaimed and re-executed after a rollback doesn't panic sending
// twice) the moment it begins, then waiting for EITHER release to close (the
// step finishes normally, recording "finished" on the owning Order) OR its
// own ctx to be cancelled (the step returns ctx.Err(), exactly what a real
// crash produces: nothing further in claimOnce's transaction can write
// successfully once ctx is cancelled, so the whole claim rolls back).
func newSlowFlow(name string, started chan<- struct{}, release <-chan struct{}) *entflow.FlowOf[*loopInput] {
	f := entflow.New[*loopInput](name,
		entflow.WithOwnerRef(func(in *loopInput) (int, error) {
			return in.OrderID, nil
		}),
	)
	entflow.Step(f, "slow", func(ctx context.Context, tx *ent.Tx, in *loopInput) (*ent.Order, error) {
		started <- struct{}{}
		select {
		case <-release:
			return tx.Order.UpdateOneID(in.OrderID).SetPaymentIntentID("finished").Save(ctx)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	return f
}

// TestShutdownWaitsForInFlightStepToCommit proves D-49's polite path:
// Shutdown issued while a step is mid-flight does not return until that
// step's transaction has actually committed, and the effect is present
// afterward.
func TestShutdownWaitsForInFlightStepToCommit(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	flow := newSlowFlow("ShutdownWaits", started, release)
	require.NoError(t, eng.Register(flow, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, flow, &loopInput{OrderID: owner.ID})
	require.NoError(t, err)

	w, err := New(eng, Options{
		Dialect:      "sqlite",
		Concurrency:  1,
		PollInterval: 10 * time.Millisecond,
		DrainTimeout: 2 * time.Second,
	})
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("step never started")
	}

	shutdownDone := make(chan error, 1)
	shutdownStart := time.Now()
	go func() { shutdownDone <- w.Shutdown(context.Background()) }()

	// Shutdown must still be waiting a short while later — proving it did
	// not return before the step's transaction committed.
	time.Sleep(150 * time.Millisecond)
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned before the in-flight step released")
	default:
	}

	close(release)

	var shutdownErr error
	select {
	case shutdownErr = <-shutdownDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown never returned after the step released")
	}
	require.NoError(t, shutdownErr)
	require.Greater(t, time.Since(shutdownStart), 140*time.Millisecond,
		"Shutdown must have actually waited for the step, not returned immediately")

	<-runDone

	final, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateDone, final.State)

	updatedOwner, err := client.Order.Get(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, "finished", updatedOwner.PaymentIntentID)
}

// TestShutdownDrainTimeoutRollsBackAndReportsInFlightCount proves the
// paired-absence claim on the impolite path: a drain deadline shorter than
// a deliberately slow step returns a non-nil error naming the in-flight
// count, the step's effect is absent, the run's state/current_step are
// unchanged, and a subsequently started worker claims and completes the SAME
// run afterward — the crash-shaped outcome D-49 promises.
func TestShutdownDrainTimeoutRollsBackAndReportsInFlightCount(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	started := make(chan struct{}, 4)
	release := make(chan struct{}) // never closed here — the step blocks until its ctx is cancelled
	flow := newSlowFlow("ShutdownTimeout", started, release)
	require.NoError(t, eng.Register(flow, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, flow, &loopInput{OrderID: owner.ID})
	require.NoError(t, err)

	w, err := New(eng, Options{
		Dialect:      "sqlite",
		Concurrency:  1,
		PollInterval: 10 * time.Millisecond,
		DrainTimeout: 150 * time.Millisecond,
	})
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("step never started")
	}

	shutdownErr := w.Shutdown(context.Background())
	require.Error(t, shutdownErr)
	require.Contains(t, shutdownErr.Error(), "1", "the drain-timeout error must name the in-flight count")

	<-runDone

	unchanged, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StatePending, unchanged.State)
	require.Empty(t, unchanged.CurrentStep)

	unchangedOwner, err := client.Order.Get(ctx, owner.ID)
	require.NoError(t, err)
	require.Empty(t, unchangedOwner.PaymentIntentID, "the step's effect must not survive the drain-timeout rollback")

	// A fresh worker claims and completes the SAME run afterward. Release
	// the gate first so the re-claimed step doesn't block forever again.
	close(release)
	w2, err := New(eng, Options{Dialect: "sqlite", Concurrency: 1, PollInterval: 10 * time.Millisecond})
	require.NoError(t, err)
	runCtx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	done2 := make(chan error, 1)
	go func() { done2 <- w2.Run(runCtx2) }()

	waitForRunsDone(t, ctx, client, []int{run.ID.(int)}, 2*time.Second)
	cancel2()
	require.NoError(t, <-done2)

	finalOwner, err := client.Order.Get(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, "finished", finalOwner.PaymentIntentID)
}

// TestShutdownCalledTwiceIsANoOpTheSecondTime covers both a Worker whose Run
// was never invoked and one that actually ran and drained — the second
// Shutdown call must return nil without redoing any work either way.
func TestShutdownCalledTwiceIsANoOpTheSecondTime(t *testing.T) {
	t.Run("never started", func(t *testing.T) {
		eng := entflow.NewEngine()
		w, err := New(eng, Options{Dialect: "sqlite", Concurrency: 1})
		require.NoError(t, err)
		require.NoError(t, w.Shutdown(context.Background()))
		require.NoError(t, w.Shutdown(context.Background()))
	})

	t.Run("after a real drain", func(t *testing.T) {
		client := entclient.New(t)
		store := entflowfixture.New(client)
		eng := entflow.NewEngine()

		var counter atomic.Int64
		flow := newLoopFlow("ShutdownTwiceAfterDrain", &counter)
		require.NoError(t, eng.Register(flow, store))

		w, err := New(eng, Options{Dialect: "sqlite", Concurrency: 1, PollInterval: 10 * time.Millisecond})
		require.NoError(t, err)

		done := make(chan error, 1)
		go func() { done <- w.Run(context.Background()) }()
		// Give Run's goroutine a moment to actually enter pollLoop before
		// Shutdown, avoiding the inherent (and here irrelevant) race
		// between "go w.Run(ctx)" returning and its first statements
		// running.
		time.Sleep(20 * time.Millisecond)

		require.NoError(t, w.Shutdown(context.Background()))
		require.NoError(t, w.Shutdown(context.Background()))
		<-done
	})
}

// TestShutdownOnNeverStartedWorkerReturnsWithoutError proves Shutdown is
// safe to call on a Worker whose Run has never been invoked: nothing to
// stop, nothing to drain.
func TestShutdownOnNeverStartedWorkerReturnsWithoutError(t *testing.T) {
	eng := entflow.NewEngine()
	w, err := New(eng, Options{Dialect: "sqlite", Concurrency: 1})
	require.NoError(t, err)
	require.NoError(t, w.Shutdown(context.Background()))
}

// TestShutdownZeroDrainEquivalentToCancellingRunContext proves Run's
// own doc-commented guarantee: cancelling the context passed to Run takes
// the identical path a zero-drain Shutdown would — the in-flight step's
// transaction rolls back, leaving the same paired absence (no effect, no
// progress-pointer advance) a real crash would.
func TestShutdownZeroDrainEquivalentToCancellingRunContext(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	started := make(chan struct{}, 4)
	release := make(chan struct{}) // never closed
	flow := newSlowFlow("CancelRunCtx", started, release)
	require.NoError(t, eng.Register(flow, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, flow, &loopInput{OrderID: owner.ID})
	require.NoError(t, err)

	w, err := New(eng, Options{Dialect: "sqlite", Concurrency: 1, PollInterval: 10 * time.Millisecond})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("step never started")
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	final, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StatePending, final.State)
	require.Empty(t, final.CurrentStep)

	finalOwner, err := client.Order.Get(ctx, owner.ID)
	require.NoError(t, err)
	require.Empty(t, finalOwner.PaymentIntentID,
		"cancelling Run's context must leave the same crash-shaped absence a zero-drain Shutdown would")
}
