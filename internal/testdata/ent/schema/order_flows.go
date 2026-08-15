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

// ProcessOrderRequest is the input to the ProcessOrder flow — plan 02-08's
// multi-DB-step crash-simulation fixture. CancelOrder has exactly one
// executable DB step, which makes "crash at every step boundary" close to
// vacuous; ProcessOrder exists solely to give the harness a flow with
// enough steps for the graph-derived crash-point matrix (D-56) to be a
// meaningful, non-trivial enumeration.
type ProcessOrderRequest struct {
	OrderID int
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
	f := entflow.New[*CancelOrderRequest]("CancelOrder", entflow.WithOwner("Order"),
		entflow.WithOwnerRef(func(in *CancelOrderRequest) (int, error) {
			return in.OrderID, nil
		}),
		entflow.WithSelfLoader(func(ctx context.Context, tx *ent.Tx, in *CancelOrderRequest) (*ent.Order, error) {
			return tx.Order.Get(ctx, in.OrderID)
		}),
	)
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

	p := processOrderFlow()

	// CancelOrderFlowRun's Mixin() (cancelorderflowrun.go) derives its
	// `state` enum from EVERY flow returned here, not just f — a run table
	// shared by two flows needs a failed:<step> value for both flows'
	// steps. Keeping both flows in one Flows() slice is what lets
	// entflow.RunMixin(Order{}.Flows()...) pick up ProcessOrder's steps
	// automatically the moment they are declared, with no separate wiring.
	return []entflow.Flow{f, p}
}

// processOrderFlow declares ProcessOrder: three DB steps that each mutate
// Order's status and increment its effect_count column (order.go), the
// D-55 side-effect counter every crash-and-resume subtest asserts against
// exactly. It shares CancelOrderFlowRun's table via the same
// entflowfixture.RunStore both flows are registered against — plan 02-08's
// own decision (see order.go's effect_count doc comment) is a column on
// Order, not a dedicated run table, so no new run entity is needed here.
//
// The three steps are declared out of dependency order ("ship" first in
// source, "reserve" last) and wired back into the correct execution order
// purely through After edges: reserve -> charge -> ship. This is
// deliberate, not sloppy — a topoOrder implementation that silently fell
// back to declaration order when it should be resolving After edges would
// execute these three steps in the WRONG order and this flow would still
// look plausible; declaring them out of order is what makes the ordering
// non-trivial enough to actually exercise topoOrder's dependency
// resolution (D-56's "the matrix is enumerated from the flow's step graph"
// requires that graph to be a real graph, not a list that happens to
// already be sorted).
func processOrderFlow() entflow.Flow {
	f := entflow.New[*ProcessOrderRequest]("ProcessOrder", entflow.WithOwner("Order"),
		entflow.WithOwnerRef(func(in *ProcessOrderRequest) (int, error) {
			return in.OrderID, nil
		}),
	)
	entflow.UpdateSelf(f, "ship", func(ctx context.Context, tx *ent.Tx, in *ProcessOrderRequest) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).
			SetStatus(order.StatusShipped).
			AddEffectCount(1).
			Save(ctx)
	}, entflow.Transition("shipped"), entflow.After("charge"))
	entflow.UpdateSelf(f, "reserve", func(ctx context.Context, tx *ent.Tx, in *ProcessOrderRequest) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).
			SetStatus(order.StatusPending).
			AddEffectCount(1).
			Save(ctx)
	}, entflow.Transition("pending"))
	entflow.UpdateSelf(f, "charge", func(ctx context.Context, tx *ent.Tx, in *ProcessOrderRequest) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).
			SetStatus(order.StatusPaid).
			AddEffectCount(1).
			Save(ctx)
	}, entflow.Transition("paid"), entflow.After("reserve"))
	return f
}
