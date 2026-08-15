package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"

	"github.com/smintz/entflow"
)

// Order is the fixture entity used to prove the Phase 1 walking skeleton:
// a schema-declared flow (order_flows.go) mutates this entity's status field
// through a real, caller-supplied transaction. The status enum carries a
// Transitions annotation (SM-01/D-14) — declaration-only in Phase 1, an
// enforced hook in Phase 6.
type Order struct {
	ent.Schema
}

// Fields of the Order.
func (Order) Fields() []ent.Field {
	return []ent.Field{
		field.String("payment_intent_id").
			Optional(),
		// effect_count is plan 02-08's D-55 side-effect counter: an integer
		// column on Order, not a dedicated table, so a duplicated effect
		// (a step's closure applied twice across a crash and resume) is a
		// wrong count and the counter is atomic with the effect it counts
		// by construction — every DB step of the ProcessOrder fixture flow
		// (order_flows.go) increments it inside the step's own
		// transaction, the same write that mutates status, rather than a
		// second write that could itself be lost independently of the
		// effect it is meant to be counting.
		field.Int("effect_count").
			Default(0),
		field.Enum("status").
			Values("draft", "pending", "paid", "shipped", "delivered", "cancelled").
			Default("draft").
			Annotations(entflow.Transitions(map[string][]string{
				"draft":   {"pending", "cancelled"},
				"pending": {"paid", "cancelled"},
				"paid":    {"shipped", "cancelled"},
				"shipped": {"delivered"},
			})),
	}
}

// Edges of the Order.
func (Order) Edges() []ent.Edge {
	return []ent.Edge{
		// runs is the inverse of CancelOrderFlowRun's "owner" edge — the
		// D-26 lineage edge a run row carries back to the aggregate it
		// belongs to.
		edge.From("runs", CancelOrderFlowRun.Type).
			Ref("owner"),
	}
}

// Hooks forces ent's codegen to route schema-stitching through the separate
// ent/runtime package instead of embedding it directly in the generated root
// ent package. ent's codegen decides which format to use purely by counting
// Hooks/Policy/Interceptors declared on the schema (entc/gen/template/runtime.tmpl)
// — it has no way to know that order_flows.go's Flows() method also imports
// generated types. Without at least one Hook here, ent emits the
// schema-stitching init() directly into ent/runtime.go in the root package,
// which imports this schema package — creating a genuine, unresolvable
// import cycle once schema also imports the generated ent package for
// Flows()'s step closures (schema -> ent, ent -> schema).
//
// This hook is an intentional, effect-free passthrough — it does not change
// mutation behavior. It exists solely to trigger ent's own cyclic-import
// avoidance (the same "ent/runtime" split entflow.md §3.1's commitment 4
// describes), which is otherwise never triggered by a schema method ent's
// codegen doesn't recognize.
func (Order) Hooks() []ent.Hook {
	return []ent.Hook{
		func(next ent.Mutator) ent.Mutator {
			return next
		},
	}
}
