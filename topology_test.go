package entflow_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/ent/schema"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
	"github.com/smintz/entflow/internal/testdata/pgtest"
	"github.com/smintz/entflow/worker"
)

// crashworkerBinPath is built once in TestMain and shared by every test in
// this file — building `go build`s cheaply and needs no Docker, so it
// always runs; individual tests still gate on
// pgtest.SkipUnlessDockerAvailable before touching a real database.
var crashworkerBinPath string

// TestMain builds internal/testdata/crashworker with the Go toolchain into a
// throwaway temp directory (never the repo — the sequential_execution
// contract forbids leaving compiled binaries behind) before running this
// file's tests, following the same "build once, report combined output on
// failure" style deps_test.go's runGoListDeps already established for
// external-process invocation in this module.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "entflow-crashworker-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "topology_test: creating temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	crashworkerBinPath = filepath.Join(dir, "crashworker")
	cmd := exec.Command("go", "build", "-o", crashworkerBinPath, "./internal/testdata/crashworker")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "topology_test: building crashworker: %v\n%s\n", err, out)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// waitForRunStates polls every run named by ids until each has reached
// want, or fails the test after timeout.
func waitForRunStates(t *testing.T, ctx context.Context, client *ent.Client, ids []int, want cancelorderflowrun.State, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, id := range ids {
			row, err := client.CancelOrderFlowRun.Get(ctx, id)
			require.NoError(t, err)
			if row.State != want {
				return false
			}
		}
		return true
	}, timeout, 20*time.Millisecond, "not every run reached %q within %s", want, timeout)
}

// crashworkerEnv returns the environment internal/testdata/crashworker
// needs to reach the SAME isolated Postgres schema dsn names, layered over
// the ambient process environment (PATH etc.) rather than replacing it.
func crashworkerEnv(dsn string, extra ...string) []string {
	env := append(os.Environ(), "ENTFLOW_CRASHWORKER_DSN="+dsn)
	return append(env, extra...)
}

// TestTopologyDedicatedBinaryAndInProcessWorkerShareOneDatabase proves
// DUR-09's concurrency edge and the plan's core claim: a dedicated
// crashworker subprocess and an in-process worker — the two call sites of
// the identical worker.New — claim from the SAME Postgres schema with no
// coordination beyond the row lock, and every run reaches done exactly
// once. CancelOrder's own step IS the observed effect (the owning Order's
// status becomes cancelled); D-30's atomic claim-execute-advance transaction
// plus SKIP LOCKED's row-exclusivity (certified in plan 02-03) is what makes
// "exactly once" true here — a dedicated numeric effect counter, for a
// regression-proof version of this same claim, arrives with plan 02-08's
// crash-simulation fixture flow.
func TestTopologyDedicatedBinaryAndInProcessWorkerShareOneDatabase(t *testing.T) {
	pgtest.SkipUnlessDockerAvailable(t)
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())

	clients, dsn := pgtest.StartNWithDSN(t, 1)
	client := clients[0]
	store := entflowfixture.New(client)

	eng := entflow.NewEngine()
	cancelOrder := cancelOrderFlowTyped(t)
	require.NoError(t, eng.Register(cancelOrder, store))

	const numRuns = 6
	type seed struct{ runID, ownerID int }
	seeds := make([]seed, 0, numRuns)
	for i := 0; i < numRuns; i++ {
		owner, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
		require.NoError(t, err)
		run, err := entflow.Start(ctx, eng, cancelOrder, &schema.CancelOrderRequest{OrderID: owner.ID})
		require.NoError(t, err)
		seeds = append(seeds, seed{runID: run.ID.(int), ownerID: owner.ID})
	}
	runIDs := make([]int, len(seeds))
	for i, s := range seeds {
		runIDs[i] = s.runID
	}

	// Call site one: the in-process worker, `go w.Run(ctx)` alongside this
	// test standing in for an API server. Options.Flows is pinned to
	// CancelOrder: since plan 02-08, CancelOrderFlowRun's table is also
	// shared by the ProcessOrder crash-simulation fixture flow (only
	// registered on eng below via cancelOrder, so it is not actually
	// claimable here regardless), but pinning Flows explicitly is what
	// keeps this test's own claim, "CancelOrder's own step IS the observed
	// effect", true by construction rather than by the accident of which
	// flow eng happens to have registered.
	w, err := worker.New(eng, worker.Options{
		Dialect:      "postgres",
		Concurrency:  2,
		PollInterval: 20 * time.Millisecond,
		Flows:        []string{cancelOrder.Name()},
		Context: func(c context.Context) context.Context {
			return entflowfixture.WithViewer(c, entflowfixture.AdminViewer())
		},
	})
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()

	// Call site two: the dedicated crashworker binary, a real OS subprocess
	// against the identical schema. ENTFLOW_CRASHWORKER_FLOWS mirrors the
	// in-process worker's own Flows restriction above — the binary
	// registers every flow schema.Order{}.Flows() declares (its own
	// documented behavior, internal/testdata/crashworker/README.md), but
	// this test only seeds and asserts against CancelOrder runs.
	cmd := exec.Command(crashworkerBinPath)
	cmd.Env = crashworkerEnv(dsn,
		"ENTFLOW_CRASHWORKER_CONCURRENCY=2",
		"ENTFLOW_CRASHWORKER_POLL_INTERVAL=20ms",
		"ENTFLOW_CRASHWORKER_DRAIN_TIMEOUT=2s",
		"ENTFLOW_CRASHWORKER_FLOWS="+cancelOrder.Name(),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Wait()
		}
	})

	waitForRunStates(t, ctx, client, runIDs, cancelorderflowrun.StateDone, 15*time.Second)

	cancel()
	require.NoError(t, <-done)
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	require.NoError(t, cmd.Wait(), "crashworker stderr: %s", stderr.String())

	cancelledCount := 0
	for _, s := range seeds {
		row, err := client.CancelOrderFlowRun.Get(ctx, s.runID)
		require.NoError(t, err)
		require.Equal(t, cancelorderflowrun.StateDone, row.State)

		owner, err := client.Order.Get(ctx, s.ownerID)
		require.NoError(t, err)
		require.Equal(t, order.StatusCancelled, owner.Status)
		cancelledCount++
	}
	require.Equal(t, numRuns, cancelledCount,
		"the observed step-effect count (orders transitioned to cancelled) must equal the run count")
}

