package schema

import (
	"context"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/order"
)

// CancelOrderRequest is the input to the CancelOrder flow.
type CancelOrderRequest struct {
	OrderID int
}

// Flows declares the flows owned by Order. This is the worked example from
// entflow.md §3.1/§3.4, trimmed to the single DB step this walking skeleton
// proves end-to-end — the Activity and Emit steps described in the design
// document arrive in Phase 3/4 alongside their execution semantics.
func (Order) Flows() []entflow.Flow {
	f := entflow.New[*CancelOrderRequest]("CancelOrder")
	entflow.UpdateSelf(f, "cancel", func(ctx context.Context, tx *ent.Tx, in *CancelOrderRequest) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).
			SetStatus(order.StatusCancelled).
			Save(ctx)
	}, entflow.Transition("cancelled"))
	return []entflow.Flow{f}
}
