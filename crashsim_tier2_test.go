package entflow_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/crashpoint"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/ent/schema"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
	"github.com/smintz/entflow/internal/testdata/pgtest"
)

// tier2Case names one of the small, explicitly chosen crash points tier 2
// terminates a real subprocess at — see TestCrashSimTier2's doc comment for
// why these three and not the full matrix (that is tier 1's job).
type tier2Case struct {
	// label is a human-readable description of what this crash point
	// represents, used only in the subtest name and failure messages.
	label string
	// crashPoint is the crashpoint.Name(...)/crashpoint.PreClaimName value
	// the crashworker subprocess is armed with.
	crashPoint string
	// stepName is the DB step this crash point targets — used to compute
	// how many uncrashed prior claims must run before arming.
	stepName string
}

// tier2CrashworkerEnv extends crashworkerEnv (topology_test.go) with the
// two environment variables that arm a crash point (internal/testdata/
// crashworker/main.go's envCrashPoint/envSentinelPath pair).
func tier2CrashworkerEnv(dsn, crashPointName, sentinelPath string) []string {
	return crashworkerEnv(dsn,
		"ENTFLOW_CRASHWORKER_FLOWS=ProcessOrder",
		"ENTFLOW_CRASHWORKER_CONCURRENCY=1",
		"ENTFLOW_CRASHWORKER_POLL_INTERVAL=20ms",
		"ENTFLOW_CRASHWORKER_CRASH_POINT="+crashPointName,
		"ENTFLOW_CRASHWORKER_SENTINEL_PATH="+sentinelPath,
	)
}

// waitForSentinelAndUnchangedRun blocks until BOTH of D-53's synchronization
// facts hold — the sentinel file exists, and the run's own committed state
// (current_step, state) still equals what it was immediately before this
// claim was attempted — or fails the test naming whichever fact was still
// missing once the deadline elapses. Synchronization is entirely on these
// two observed facts via assert.Eventually's own polling (the sanctioned
// pattern this module already uses for state-driven waits, e.g.
// topology_test.go's waitForRunStates) — never a fixed sleep as the
// synchronization primitive itself.
//
// The run-state check matters as much as the sentinel: the sentinel alone
// only proves the crashworker PROCESS reached the right line of code; the
// run-state check proves the DATABASE has not raced ahead of it (the
// claimed row's committed state — visible to this parent connection under
// ordinary MVCC read-committed semantics — is still exactly the pre-claim
// snapshot, since nothing this claim's still-open, uncommitted transaction
// wrote is visible to any other connection until it commits, which it
// never will).
func waitForSentinelAndUnchangedRun(t *testing.T, ctx context.Context, client *ent.Client, runID int, sentinelPath string, wantCurrentStep string, wantState cancelorderflowrun.State, timeout time.Duration) {
	t.Helper()

	var lastSentinelExists, lastDBUnchanged bool
	var lastCurrentStep string
	var lastState cancelorderflowrun.State

	ok := assert.Eventually(t, func() bool {
		_, statErr := os.Stat(sentinelPath)
		lastSentinelExists = statErr == nil

		row, err := client.CancelOrderFlowRun.Get(ctx, runID)
		if err != nil {
			return false
		}
		lastCurrentStep = row.CurrentStep
		lastState = row.State
		lastDBUnchanged = row.CurrentStep == wantCurrentStep && row.State == wantState

		return lastSentinelExists && lastDBUnchanged
	}, timeout, 10*time.Millisecond)

	if !ok {
		t.Fatalf("timed out waiting for tier-2 synchronization: sentinel file exists=%v, database state unchanged=%v (current_step=%q state=%q, want current_step=%q state=%q)",
			lastSentinelExists, lastDBUnchanged, lastCurrentStep, lastState, wantCurrentStep, wantState)
	}
}