// TestTopologyCrashworkerExitsWithinDrainWindowOnTermination proves the
// binary's own honored-signal contract: a SIGTERM drives graceful Shutdown
// within its configured drain window, and the run it may have been
// mid-claim on is left either terminal or claimable — never stuck.
func TestTopologyCrashworkerExitsWithinDrainWindowOnTermination(t *testing.T) {
	pgtest.SkipUnlessDockerAvailable(t)
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())

	clients, dsn := pgtest.StartNWithDSN(t, 1)
	client := clients[0]
	store := entflowfixture.New(client)

	eng := entflow.NewEngine()
	cancelOrder := cancelOrderFlowTyped(t)
	require.NoError(t, eng.Register(cancelOrder, store))

	owner, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
	require.NoError(t, err)
	run, err := entflow.Start(ctx, eng, cancelOrder, &schema.CancelOrderRequest{OrderID: owner.ID})
	require.NoError(t, err)

	const drainTimeout = 2 * time.Second
	cmd := exec.Command(crashworkerBinPath)
	cmd.Env = crashworkerEnv(dsn,
		"ENTFLOW_CRASHWORKER_CONCURRENCY=1",
		"ENTFLOW_CRASHWORKER_POLL_INTERVAL=20ms",
		fmt.Sprintf("ENTFLOW_CRASHWORKER_DRAIN_TIMEOUT=%s", drainTimeout),
		"ENTFLOW_CRASHWORKER_FLOWS="+cancelOrder.Name(),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Wait()
		}
	})

	// Give the (fast, single-step) CancelOrder flow a moment to be claimed
	// and run at least once before terminating the process.
	time.Sleep(150 * time.Millisecond)
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		require.NoError(t, err, "crashworker stderr: %s", stderr.String())
	case <-time.After(drainTimeout + 5*time.Second):
		t.Fatalf("crashworker did not exit within its drain window; stderr: %s", stderr.String())
	}

	row, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	claimableOrTerminal := row.State == cancelorderflowrun.StatePending ||
		row.State == cancelorderflowrun.StateRunning ||
		row.State == cancelorderflowrun.StateDone
	require.True(t, claimableOrTerminal, "run left in state %q, neither claimable nor terminal", row.State)

	if row.State != cancelorderflowrun.StateDone {
		w, err := worker.New(eng, worker.Options{
			Dialect:      "postgres",
			Concurrency:  1,
			PollInterval: 10 * time.Millisecond,
			Context: func(c context.Context) context.Context {
				return entflowfixture.WithViewer(c, entflowfixture.AdminViewer())
			},
		})
		require.NoError(t, err)
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		doneCh := make(chan error, 1)
		go func() { doneCh <- w.Run(runCtx) }()
		waitForRunStates(t, ctx, client, []int{run.ID.(int)}, cancelorderflowrun.StateDone, 5*time.Second)
		cancel()
		require.NoError(t, <-doneCh)
	}

	final, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateDone, final.State)
}
