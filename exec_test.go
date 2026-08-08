package entflow_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/ent/schema"
	"github.com/smintz/entflow/internal/testdata/entclient"
)

// TestCancelOrderFlow proves the Phase 1 walking skeleton end to end: a flow
// declared on a schema via Flows() is retrievable through FlowsOf, executes
// through Exec inside a caller-supplied *ent.Tx, and the resulting status
// change is readable from the database after commit — through the
// NON-transactional client, which is what makes this a real end-to-end proof
// rather than a test of in-memory transaction state.
func TestCancelOrderFlow(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)

	// Seed one Order with status paid.
	seeded, err := client.Order.Create().
		SetStatus(order.StatusPaid).
		Save(ctx)
	require.NoError(t, err)

	// Retrieve the flow declared on the schema via explicit registration
	// (D-06) — no reflection over the schema package.
	flows := entflow.FlowsOf(schema.Order{})
	require.Len(t, flows, 1)
	require.Equal(t, "CancelOrder", flows[0].Name())

	flow, ok := flows[0].(*entflow.FlowOf[*schema.CancelOrderRequest])
	require.True(t, ok, "expected *entflow.FlowOf[*schema.CancelOrderRequest], got %T", flows[0])

	tx, err := client.Tx(ctx)
	require.NoError(t, err)

	err = flow.Exec(ctx, tx, &schema.CancelOrderRequest{OrderID: seeded.ID})
	require.NoError(t, err)

	require.NoError(t, tx.Commit())

	// Re-query through the plain, NON-transactional client — this is the
	// real end-to-end proof that the mutation committed to the database.
	got, err := client.Order.Get(ctx, seeded.ID)
	require.NoError(t, err)
	require.Equal(t, order.StatusCancelled, got.Status)
}