// TestCrashSimTier2 is tier 2 of the crash-simulation release gate
// (D-52/D-53/TEST-01/TEST-02) — fewer cases than tier 1, but the only tier
// that tests the claim the release gate actually makes: that a REAL worker
// process, terminated by a REAL uncatchable signal against a REAL Postgres,
// leaves no step effect ever committed without its progress record, and
// that a different worker resumes it to the same terminal state.
//
// The chosen crash points are deliberately small and explicit, not the
// full graph-derived matrix (that exhaustive enumeration is tier 1's job,
// D-56): the first step's post-effect-pre-commit boundary, the last step's
// post-effect-pre-commit boundary, and one mid-flow boundary — enough to
// prove the claim holds at the start, middle, and end of a multi-step run
// without paying a real-subprocess-kill's cost for every one of tier 1's
// 19 crash points.
func TestCrashSimTier2(t *testing.T) {
	pgtest.SkipUnlessDockerAvailable(t)
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())

	processOrder := processOrderFlowTyped(t)
	stepOrder, err := processOrder.StepOrder()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(stepOrder), 3, "the fixture flow must have at least 3 steps for a genuine first/mid/last split")

	clients, dsn := pgtest.StartNWithDSN(t, 1)
	client := clients[0]
	store := entflowfixture.New(client)

	var baseline crashSimBaseline
	t.Run("baseline", func(t *testing.T) {
		baseline = establishCrashSimBaseline(t, ctx, client, store, processOrder)
		require.Equal(t, len(stepOrder), baseline.effectCount, "an uncrashed ProcessOrder run must apply each step's effect exactly once")
	})

	cases := []tier2Case{
		{
			label:      "first step, post-effect-pre-commit",
			crashPoint: crashpoint.Name(stepOrder[0], crashpoint.AfterStep),
			stepName:   stepOrder[0],
		},
		{
			label:      "mid-flow boundary",
			crashPoint: crashpoint.Name(stepOrder[1], crashpoint.BeforeCommit),
			stepName:   stepOrder[1],
		},
		{
			label:      "last step, post-effect-pre-commit",
			crashPoint: crashpoint.Name(stepOrder[len(stepOrder)-1], crashpoint.AfterStep),
			stepName:   stepOrder[len(stepOrder)-1],
		},
	}

	executed := 0
	for _, c := range cases {
		c := c
		t.Run(c.label+" ("+c.crashPoint+")", func(t *testing.T) {
			executed++
			priorClaims := stepIndex(stepOrder, c.stepName)
			require.GreaterOrEqual(t, priorClaims, 0)

			owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
			require.NoError(t, err)

			// Seed and advance the run through every step strictly before
			// c.stepName using an ordinary in-process worker — no crash
			// point armed yet.
			eng := entflow.NewEngine()
			require.NoError(t, eng.Register(processOrder, store))
			run, err := entflow.Start(ctx, eng, processOrder, &schema.ProcessOrderRequest{OrderID: owner.ID})
			require.NoError(t, err)
			runID := run.ID.(int)

			seedWorker := newCrashSimWorker(t, eng)
			advanceExactClaims(t, ctx, seedWorker, priorClaims)

			preRun, err := client.CancelOrderFlowRun.Get(ctx, runID)
			require.NoError(t, err)
			preOrder, err := client.Order.Get(ctx, owner.ID)
			require.NoError(t, err)

			sentinelPath := filepath.Join(t.TempDir(), "sentinel")

			cmd := exec.Command(crashworkerBinPath)
			cmd.Env = tier2CrashworkerEnv(dsn, c.crashPoint, sentinelPath)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			require.NoError(t, cmd.Start())
			// Cleanup is a safety net only: the happy path below always
			// kills and Waits on cmd itself. If an assertion fails before
			// that point, this still reaps the process rather than leaking
			// it into later subtests sharing the same container/schema.
			t.Cleanup(func() {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})

			waitForSentinelAndUnchangedRun(t, ctx, client, runID, sentinelPath,
				preRun.CurrentStep, preRun.State, 15*time.Second)

			require.NoError(t, cmd.Process.Signal(syscall.SIGKILL))
			waitErr := cmd.Wait()
			require.Error(t, waitErr, "a SIGKILLed process must report a non-nil Wait error")

			// Assert from a FRESH connection (client is a fresh *sql.DB
			// pool the crashworker subprocess never shared) that D-55's
			// paired-absence claim holds after a REAL process kill: the
			// step's effect and its progress record are either both
			// present or both absent, never one without the other.
			postRun, err := client.CancelOrderFlowRun.Get(ctx, runID)
			require.NoError(t, err)
			postOrder, err := client.Order.Get(ctx, owner.ID)
			require.NoError(t, err)

			effectPresent := postOrder.Status != preOrder.Status || postOrder.EffectCount != preOrder.EffectCount
			progressPresent := postRun.CurrentStep != preRun.CurrentStep ||
				postRun.State != preRun.State ||
				len(postRun.Results) != len(preRun.Results)
			require.Equal(t, effectPresent, progressPresent,
				"crash point %q: step effect present=%v, progress record present=%v — never one without the other (D-55). order before=%+v after=%+v; run before current_step=%q state=%q, after current_step=%q state=%q",
				c.crashPoint, effectPresent, progressPresent, preOrder, postOrder, preRun.CurrentStep, preRun.State, postRun.CurrentStep, postRun.State)
			require.False(t, effectPresent, "crash point %q: a real process kill strictly before commit must leave the step's effect completely absent; order before=%+v after=%+v", c.crashPoint, preOrder, postOrder)
			require.False(t, progressPresent, "crash point %q: a real process kill strictly before commit must leave the run's progress record completely unchanged; run before current_step=%q state=%q after current_step=%q state=%q", c.crashPoint, preRun.CurrentStep, preRun.State, postRun.CurrentStep, postRun.State)

			// A DIFFERENT worker — in-process this time, the point being
			// "any worker resumes what another worker, in another
			// process, left behind" — drives the run to completion.
			resumeWorker := newCrashSimWorker(t, eng)
			driveToTerminal(t, ctx, resumeWorker, client, runID, 10*time.Second)

			finalRun, err := client.CancelOrderFlowRun.Get(ctx, runID)
			require.NoError(t, err)
			finalOrder, err := client.Order.Get(ctx, owner.ID)
			require.NoError(t, err)

			require.Equal(t, baseline.terminalState, string(finalRun.State),
				"crash point %q: resumed run's terminal state must equal the uncrashed baseline exactly", c.crashPoint)
			require.Equal(t, baseline.orderStatus, finalOrder.Status,
				"crash point %q: resumed run's owning Order must equal the uncrashed baseline exactly", c.crashPoint)
			require.Equal(t, baseline.effectCount, finalOrder.EffectCount,
				"crash point %q: resumed run's effect counter must equal the uncrashed baseline EXACTLY (D-55)", c.crashPoint)
		})
	}

	// The harness's own output is part of the deliverable (TEST-02): name
	// which tiers ran, against which dialect, how many crash points were
	// enumerated and executed per tier, how many were skipped and why, and
	// state explicitly what the gate does and does not certify. A run in
	// which zero crash points executed must fail, not report success.
	t.Logf("crash-simulation harness report | tier: tier-2 (real subprocess SIGKILL against real Postgres) | dialect: %s | flow: %s | crash points enumerated: %d | executed: %d | skipped: 0 | this gate certifies PostgreSQL only — it does NOT certify MySQL or SQLite",
		crashSimDialect, processOrder.Name(), len(cases), executed)
	require.NotZero(t, executed, "a tier-2 run that executed zero crash points must fail, never report success")
	require.Equal(t, len(cases), executed, "every chosen tier-2 crash point must have been exercised")
}
