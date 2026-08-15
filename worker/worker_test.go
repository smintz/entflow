package worker_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
	"github.com/smintz/entflow/internal/testdata/pgtest"
	"github.com/smintz/entflow/worker"
)

// TestNewRefusesUnsupportedDialect proves DUR-08/D-37: an unrecognized
// dialect string is refused at construction, naming both the offending
// dialect and the documentation path — never lazily deferred to the first
// claim.
func TestNewRefusesUnsupportedDialect(t *testing.T) {
	eng := entflow.NewEngine()
	w, err := worker.New(eng, worker.Options{Dialect: "cockroachdb"})
	require.Nil(t, w)
	require.ErrorIs(t, err, worker.ErrUnsupportedDialect)
	require.Contains(t, err.Error(), "cockroachdb")
	require.Contains(t, err.Error(), "docs/dialects.md")
}

// TestNewRefusesSQLiteConcurrency proves D-36: SQLite with Concurrency
// above one is refused at construction rather than silently serialized.
func TestNewRefusesSQLiteConcurrency(t *testing.T) {
	eng := entflow.NewEngine()
	w, err := worker.New(eng, worker.Options{Dialect: "sqlite", Concurrency: 2})
	require.Nil(t, w)
	require.ErrorIs(t, err, worker.ErrSQLiteConcurrency)
}

// TestNewAcceptsSQLiteSingleWorker and TestNewAcceptsPostgresConcurrency
// prove the boundary: SQLite at concurrency 1, and any other ratified
// dialect at higher concurrency, both construct successfully.
func TestNewAcceptsSQLiteSingleWorker(t *testing.T) {
	eng := entflow.NewEngine()
	w, err := worker.New(eng, worker.Options{Dialect: "sqlite", Concurrency: 1})
	require.NoError(t, err)
	require.NotNil(t, w)
}

func TestNewAcceptsPostgresConcurrency(t *testing.T) {
	eng := entflow.NewEngine()
	w, err := worker.New(eng, worker.Options{Dialect: "postgres", Concurrency: 4})
	require.NoError(t, err)
	require.NotNil(t, w)
}

// TestNewValidatesBeforeAnyDatabaseRoundTrip proves New's validation is
// synchronous and side-effect-free: constructing against a bogus dialect (or
// an over-concurrent SQLite configuration) never touches a database — there
// is no database configured at all in this test, so a database round trip
// would surface as a nil-pointer panic or a network-dial error, neither of
// which occurs.
func TestNewValidatesBeforeAnyDatabaseRoundTrip(t *testing.T) {
	eng := entflow.NewEngine() // no flows, no stores, no client anywhere

	_, err := worker.New(eng, worker.Options{Dialect: "not-a-real-dialect"})
	require.Error(t, err)

	_, err = worker.New(eng, worker.Options{Dialect: "sqlite", Concurrency: 8})
	require.Error(t, err)
}

// TestStrategyForDialectMatrix exercises the full D-35 matrix end to end
// through the public entry point.
func TestStrategyForDialectMatrix(t *testing.T) {
	for _, dialect := range []string{"postgres", "postgresql", "mysql", "sqlite", "sqlite3"} {
		t.Run(dialect, func(t *testing.T) {
			strategy, err := worker.StrategyForDialect(dialect)
			require.NoError(t, err)
			require.NotNil(t, strategy)
		})
	}

	_, err := worker.StrategyForDialect("gremlin")
	require.Error(t, err)
	var wrapped error = err
	require.True(t, errors.Is(wrapped, worker.ErrUnsupportedDialect))
	require.True(t, strings.Contains(err.Error(), "gremlin"))
}

// concurrencyInput is the input type for this file's own local single-DB-
// step flows, used only to exercise Worker.Run's N-goroutine claim pool
// against a real Postgres — deliberately independent of
// schema.CancelOrderRequest, mirroring durablerun_test.go's own local-flow
// pattern in the root package.
type concurrencyInput struct {
	OrderID int
}

// newConcurrencyFlow returns a single-DB-step, unconditioned flow named name
// that increments counter and mutates its owning Order row every time its
// step closure actually runs — the side-effect counter TestWorkerConcurrency
// and TestTwoWorkers assert against to prove no run's step closure executes
// more than once.
func newConcurrencyFlow(name string, counter *atomic.Int64) *entflow.FlowOf[*concurrencyInput] {
	f := entflow.New[*concurrencyInput](name,
		entflow.WithOwnerRef(func(in *concurrencyInput) (int, error) {
			return in.OrderID, nil
		}),
	)
	entflow.Step(f, "mutate", func(ctx context.Context, tx *ent.Tx, in *concurrencyInput) (*ent.Order, error) {
		counter.Add(1)
		return tx.Order.UpdateOneID(in.OrderID).SetPaymentIntentID("mutated").Save(ctx)
	})
	return f
}

// newPanickingFlow returns a single-DB-step flow named name whose step
// closure always panics — TestPanickingStep's fixture for D-31's "a bad
// step must not take down the worker" requirement.
func newPanickingFlow(name string) *entflow.FlowOf[*concurrencyInput] {
	f := entflow.New[*concurrencyInput](name,
		entflow.WithOwnerRef(func(in *concurrencyInput) (int, error) {
			return in.OrderID, nil
		}),
	)
	entflow.Step(f, "boom", func(ctx context.Context, tx *ent.Tx, in *concurrencyInput) (*ent.Order, error) {
		panic("newPanickingFlow: deliberate panic for TestPanickingStep")
	})
	return f
}

