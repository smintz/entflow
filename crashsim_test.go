package entflow_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/crashpoint"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/ent/schema"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
	"github.com/smintz/entflow/internal/testdata/pgtest"
	"github.com/smintz/entflow/worker"
)

// crashSimDialect is the one dialect the crash-simulation release gate
// certifies (D-35) — named here once so both tiers' harness reports quote
// the identical string.
const crashSimDialect = "postgres"

// processOrderFlowTyped resolves the ProcessOrder crash-simulation fixture
// flow (order_flows.go) from schema.Order{}.Flows() back to its concrete
// generic type, the same discovery path flow.go's own doc comment
// documents and cancelOrderFlowTyped (durablerun_test.go) already
// establishes for CancelOrder.
func processOrderFlowTyped(t *testing.T) *entflow.FlowOf[*schema.ProcessOrderRequest] {
	t.Helper()
	for _, f := range entflow.FlowsOf(schema.Order{}) {
		if f.Name() != "ProcessOrder" {
			continue
		}
		typed, ok := f.(*entflow.FlowOf[*schema.ProcessOrderRequest])
		require.True(t, ok, "ProcessOrder flow is not *entflow.FlowOf[*schema.ProcessOrderRequest], got %T", f)
		return typed
	}
	t.Fatal("schema.Order{}.Flows() does not declare a ProcessOrder flow")
	return nil
}

// crashSimBaseline is the uncrashed run's recorded outcome — the ground
// truth every crashed-and-resumed run in this file is compared against
// EXACTLY, not "close enough" (the plan's own emphasis): terminal run
// state, the owning Order's final field values, and the effect counter's
// final value.
type crashSimBaseline struct {
	terminalState string
	orderStatus   order.Status
	effectCount   int
}

// newCrashSimWorker constructs a worker.Worker restricted to ProcessOrder
// only (worker.Options.Flows) — CancelOrderFlowRun's table is shared with
// CancelOrder, and this file never seeds a CancelOrder run, but pinning
// Flows keeps the claim path honest about which flow it is driving rather
// than relying on CancelOrder simply never having a claimable row. Every
// call site in this file uses w.ClaimOnce directly (never w.Run), so
// PollInterval is irrelevant and left at its zero value.
func newCrashSimWorker(t *testing.T, eng *entflow.Engine) *worker.Worker {
	t.Helper()
	w, err := worker.New(eng, worker.Options{
		Dialect:     crashSimDialect,
		Concurrency: 1,
		Flows:       []string{"ProcessOrder"},
		Context: func(c context.Context) context.Context {
			return entflowfixture.WithViewer(c, entflowfixture.AdminViewer())
		},
	})
	require.NoError(t, err)
	return w
}

// isTerminalRunState reports whether s is one of ProcessOrder's terminal
// states — done, cancelled, or any failed:<step> value (D-27).
func isTerminalRunState(s cancelorderflowrun.State) bool {
	switch s {
	case cancelorderflowrun.StateDone, cancelorderflowrun.StateCancelled:
		return true
	}
	return strings.HasPrefix(string(s), "failed:")
}

