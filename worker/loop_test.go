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

// This file is an internal (white-box) test file — package worker, not
// worker_test — deliberately, so TestPollJitterBandAcrossManyWaits can call
// jitteredInterval directly rather than inferring its distribution from
// wall-clock timing. Every other test here drives the loop through its
// exported surface (New, Run) against a real SQLite client, matching the
// rest of this package's convention.

// loopInput is the input type for this file's own local, single-DB-step
// fixture flow — deliberately independent of schema.CancelOrderRequest,
// mirroring worker_test.go's own concurrencyInput pattern.
type loopInput struct {
	OrderID int
}

// newLoopFlow returns a single-DB-step, unconditioned flow named name that
// increments counter every time its step closure actually runs.
func newLoopFlow(name string, counter *atomic.Int64) *entflow.FlowOf[*loopInput] {
	f := entflow.New[*loopInput](name,
		entflow.WithOwnerRef(func(in *loopInput) (int, error) {
			return in.OrderID, nil
		}),
	)
	entflow.Step(f, "mutate", func(ctx context.Context, tx *ent.Tx, in *loopInput) (*ent.Order, error) {
		counter.Add(1)
		return tx.Order.UpdateOneID(in.OrderID).SetPaymentIntentID("touched").Save(ctx)
	})
	return f
}

// waitForRunsDone polls every run named by ids until each has reached done,
// or fails the test after timeout.
func waitForRunsDone(t *testing.T, ctx context.Context, client *ent.Client, ids []int, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, id := range ids {
			row, err := client.CancelOrderFlowRun.Get(ctx, id)
			require.NoError(t, err)
			if row.State != cancelorderflowrun.StateDone {
				return false
			}
		}
		return true
	}, timeout, 5*time.Millisecond, "not every run reached done within %s", timeout)
}

// TestPollJitterBandAcrossManyWaits samples jitteredInterval well beyond the
// 20-consecutive-wait floor and asserts every sample lies within the
// configured band around base, and that the samples are not all equal —
// jitter applied fresh on every call, not memoized once. Asserting a band
// rather than an exact sequence keeps this test non-flaky by construction.
func TestPollJitterBandAcrossManyWaits(t *testing.T) {
	const base = 100 * time.Millisecond
	const jitter = 0.3
	const samples = 30

	lower := time.Duration(float64(base) * (1 - jitter))
	upper := time.Duration(float64(base) * (1 + jitter))

	seen := make(map[time.Duration]bool, samples)
	for i := 0; i < samples; i++ {
		got := jitteredInterval(base, jitter)
		require.GreaterOrEqualf(t, got, lower, "sample %d (%s) fell below the configured jitter band [%s, %s]", i, got, lower, upper)
		require.LessOrEqualf(t, got, upper, "sample %d (%s) exceeded the configured jitter band [%s, %s]", i, got, lower, upper)
		seen[got] = true
	}
	require.Greater(t, len(seen), 1, "%d consecutive waits sampled the same value — jitter must be applied fresh on every wait, not once", samples)
}

// TestPollJitterZeroDisablesIt proves jitteredInterval's own documented
// escape hatch: jitter<=0 returns base unchanged, every time.
func TestPollJitterZeroDisablesIt(t *testing.T) {
	const base = 250 * time.Millisecond
	for i := 0; i < 5; i++ {
		require.Equal(t, base, jitteredInterval(base, 0))
	}
}