// waitForDone polls every run named by ids until every one of them has
// reached the run table's done state, or fails the test after timeout.
func waitForDone(t *testing.T, ctx context.Context, client *ent.Client, ids []int, timeout time.Duration) {
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
	}, timeout, 20*time.Millisecond, "not every run reached done within %s", timeout)
}

// TestWorkerConcurrency proves D-31 against a real Postgres: Options.
// Concurrency runs N independent claim goroutines (never one claim
// transaction fanning a batch out), and starting 8 claimable runs at
// Concurrency:4 produces exactly one terminal transition and exactly one
// step-effect mutation per run — never more.
func TestWorkerConcurrency(t *testing.T) {
	pgtest.SkipUnlessDockerAvailable(t)
	ctx := context.Background()
	client := pgtest.Start(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	var counter atomic.Int64
	flow := newConcurrencyFlow("WorkerConcurrency", &counter)
	require.NoError(t, eng.Register(flow, store))

	const numRuns = 8
	runIDs := make([]int, 0, numRuns)
	for i := 0; i < numRuns; i++ {
		owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
		require.NoError(t, err)
		run, err := entflow.Start(ctx, eng, flow, &concurrencyInput{OrderID: owner.ID})
		require.NoError(t, err)
		runIDs = append(runIDs, run.ID.(int))
	}

	w, err := worker.New(eng, worker.Options{
		Dialect:      "postgres",
		Concurrency:  4,
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()

	waitForDone(t, ctx, client, runIDs, 10*time.Second)

	cancel()
	require.NoError(t, <-done)

	require.EqualValues(t, numRuns, counter.Load(),
		"the observed step-effect count must equal the number of runs, never more")

	for _, id := range runIDs {
		row, err := client.CancelOrderFlowRun.Get(ctx, id)
		require.NoError(t, err)
		require.Equal(t, cancelorderflowrun.StateDone, row.State)
	}
}

// TestTwoWorkers proves the same one-terminal-transition-per-run property
// holds across two independently constructed Worker instances — each its
// own *ent.Client/connection pool, mirroring two separate worker processes —
// started concurrently against the same underlying database, with no
// coordination beyond the row lock (DUR-04's user story).
func TestTwoWorkers(t *testing.T) {
	pgtest.SkipUnlessDockerAvailable(t)
	ctx := context.Background()
	clients := pgtest.StartN(t, 2)
	client1, client2 := clients[0], clients[1]

	var counter atomic.Int64
	flow := newConcurrencyFlow("TwoWorkers", &counter)

	eng1 := entflow.NewEngine()
	require.NoError(t, eng1.Register(flow, entflowfixture.New(client1)))
	eng2 := entflow.NewEngine()
	require.NoError(t, eng2.Register(flow, entflowfixture.New(client2)))

	const numRuns = 8
	runIDs := make([]int, 0, numRuns)
	for i := 0; i < numRuns; i++ {
		owner, err := client1.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
		require.NoError(t, err)
		run, err := entflow.Start(ctx, eng1, flow, &concurrencyInput{OrderID: owner.ID})
		require.NoError(t, err)
		runIDs = append(runIDs, run.ID.(int))
	}

	w1, err := worker.New(eng1, worker.Options{Dialect: "postgres", Concurrency: 2, PollInterval: 10 * time.Millisecond})
	require.NoError(t, err)
	w2, err := worker.New(eng2, worker.Options{Dialect: "postgres", Concurrency: 2, PollInterval: 10 * time.Millisecond})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() { done1 <- w1.Run(runCtx) }()
	go func() { done2 <- w2.Run(runCtx) }()

	waitForDone(t, ctx, client1, runIDs, 10*time.Second)

	cancel()
	require.NoError(t, <-done1)
	require.NoError(t, <-done2)

	require.EqualValues(t, numRuns, counter.Load(),
		"two workers against one database must still produce exactly one step effect per run")

	for _, id := range runIDs {
		row, err := client1.CancelOrderFlowRun.Get(ctx, id)
		require.NoError(t, err)
		require.Equal(t, cancelorderflowrun.StateDone, row.State)
	}
}

// TestPanickingStep proves a step closure's panic leaves its run claimable
// (never advanced, never terminally failed) and never terminates Worker.Run
// — the goroutine keeps polling until ctx is cancelled, at which point Run
// still returns cleanly. runStep (exec.go) already converts a step
// closure's panic into a returned *entflow.StepError before it ever reaches
// worker.claimOnceRecovered's own recover boundary; this test proves that
// conversion holds under the new N-goroutine pool, not just the single-
// claimer shape plan 02-02 proved.
func TestPanickingStep(t *testing.T) {
	pgtest.SkipUnlessDockerAvailable(t)
	ctx := context.Background()
	client := pgtest.Start(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	flow := newPanickingFlow("PanickingStep")
	require.NoError(t, eng.Register(flow, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, flow, &concurrencyInput{OrderID: owner.ID})
	require.NoError(t, err)

	w, err := worker.New(eng, worker.Options{
		Dialect:      "postgres",
		Concurrency:  2,
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	runCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()

	// Run must return cleanly (nil) once its context times out — a worker
	// that crashed or deadlocked on the panicking step would either never
	// send on done, or the whole test process would already be dead from an
	// unrecovered panic, per claimOnceRecovered's doc comment.
	require.NoError(t, <-done)

	finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StatePending, finalRun.State,
		"a panicking step must leave its run claimable, not advance it")
	require.Empty(t, finalRun.CurrentStep)
}
