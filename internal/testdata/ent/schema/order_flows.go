package schema

import (
	"context"
	"time"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/order"
)

// CancelOrderRequest is the input to the CancelOrder flow.
type CancelOrderRequest struct {
	OrderID int
}

// RefundResult is the refund Activity's declared output shape, matching
// entflow.md §3.1's worked example exactly.
type RefundResult struct {
	RefundID string
}

// Flows declares the flows owned by Order. This is the worked example from
// entflow.md §3.1 in full: the DB step that transitions the order, an
// Activity guarded by SelfWas("paid") with a fixed backoff retry policy, and
// an Emit fired after the DB step commits. Under D-13, the Activity and Emit
// steps here are declarable and describable but not executable in Phase 1 —
// Exec refuses this flow with ErrRequiresDurableRun until Phase 3/4 ship
// their execution semantics. That refusal, not a working-but-unsafe inline
// path, is the intended Phase 1 demo.
func (Order) Flows() []entflow.Flow {
	f := entflow.New[*CancelOrderRequest]("CancelOrder", entflow.WithOwner("Order"))
	entflow.UpdateSelf(f, "cancel", func(ctx context.Context, tx *ent.Tx, in *CancelOrderRequest) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).
			SetStatus(order.StatusCancelled).
			Save(ctx)
	}, entflow.Transition("cancelled"))
	entflow.Activity(f, "refund",
		func(ctx context.Context, self *ent.Order, att entflow.Attempt) (entflow.JSON[RefundResult], error) {
			// Never executed in Phase 1 (D-13). A real implementation would
			// call an external payment provider here via entflow.Use[T],
			// keyed by att.IdempotencyKey() — see entflow.md §3.1.
			return entflow.NewJSON(RefundResult{RefundID: "re_" + att.IdempotencyKey()}), nil
		},
		entflow.When(entflow.SelfWas("paid")),
		entflow.Retry(entflow.Backoff(5, time.Second, time.Minute)),
	)
	entflow.Emit(f, "order.cancelled", entflow.After("cancel"))
	return []entflow.Flow{f}
}