// driveToTerminal repeatedly calls w.ClaimOnce until runID's row reaches a
// terminal state, or fails the test after timeout — the "resume with a
// different worker instance" half of every crash-simulation subtest below,
// and the whole mechanism the uncrashed baseline is established with.
func driveToTerminal(t *testing.T, ctx context.Context, w *worker.Worker, client *ent.Client, runID int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		claimed, err := w.ClaimOnce(ctx)
		require.NoError(t, err)

		row, err := client.CancelOrderFlowRun.Get(ctx, runID)
		require.NoError(t, err)
		if isTerminalRunState(row.State) {
			return
		}
		if !claimed {
			if time.Now().After(deadline) {
				t.Fatalf("run %d did not reach a terminal state within %s (state=%q, current_step=%q)", runID, timeout, row.State, row.CurrentStep)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// advanceExactClaims calls w.ClaimOnce exactly n times, requiring each one
// to succeed — used to drive a run through every step strictly BEFORE the
// one a subtest is about to install a crash-point hook for, so the
// pre-claim snapshot below reflects "immediately before this specific
// claim", not "immediately after the run was created".
func advanceExactClaims(t *testing.T, ctx context.Context, w *worker.Worker, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		claimed, err := w.ClaimOnce(ctx)
		require.NoError(t, err)
		require.True(t, claimed, "expected claim %d/%d to succeed while advancing to the target step", i+1, n)
	}
}

// stepIndex returns the 0-based position of step in order, or -1 if step
// is not present.
func stepIndex(order []string, step string) int {
	for i, s := range order {
		if s == step {
			return i
		}
	}
	return -1
}

// parseCrashPointName splits a Matrix-produced name back into its target
// step and boundary — the inverse of crashpoint.Name/crashpoint.PreClaimName,
// kept local to this test file rather than exported from crashpoint since
// only the test harness ever needs to go from name back to step (dbstep.go
// only ever goes the other direction).
func parseCrashPointName(name string) (step string, isPreClaim bool) {
	if name == crashpoint.PreClaimName {
		return "", true
	}
	idx := strings.LastIndex(name, ":")
	if idx < 0 {
		return name, false
	}
	return name[:idx], false
}

// TestCrashSimTier1 is tier 1 of the crash-simulation release gate
// (D-52/TEST-01/TEST-02): fast, in-process, deterministic, and the bulk of
// the crash-point matrix. For every name crashpoint.Matrix enumerates from
// ProcessOrder's own step graph (D-56 — never hand-listed), a subtest
// drives the run to immediately before that crash point, installs a hook
// that aborts the claim exactly as a real crash would, asserts D-55's
// paired-absence claim, then resumes the SAME run with a DIFFERENT worker
// instance and asserts its outcome equals the uncrashed baseline exactly —
// including the effect counter, which is what catches a run that
// duplicated an effect and reached the right state anyway (a check
// terminal-state equality alone would wave through).
func TestCrashSimTier1(t *testing.T) {
	pgtest.SkipUnlessDockerAvailable(t)
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())

	processOrder := processOrderFlowTyped(t)
	stepOrder, err := processOrder.StepOrder()
	require.NoError(t, err)
	require.NotEmpty(t, stepOrder, "ProcessOrder must declare at least one executable DB step")

	matrix := crashpoint.Matrix(processOrder.Name(), stepOrder)
	// Guard against a regression that silently empties the matrix — a
	// suite ranging over zero subtests reports zero failures, which is
	// exactly the "the harness passed" false claim T-02-20 forbids.
	require.NotEmpty(t, matrix, "crash-point matrix must never be empty")
	require.GreaterOrEqual(t, len(matrix), len(stepOrder),
		"crash-point matrix must enumerate at least one crash point per declared step")

	client := pgtest.Start(t)
	store := entflowfixture.New(client)

	// --- Establish the baseline: an uncrashed run, the exact-equality
	// ground truth every crashed-and-resumed run below is compared
	// against. Its own subtest so a broken fixture (e.g. a duplicated
	// effect inside a step closure) is reported as a discrete subtest
	// failure, not merely a whole-suite abort before any subtest runs. ---
	var baseline crashSimBaseline
	t.Run("baseline", func(t *testing.T) {
		eng := entflow.NewEngine()
		require.NoError(t, eng.Register(processOrder, store))

		owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
		require.NoError(t, err)
		run, err := entflow.Start(ctx, eng, processOrder, &schema.ProcessOrderRequest{OrderID: owner.ID})
		require.NoError(t, err)

		w := newCrashSimWorker(t, eng)
		driveToTerminal(t, ctx, w, client, run.ID.(int), 10*time.Second)

		finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
		require.NoError(t, err)
		finalOrder, err := client.Order.Get(ctx, owner.ID)
		require.NoError(t, err)

		baseline = crashSimBaseline{
			terminalState: string(finalRun.State),
			orderStatus:   finalOrder.Status,
			effectCount:   finalOrder.EffectCount,
		}
		require.Equal(t, string(cancelorderflowrun.StateDone), baseline.terminalState, "an uncrashed ProcessOrder run must reach done")
		require.Equal(t, order.StatusShipped, baseline.orderStatus, "an uncrashed ProcessOrder run must reach the last step's transition")
		require.Equal(t, len(stepOrder), baseline.effectCount, "an uncrashed ProcessOrder run must apply each step's effect exactly once")
	})

	executed := 0
	for _, name := range matrix {
		name := name
		t.Run(name, func(t *testing.T) {
			executed++

			step, isPreClaim := parseCrashPointName(name)
			var priorClaims int
			if isPreClaim {
				priorClaims = 0
			} else {
				idx := stepIndex(stepOrder, step)
				require.GreaterOrEqual(t, idx, 0, "crash point %q names step %q, not found in the flow's step order", name, step)
				priorClaims = idx
			}

			eng := entflow.NewEngine()
			require.NoError(t, eng.Register(processOrder, store))

			owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
			require.NoError(t, err)
			run, err := entflow.Start(ctx, eng, processOrder, &schema.ProcessOrderRequest{OrderID: owner.ID})
			require.NoError(t, err)
			runID := run.ID.(int)

			w1 := newCrashSimWorker(t, eng)
			advanceExactClaims(t, ctx, w1, priorClaims)

			preRun, err := client.CancelOrderFlowRun.Get(ctx, runID)
			require.NoError(t, err)
			preOrder, err := client.Order.Get(ctx, owner.ID)
			require.NoError(t, err)

			uninstall := crashpoint.Install(name, func() error {
				return fmt.Errorf("crashsim: simulated crash at %s", name)
			})
			claimed, claimErr := w1.ClaimOnce(ctx)
			uninstall()
			require.False(t, claimed, "a claim that hit a simulated crash point must never report success")
			require.Error(t, claimErr, "a claim that hit a simulated crash point must surface the simulated crash")

			postRun, err := client.CancelOrderFlowRun.Get(ctx, runID)
			require.NoError(t, err)
			postOrder, err := client.Order.Get(ctx, owner.ID)
			require.NoError(t, err)

			// D-55's paired-absence claim, expressed as a single combined
			// condition so a mixed state fails loudly with both facts in
			// the message rather than tripping one check and passing
			// another unrelated one: the step's effect (the owning
			// Order's observable fields) and its progress record (the
			// run's current_step/state/results) are either both present
			// or both absent.
			effectPresent := postOrder.Status != preOrder.Status || postOrder.EffectCount != preOrder.EffectCount
			progressPresent := postRun.CurrentStep != preRun.CurrentStep ||
				postRun.State != preRun.State ||
				len(postRun.Results) != len(preRun.Results)
			require.Equal(t, effectPresent, progressPresent,
				"crash point %q: step effect present=%v, progress record present=%v — never one without the other (D-55). order before=%+v after=%+v; run before current_step=%q state=%q, after current_step=%q state=%q",
				name, effectPresent, progressPresent, preOrder, postOrder, preRun.CurrentStep, preRun.State, postRun.CurrentStep, postRun.State)

			// Every boundary crashpoint.Matrix enumerates for this claim
			// path is strictly pre-commit (worker/dbstep.go never calls
			// crashpoint.At after tx.Commit()), so a simulated crash at
			// ANY of them — including the before-commit boundary
			// immediately adjacent to the real commit — must resolve to
			// the wholly-absent side of the pair, never the wholly-present
			// one: the whole point of D-30's one-transaction-per-claim
			// design is that there is no code path between "nothing
			// committed" and "everything for this step committed".
			require.False(t, effectPresent, "crash point %q: a pre-commit crash must leave the step's effect completely absent; order before=%+v after=%+v", name, preOrder, postOrder)
			require.False(t, progressPresent, "crash point %q: a pre-commit crash must leave the run's progress record completely unchanged; run before current_step=%q state=%q after current_step=%q state=%q", name, preRun.CurrentStep, preRun.State, postRun.CurrentStep, postRun.State)

			// Resume with a DIFFERENT worker instance — the point being
			// "any worker can finish what another worker started", not
			// merely "the same worker can retry its own claim".
			w2 := newCrashSimWorker(t, eng)
			driveToTerminal(t, ctx, w2, client, runID, 10*time.Second)

			finalRun, err := client.CancelOrderFlowRun.Get(ctx, runID)
			require.NoError(t, err)
			finalOrder, err := client.Order.Get(ctx, owner.ID)
			require.NoError(t, err)

			require.Equal(t, baseline.terminalState, string(finalRun.State),
				"crash point %q: resumed run's terminal state must equal the uncrashed baseline exactly", name)
			require.Equal(t, baseline.orderStatus, finalOrder.Status,
				"crash point %q: resumed run's owning Order must equal the uncrashed baseline exactly", name)
			require.Equal(t, baseline.effectCount, finalOrder.EffectCount,
				"crash point %q: resumed run's effect counter must equal the uncrashed baseline EXACTLY, not merely 'at least' — a duplicated effect that still reached the right terminal state must be caught here (D-55)", name)
		})
	}

	// The harness's own output is part of the deliverable (TEST-02): a
	// reader of CI output must be able to tell what this run certified
	// without reading the source. This tier never skips once Docker is
	// available — pgtest.SkipUnlessDockerAvailable above already turned a
	// Docker-unavailable environment into either a loud skip or, under
	// ENTFLOW_REQUIRE_CRASHSIM=1, a failure before this point is ever
	// reached.
	t.Logf("crash-simulation harness report | tier: tier-1 (in-process logical crash points) | dialect: %s | flow: %s | crash points enumerated: %d | executed: %d | skipped: 0 | this run certifies %s only, not MySQL or SQLite",
		crashSimDialect, processOrder.Name(), len(matrix), executed, crashSimDialect)
	require.NotZero(t, executed, "a harness run that executed zero crash points must fail, never report success")
	require.Equal(t, len(matrix), executed, "every enumerated crash point must have been exercised")
}
