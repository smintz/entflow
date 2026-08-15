package entflow_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/ent/schema"
	"github.com/smintz/entflow/internal/testdata/entclient"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
	"github.com/smintz/entflow/worker"
)

// TestRunQueryDeniedWithNoViewer proves OPS-02's Query-side first rule
// (CancelOrderFlowRun's Policy(), internal/testdata/ent/schema/
// cancelorderflowrun.go): a context carrying no entflowfixture.Viewer is
// denied outright, never silently handed zero rows that would be
// indistinguishable from "this order genuinely has no runs".
func TestRunQueryDeniedWithNoViewer(t *testing.T) {
	client := entclient.New(t)
	adminCtx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())

	owner, err := client.Order.Create().SetStatus(order.StatusPending).Save(adminCtx)
	require.NoError(t, err)
	_, err = client.CancelOrderFlowRun.Create().
		SetState(cancelorderflowrun.StatePending).
		SetInput([]byte(`{}`)).
		SetOwner(owner).
		Save(adminCtx)
	require.NoError(t, err)

	_, err = client.CancelOrderFlowRun.Query().All(context.Background())
	require.Error(t, err, "a run query with no viewer in context must be denied, not return rows")
}

// TestRunQueryScopedToOwnerOrAdmin proves OPS-02's Query-side second rule:
// a non-admin viewer sees only runs whose owner edge belongs to their own
// order (Viewer.Subject); an admin viewer sees every run regardless of
// owner.
func TestRunQueryScopedToOwnerOrAdmin(t *testing.T) {
	client := entclient.New(t)
	adminCtx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())

	ownerA, err := client.Order.Create().SetStatus(order.StatusPending).Save(adminCtx)
	require.NoError(t, err)
	ownerB, err := client.Order.Create().SetStatus(order.StatusPending).Save(adminCtx)
	require.NoError(t, err)

	runA, err := client.CancelOrderFlowRun.Create().
		SetState(cancelorderflowrun.StatePending).SetInput([]byte(`{}`)).SetOwner(ownerA).Save(adminCtx)
	require.NoError(t, err)
	runB, err := client.CancelOrderFlowRun.Create().
		SetState(cancelorderflowrun.StatePending).SetInput([]byte(`{}`)).SetOwner(ownerB).Save(adminCtx)
	require.NoError(t, err)

	viewerACtx := entflowfixture.WithViewer(context.Background(), entflowfixture.Viewer{Subject: ownerA.ID})
	seenByA, err := client.CancelOrderFlowRun.Query().All(viewerACtx)
	require.NoError(t, err)
	require.Len(t, seenByA, 1, "a non-admin viewer must see only their own order's runs")
	require.Equal(t, runA.ID, seenByA[0].ID)

	seenByAdmin, err := client.CancelOrderFlowRun.Query().All(adminCtx)
	require.NoError(t, err)
	ids := make([]int, len(seenByAdmin))
	for i, r := range seenByAdmin {
		ids[i] = r.ID
	}
	require.Contains(t, ids, runA.ID, "an admin viewer must see every run")
	require.Contains(t, ids, runB.ID, "an admin viewer must see every run")
}

// TestCancelDeniedForNonOwner proves OPS-02's Mutation-side cancel guard
// (D-43): a viewer who is not the run's owner cannot move it to the
// cancelled state.
func TestCancelDeniedForNonOwner(t *testing.T) {
	client := entclient.New(t)
	adminCtx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())

	owner, err := client.Order.Create().SetStatus(order.StatusPending).Save(adminCtx)
	require.NoError(t, err)
	run, err := client.CancelOrderFlowRun.Create().
		SetState(cancelorderflowrun.StatePending).SetInput([]byte(`{}`)).SetOwner(owner).Save(adminCtx)
	require.NoError(t, err)

	nonOwnerCtx := entflowfixture.WithViewer(context.Background(), entflowfixture.Viewer{Subject: owner.ID + 999})
	_, err = client.CancelOrderFlowRun.UpdateOneID(run.ID).
		SetState(cancelorderflowrun.StateCancelled).
		Save(nonOwnerCtx)
	require.Error(t, err, "a non-owner's cancel mutation must be denied")

	persisted, err := client.CancelOrderFlowRun.Get(adminCtx, run.ID)
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StatePending, persisted.State, "a denied cancel must not change the stored state")
}

// TestWorkerAdvancesRunOnlyWithOptionsContext proves D-45: a worker with no
// Options.Context hook configured has no viewer of its own and is honestly
// denied by CancelOrderFlowRun's Policy() — the same denial an application
// query with no viewer gets, not a silent grant of access the worker was
// never given. Supplying Options.Context to derive an admin-viewer-bearing
// context from the worker's own polling context lets the exact same claim
// succeed.
func TestWorkerAdvancesRunOnlyWithOptionsContext(t *testing.T) {
	client := entclient.New(t)
	adminCtx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	cancelOrder := cancelOrderFlowTyped(t)
	require.NoError(t, eng.Register(cancelOrder, store))

	owner, err := client.Order.Create().SetStatus(order.StatusPaid).Save(adminCtx)
	require.NoError(t, err)
	run, err := entflow.Start(adminCtx, eng, cancelOrder, &schema.CancelOrderRequest{OrderID: owner.ID})
	require.NoError(t, err)

	noHookWorker, err := worker.New(eng, worker.Options{
		Dialect:       "sqlite",
		Concurrency:   1,
		ClaimStrategy: worker.SQLiteStrategy(),
	})
	require.NoError(t, err)

	// No viewer on the plain background context a poll loop would
	// naturally run under with no hook configured.
	_, err = noHookWorker.ClaimOnce(context.Background())
	require.Error(t, err, "a worker with no Options.Context hook must be denied by a privacy-governed run entity")

	hookedWorker, err := worker.New(eng, worker.Options{
		Dialect:       "sqlite",
		Concurrency:   1,
		ClaimStrategy: worker.SQLiteStrategy(),
		Context: func(ctx context.Context) context.Context {
			return entflowfixture.WithViewer(ctx, entflowfixture.AdminViewer())
		},
	})
	require.NoError(t, err)

	claimed, err := hookedWorker.ClaimOnce(context.Background())
	require.NoError(t, err)
	require.True(t, claimed, "the worker's own context, derived through Options.Context, must be able to advance a run")

	persisted, err := client.CancelOrderFlowRun.Get(adminCtx, run.ID.(int))
	require.NoError(t, err)
	// CancelOrder declares exactly one DB step ("cancel"), so a single
	// successful claim carries the run straight to done.
	require.Equal(t, cancelorderflowrun.StateDone, persisted.State)
}
