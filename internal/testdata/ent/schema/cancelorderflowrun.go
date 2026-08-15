package schema

import (
	"context"

	"entgo.io/ent"
	"entgo.io/ent/privacy"
	"entgo.io/ent/schema/edge"

	"github.com/smintz/entflow"
	gen "github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
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

// Policy is an ordinary ent privacy.Policy (OPS-02, D-45) — entflow core
// ships no reusable privacy rule (it cannot name the application's viewer
// type), so this fixture writes its own, exactly the way any real adopter
// would. Query side denies a viewer-less context outright, then scopes
// reads to the viewer's own order (or, for an admin viewer, every run).
// Mutation side allows any mutation carrying the workflow marker
// (entflow.IsWorkflow, D-44) through unconditionally — the worker's own
// Advance/Fail writes, which always originate inside a claim transaction
// the worker owns — then separately gates starting a run (Create) to the
// viewer starting it on their own order, and cancelling one (an Update that
// sets state to the cancelled value) to the run's owner or an admin
// (D-43). Everything else falls through to the final AlwaysDenyRule.
func (CancelOrderFlowRun) Policy() ent.Policy {
	return privacy.Policy{
		Query: privacy.QueryPolicy{
			queryRuleFunc(denyRunQueryWithNoViewer),
			queryRuleFunc(scopeRunQueryToViewer),
		},
		Mutation: privacy.MutationPolicy{
			privacy.MutationRuleFunc(allowWorkflowMutation),
			privacy.MutationRuleFunc(allowAdminMutation),
			privacy.MutationRuleFunc(guardRunMutation),
			privacy.AlwaysDenyRule(),
		},
	}
}

// queryRuleFunc adapts an ordinary function to privacy.QueryRule.
// entgo.io/ent/privacy ships MutationRuleFunc but, absent the "privacy"
// codegen feature flag this fixture deliberately does not enable (Policy()
// needs nothing beyond the base package — RESEARCH.md Pattern 4), no
// equivalent QueryRuleFunc — this is the same one-method adapter shape,
// hand-written.
type queryRuleFunc func(context.Context, ent.Query) error

// EvalQuery implements privacy.QueryRule.
func (f queryRuleFunc) EvalQuery(ctx context.Context, q ent.Query) error {
	return f(ctx, q)
}

// denyRunQueryWithNoViewer is Policy()'s Query-side first rule (OPS-02): a
// context with no entflowfixture.Viewer attached is denied outright, never
// silently returning zero rows — a caller who forgot to supply a viewer
// gets a privacy error, not an empty-looking "no runs" result.
func denyRunQueryWithNoViewer(ctx context.Context, _ ent.Query) error {
	if _, ok := entflowfixture.ViewerFromContext(ctx); !ok {
		return privacy.Denyf("cancelorderflowrun: query denied: no viewer in context")
	}
	return privacy.Skip
}

// scopeRunQueryToViewer is Policy()'s Query-side second rule: an admin
// viewer is allowed through unfiltered; any other viewer's query is scoped,
// in place, to runs whose owner edge belongs to their own order
// (Viewer.Subject) — the standard ent "filter the query, then Allow"
// pattern.
func scopeRunQueryToViewer(ctx context.Context, q ent.Query) error {
	// denyRunQueryWithNoViewer, evaluated first in the chain, already
	// required a viewer to reach here.
	v, _ := entflowfixture.ViewerFromContext(ctx)
	if v.Admin {
		return privacy.Allow
	}
	runQuery, ok := q.(*gen.CancelOrderFlowRunQuery)
	if !ok {
		return privacy.Skip
	}
	runQuery.Where(cancelorderflowrun.HasOwnerWith(order.IDEQ(v.Subject)))
	return privacy.Allow
}

// allowWorkflowMutation is Policy()'s Mutation-side first rule (D-44): any
// mutation carrying the workflow marker originates inside a claim
// transaction the worker itself owns, and is allowed through
// unconditionally — this is what lets the worker's own Advance/Fail writes
// succeed with no application viewer in context at all.
func allowWorkflowMutation(ctx context.Context, _ ent.Mutation) error {
	if entflow.IsWorkflow(ctx) {
		return privacy.Allow
	}
	return privacy.Skip
}

// allowAdminMutation is Policy()'s Mutation-side second rule: an admin
// viewer may perform any mutation on any run, not only the create/cancel
// shapes guardRunMutation below specifically recognizes — the same
// unrestricted-access guarantee scopeRunQueryToViewer already gives an
// admin on the query side.
func allowAdminMutation(ctx context.Context, _ ent.Mutation) error {
	v, ok := entflowfixture.ViewerFromContext(ctx)
	if ok && v.Admin {
		return privacy.Allow
	}
	return privacy.Skip
}

// guardRunMutation is Policy()'s Mutation-side third rule, covering every
// non-admin, non-worker (application-initiated) write to a run: starting
// one (Create) is allowed for the viewer starting it on their own order;
// cancelling one (an Update that sets state to the cancelled value) is
// allowed for the run's owner and denied otherwise (OPS-02, D-43) —
// cancelling means "no further steps", never "abort mid-step", see
// entflow.Cancel's own doc comment. Anything else Skips through to
// Policy()'s final AlwaysDenyRule.
func guardRunMutation(ctx context.Context, m ent.Mutation) error {
	runMut, ok := m.(*gen.CancelOrderFlowRunMutation)
	if !ok {
		return privacy.Skip
	}
	v, hasViewer := entflowfixture.ViewerFromContext(ctx)

	if runMut.Op() == ent.OpCreate {
		if !hasViewer {
			return privacy.Denyf("cancelorderflowrun: create denied: no viewer in context")
		}
		if ownerID, ok := runMut.OwnerID(); ok && ownerID == v.Subject {
			return privacy.Allow
		}
		return privacy.Denyf("cancelorderflowrun: create denied: not starting a run for your own order")
	}

	state, stateSet := runMut.State()
	if !stateSet || state != cancelorderflowrun.StateCancelled {
		return privacy.Skip
	}
	if !hasViewer {
		return privacy.Denyf("cancelorderflowrun: cancel denied: no viewer in context")
	}
	runID, ok := runMut.ID()
	if !ok {
		return privacy.Denyf("cancelorderflowrun: cancel denied: no run id on mutation")
	}
	// A same-transaction, privacy-bypassed lookup (privacy.DecisionContext)
	// of the run's own owner — the generated mutation carries no OldOwnerID
	// accessor (edges only track pending changes in this mutation, not the
	// row's persisted value), so confirming "is this viewer the run's
	// current owner" needs one read through the mutation's own Client(),
	// which reuses the mutation's transaction driver when one is active.
	owns, err := runMut.Client().CancelOrderFlowRun.Query().
		Where(cancelorderflowrun.IDEQ(runID), cancelorderflowrun.HasOwnerWith(order.IDEQ(v.Subject))).
		Exist(privacy.DecisionContext(ctx, privacy.Allow))
	if err != nil {
		return privacy.Denyf("cancelorderflowrun: cancel denied: checking ownership: %v", err)
	}
	if !owns {
		return privacy.Denyf("cancelorderflowrun: cancel denied: not the run's owner")
	}
	return privacy.Allow
}
