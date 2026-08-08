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

// TestRequiresDurableRunCancelOrderFlow proves the intended Phase 1 demo:
// the real worked-example CancelOrder flow (entflow.md §3.1, in full, with
// its refund Activity and order.cancelled Emit) is declarable and
// describable, but Exec refuses it with ErrRequiresDurableRun before
// executing anything, and the seeded order's status is left untouched.
//
// This supersedes Plan 01's TestCancelOrderFlow: once this plan's second
// task extended the fixture flow with the Activity and Emit steps from
// entflow.md §3.1, the flow became durable-only per D-13, and the old
// "Exec commits a DB mutation" proof no longer applies to it — a plain
// DB-only flow (this plan's third task's exec_test.go additions) is what
// proves that behavior now.
func TestRequiresDurableRunCancelOrderFlow(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)

	seeded, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
	require.NoError(t, err)

	flows := entflow.FlowsOf(schema.Order{})
	require.Len(t, flows, 1)
	flow, ok := flows[0].(*entflow.FlowOf[*schema.CancelOrderRequest])
	require.True(t, ok, "expected *entflow.FlowOf[*schema.CancelOrderRequest], got %T", flows[0])

	tx, err := client.Tx(ctx)
	require.NoError(t, err)

	err = flow.Exec(ctx, tx, &schema.CancelOrderRequest{OrderID: seeded.ID})
	require.Error(t, err)
	require.ErrorIs(t, err, entflow.ErrRequiresDurableRun)

	require.NoError(t, tx.Rollback())

	got, err := client.Order.Get(ctx, seeded.ID)
	require.NoError(t, err)
	require.Equal(t, order.StatusPaid, got.Status, "no step should have executed")
}
