// Package entflow makes durable workflows a schema concern for ent
// applications. Flows are declared inside ent schema files via a Flows()
// method — a peer of Fields(), Hooks(), and Policy() — with step bodies
// written as typed closures inline in the declaration.
//
// This file establishes the walking-skeleton spine of Phase 1: the Flow
// interface schemas return from Flows(), the generic FlowOf[In] builder
// New[In] constructs, and FlowsOf, the explicit-registration helper that
// mirrors how ent's own generated ent/runtime package discovers Hooks().
package entflow

import (
	"context"
	"fmt"
)

// Flow is the schema-facing interface returned from a schema's Flows()
// method: func (Order) Flows() []entflow.Flow. It is deliberately minimal in
// Phase 1 — additional methods (Meta()) are added by a later plan once the
// metadata contract is designed; keeping it to Name() now is what lets the
// fixture schema compile from this tracer onward without churn later.
type Flow interface {
	Name() string
}

// flowConfig holds construction-time options for New. It is empty in this
// plan — it exists so a later plan can add WithCodec (D-17) without altering
// New's published signature, which is a one-way contract every flow
// declaration calls.
type flowConfig struct{}

// FlowOption configures a FlowOf at construction time via New.
type FlowOption func(*flowConfig)

// FlowOf is the generic flow builder returned by New[In]. Its only type
// parameter is the flow's input type; step constructors that need additional
// type parameters (the transaction type, the entity type, an Activity's
// output type) cannot be methods on FlowOf — Go methods cannot declare their
// own type parameters — so they are package-level generic functions taking
// *FlowOf[In] as their first argument instead (see UpdateSelf below).
type FlowOf[In any] struct {
	name  string
	steps []*step
}

// New constructs a flow builder named name, typed to input In. opts is
// reserved for future construction-time configuration (WithCodec in a later
// plan); it carries no behavior in this plan.
func New[In any](name string, opts ...FlowOption) *FlowOf[In] {
	cfg := &flowConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return &FlowOf[In]{name: name}
}

// Name returns the flow's declared name, satisfying the Flow interface.
func (f *FlowOf[In]) Name() string {
	return f.name
}

// FlowsOf performs the interface{ Flows() []Flow } type assertion against
// schema, mirroring how ent's generated ent/runtime package discovers a
// schema's Hooks(). It returns nil, rather than panicking, when schema has no
// Flows() method — a schema with no flows is not an error.
//
// This is Phase 1's honest hand-written equivalent of the generated
// ent/runtime package's init(): with no codegen yet (D-06), applications
// register flows explicitly by calling FlowsOf on each schema value.
func FlowsOf(schema any) []Flow {
	fs, ok := schema.(interface{ Flows() []Flow })
	if !ok {
		return nil
	}
	return fs.Flows()
}

// UpdateSelf declares a DB step that mutates the entity owning the flow. Its
// closure fn receives the caller-supplied transaction (typed as TX, inferred
// from the closure literal — never named explicitly at the call site) and
// the flow's input, and returns the updated entity.
//
// UpdateSelf cannot be a method on *FlowOf[In]: TX and Ent are type
// parameters independent of In, and Go methods cannot declare their own type
// parameters (VERIFIED during Phase 1 research, Go 1.25.1). Every DB-step
// constructor in this package therefore follows this same package-level
// generic function shape, taking *FlowOf[In] as its first argument and
// returning it, so sequential declaration still reads naturally:
//
//	f := entflow.New[*CancelOrderRequest]("CancelOrder")
//	entflow.UpdateSelf(f, "cancel", func(ctx context.Context, tx *ent.Tx, in *CancelOrderRequest) (*ent.Order, error) {
//		return tx.Order.UpdateOneID(in.OrderID).SetStatus(order.StatusCancelled).Save(ctx)
//	}, entflow.Transition("cancelled"))
func UpdateSelf[In, TX, Ent any](
	f *FlowOf[In],
	name string,
	fn func(ctx context.Context, tx TX, in In) (Ent, error),
	opts ...StepOption,
) *FlowOf[In] {
	s := &step{
		name:        name,
		kind:        KindDB,
		constructor: "UpdateSelf",
	}
	for _, opt := range opts {
		opt(s)
	}
	s.run = func(ctx context.Context, tx any, in any) (any, error) {
		typedTx, ok := tx.(TX)
		if !ok {
			var zeroTX TX
			return nil, fmt.Errorf("entflow: step %q (UpdateSelf): tx has type %T, want %T", name, tx, zeroTX)
		}
		typedIn, ok := in.(In)
		if !ok {
			var zeroIn In
			return nil, fmt.Errorf("entflow: step %q (UpdateSelf): in has type %T, want %T", name, in, zeroIn)
		}
		return fn(ctx, typedTx, typedIn)
	}
	f.steps = append(f.steps, s)
	return f
}
