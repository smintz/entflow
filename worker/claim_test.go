package worker_test

import (
	"context"
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

// claimTable is the entflow.RunTable identity for CancelOrderFlowRun, built
// the same way entflowfixture.RunStore.Table does — claim_test.go exercises
// worker.SkipLockedStrategy directly against a real ent.Tx rather than
// through the full RunStore/Engine seam, since these tests are about the one
// non-portable SQL statement, not the worker's claim-execute-advance loop.
func claimTable() entflow.RunTable {
	return entflow.RunTable{
		Name:             cancelorderflowrun.Table,
		IDColumn:         cancelorderflowrun.FieldID,
		StateColumn:      cancelorderflowrun.FieldState,
		RetryAfterColumn: cancelorderflowrun.FieldRetryAfter,
	}
}

// newPostgresRun creates one CancelOrderFlowRun row (and its owning Order)
// directly through the generated client, bypassing entflow.Start entirely so
// the test can put the row in any state — including every terminal one —
// without needing a real flow execution to get there.
func newPostgresRun(t *testing.T, ctx context.Context, client *ent.Client, state cancelorderflowrun.State, retryAfter *time.Time) *ent.CancelOrderFlowRun {
	t.Helper()
	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)

	create := client.CancelOrderFlowRun.Create().
		SetState(state).
		SetInput([]byte(`{}`)).
		SetOwnerID(owner.ID)
	if retryAfter != nil {
		create = create.SetRetryAfter(*retryAfter)
	}
	run, err := create.Save(ctx)
	require.NoError(t, err)
	return run
}

// beginRawTx opens a *ent.Tx and returns it boxed as both entflow.Tx and
// entflow.RawQuerier — the two interfaces worker/dbstep.go's claimOnce
// asserts a RunStore.BeginTx result against.
func beginRawTx(t *testing.T, ctx context.Context, client *ent.Client) (*ent.Tx, entflow.RawQuerier) {
	t.Helper()
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	q, ok := any(tx).(entflow.RawQuerier)
	require.True(t, ok, "*ent.Tx does not satisfy entflow.RawQuerier — sql/execquery feature not enabled?")
	return tx, q
}

// TestClaimSkipLockedClaimsLowestID proves the certified statement claims
// the lowest-ID claimable run among several, against real Postgres.
func TestClaimSkipLockedClaimsLowestID(t *testing.T) {
	pgtest.SkipUnlessDockerAvailable(t)
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := pgtest.Start(t)

	first := newPostgresRun(t, ctx, client, cancelorderflowrun.StatePending, nil)
	newPostgresRun(t, ctx, client, cancelorderflowrun.StatePending, nil)
	newPostgresRun(t, ctx, client, cancelorderflowrun.StatePending, nil)

	tx, q := beginRawTx(t, ctx, client)
	defer tx.Rollback()

	strategy := worker.SkipLockedStrategy()
	id, ok, err := strategy.Claim(ctx, q, claimTable(), []string{
		string(cancelorderflowrun.StatePending), string(cancelorderflowrun.StateRunning),
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, first.ID, id, "claim must return the lowest-ID claimable run")
}

// TestClaimRetryAfterBoundary proves a run whose retry_after is in the
// future is not claimed, and the same run becomes claimable once that time
// has passed — using the database's own clock, not the test process's.
func TestClaimRetryAfterBoundary(t *testing.T) {
	pgtest.SkipUnlessDockerAvailable(t)
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := pgtest.Start(t)

	future := time.Now().Add(1 * time.Hour)
	run := newPostgresRun(t, ctx, client, cancelorderflowrun.StatePending, &future)

	states := []string{string(cancelorderflowrun.StatePending), string(cancelorderflowrun.StateRunning)}

	t.Run("not claimed while retry_after is in the future", func(t *testing.T) {
		tx, q := beginRawTx(t, ctx, client)
		defer tx.Rollback()

		strategy := worker.SkipLockedStrategy()
		_, ok, err := strategy.Claim(ctx, q, claimTable(), states)
		require.NoError(t, err)
		require.False(t, ok, "a run with a future retry_after must not be claimed")
	})

	past := time.Now().Add(-1 * time.Hour)
	_, err := client.CancelOrderFlowRun.UpdateOneID(run.ID).SetRetryAfter(past).Save(ctx)
	require.NoError(t, err)

	t.Run("claimed once retry_after is in the past", func(t *testing.T) {
		tx, q := beginRawTx(t, ctx, client)
		defer tx.Rollback()

		strategy := worker.SkipLockedStrategy()
		id, ok, err := strategy.Claim(ctx, q, claimTable(), states)
		require.NoError(t, err)
		require.True(t, ok)
		require.EqualValues(t, run.ID, id)
	})
}

// TestClaimTerminalStatesUnclaimable proves every terminal state — done,
// cancelled, and a failed:<step> value — is structurally unclaimable: the
// claim predicate's state list simply never names them (D-32).
func TestClaimTerminalStatesUnclaimable(t *testing.T) {
	terminalStates := []cancelorderflowrun.State{
		cancelorderflowrun.StateDone,
		cancelorderflowrun.StateCancelled,
		cancelorderflowrun.StateFailedCancel,
	}

	for _, state := range terminalStates {
		t.Run(string(state), func(t *testing.T) {
			pgtest.SkipUnlessDockerAvailable(t)
			ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
			client := pgtest.Start(t)

			newPostgresRun(t, ctx, client, state, nil)

			tx, q := beginRawTx(t, ctx, client)
			defer tx.Rollback()

			strategy := worker.SkipLockedStrategy()
			_, ok, err := strategy.Claim(ctx, q, claimTable(), []string{
				string(cancelorderflowrun.StatePending), string(cancelorderflowrun.StateRunning),
			})
			require.NoError(t, err)
			require.False(t, ok, "a run in terminal state %q must never be claimed", state)
		})
	}
}
