package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"

	"github.com/smintz/entflow"
)

// CancelOrderFlowRun is the hand-written run entity for Order's CancelOrder
// flow (D-23) — the shape Phase 5's schemast-based entity injection must
// reproduce. Its Mixin() derives entflow.RunMixin() from the flow's own
// declared step graph (Order{}.Flows()[0]), so the `state` enum this table
// actually stores and entflow.RunStates(f) are the same list by
// construction, never a hand-copied one.
type CancelOrderFlowRun struct {
	ent.Schema
}

// Mixin of the CancelOrderFlowRun.
func (CancelOrderFlowRun) Mixin() []ent.Mixin {
	return []ent.Mixin{
		entflow.RunMixin(Order{}.Flows()[0]),
	}
}

// Edges of the CancelOrderFlowRun.
func (CancelOrderFlowRun) Edges() []ent.Edge {
	return []ent.Edge{
		// owner is the D-26 lineage edge back to the Order this run
		// belongs to. Every run row has exactly one owning aggregate row.
		edge.To("owner", Order.Type).
			Unique().
			Required(),
	}
}

// Policy is intentionally left undeclared here — plan 02-05 adds
// DenyStatusEscalation and the generated transitions-enforcing hook.