// TestNudgeShortensWaitMaterially proves the D-33 wakeup path: with a long
// PollInterval, a run started (and therefore Engine.Notify'd, engine.go)
// while the worker's pollLoop is mid-wait still completes in a small
// fraction of that interval — the nudge, not the next tick, is what woke it.
func TestNudgeShortensWaitMaterially(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	var counter atomic.Int64
	flow := newLoopFlow("NudgeShortensWait", &counter)
	require.NoError(t, eng.Register(flow, store))

	w, err := New(eng, Options{
		Dialect:      "sqlite",
		Concurrency:  1,
		PollInterval: 2 * time.Second,
		Jitter:       0.1,
	})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()

	// Give pollLoop time to enter its first (long) wait before anything is
	// claimable, so the run started below genuinely interrupts a wait in
	// progress rather than racing the loop's very first iteration.
	time.Sleep(50 * time.Millisecond)

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	start := time.Now()
	// entflow.Start's own commit calls Engine.Notify (engine.go) — the
	// nudge under test — with no separate call needed here.
	run, err := entflow.Start(ctx, eng, flow, &loopInput{OrderID: owner.ID})
	require.NoError(t, err)

	waitForRunsDone(t, ctx, client, []int{run.ID.(int)}, 500*time.Millisecond)
	elapsed := time.Since(start)
	require.Lessf(t, elapsed, 1*time.Second,
		"a nudge delivered during the wait must shorten it materially below the %s poll interval; took %s", w.opts.PollInterval, elapsed)

	cancel()
	require.NoError(t, <-done)
	require.EqualValues(t, 1, counter.Load())
}

// TestPollingBackstopCompletesRunsWithNudgesDiscarded proves polling is the
// correctness backstop, not merely a fallback in name: with the one nudge
// entflow.Start sends drained BEFORE the worker under test is even
// constructed — so its pollLoop never has a chance to observe it — the run
// still reaches done, driven entirely by the short poll interval.
func TestPollingBackstopCompletesRunsWithNudgesDiscarded(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	var counter atomic.Int64
	flow := newLoopFlow("PollingBackstop", &counter)
	require.NoError(t, eng.Register(flow, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, flow, &loopInput{OrderID: owner.ID})
	require.NoError(t, err)

	select {
	case <-eng.Nudges():
	default:
		t.Fatal("expected entflow.Start to have sent a nudge (engine.go's Notify)")
	}

	w, err := New(eng, Options{
		Dialect:      "sqlite",
		Concurrency:  1,
		PollInterval: 20 * time.Millisecond,
		Jitter:       0.2,
	})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()

	waitForRunsDone(t, ctx, client, []int{run.ID.(int)}, 2*time.Second)

	cancel()
	require.NoError(t, <-done)
	require.EqualValues(t, 1, counter.Load())
}

// TestPollLoopClaimsWithoutFullIntervalWaitBetweenRuns proves the loop's
// single most impactful latency property: a goroutine that just claimed a
// run tries again immediately, never waiting out PollInterval first. With
// PollInterval set to 3s and 5 runs already pending when the worker starts,
// claiming all 5 must finish in a small fraction of 5*3s (which the
// per-step-costs-a-poll-interval bug this test guards against would need).
func TestPollLoopClaimsWithoutFullIntervalWaitBetweenRuns(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	var counter atomic.Int64
	flow := newLoopFlow("NoWaitAfterClaim", &counter)
	require.NoError(t, eng.Register(flow, store))

	const numRuns = 5
	ids := make([]int, 0, numRuns)
	for i := 0; i < numRuns; i++ {
		owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
		require.NoError(t, err)
		run, err := entflow.Start(ctx, eng, flow, &loopInput{OrderID: owner.ID})
		require.NoError(t, err)
		ids = append(ids, run.ID.(int))
	}

	// Drain the single-slot nudge the last Start sent — this test is about
	// the claim loop's own back-to-back behavior once it starts draining,
	// isolated from the first-wait-shortening nudge TestNudgeShortensWaitMaterially
	// already covers.
	select {
	case <-eng.Nudges():
	default:
	}

	w, err := New(eng, Options{
		Dialect:      "sqlite",
		Concurrency:  1,
		PollInterval: 3 * time.Second,
		Jitter:       0.1,
	})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- w.Run(runCtx) }()

	waitForRunsDone(t, ctx, client, ids, 1*time.Second)
	elapsed := time.Since(start)
	require.Lessf(t, elapsed, 3*time.Second,
		"claiming %d runs back-to-back must not cost a full poll interval per run; took %s", numRuns, elapsed)

	cancel()
	require.NoError(t, <-done)
	require.EqualValues(t, numRuns, counter.Load())
}
