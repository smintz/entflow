package entflow_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/entclient"
)

// TestCancelOrderFlowRunCreatesWithOwnerEdge proves DUR-02's persisted
// contract end-to-end against real codegen: a CancelOrderFlowRun row can be
// created through the generated client with state=pending, non-empty
// serialized input bytes, and an edge to its owning Order row, and read
// back with that owner traversable — a run really is an ordinary ent row,
// not a shape only entflow.RunMixin's builder output claims to produce.
func TestCancelOrderFlowRunCreatesWithOwnerEdge(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)

	owner, err := client.Order.Create().
		SetStatus(order.StatusPending).
		Save(ctx)
	require.NoError(t, err)

	run, err := client.CancelOrderFlowRun.Create().
		SetState(cancelorderflowrun.StatePending).
		SetInput([]byte(`{"OrderID":1}`)).
		SetOwner(owner).
		Save(ctx)
	require.NoError(t, err)

	require.Equal(t, cancelorderflowrun.StatePending, run.State)
	require.NotEmpty(t, run.Input)

	got, err := client.CancelOrderFlowRun.Query().
		Where(cancelorderflowrun.IDEQ(run.ID)).
		WithOwner().
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.Edges.Owner)
	require.Equal(t, owner.ID, got.Edges.Owner.ID)
}
