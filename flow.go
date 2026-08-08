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
)

// Flow is the schema-facing interface returned from a schema's Flows()
// method: func (Order) Flows() []entflow.Flow. It is deliberately minimal in
// Phase 1 — additional methods (Meta()) are added by a later plan once the
// metadata contract is designed; keeping it to Name() now is what lets the
// fixture schema compile from this tracer onward without churn later.
type Flow interface {
	Name() string
}

// flowConfig holds construction-time options for New. codec is stored as an
// untyped any because flowConfig itself is not generic — New[In] resolves it
// back to Codec[In] via resolveCodec (codec.go). selfStatus is already
// type-erased to its final shape by WithSelfStatus (exec.go) — no assert-back
// is needed for it because, unlike the codec, its erased signature never
// varies by In beyond the any boxing WithSelfStatus itself performs.
type flowConfig struct {
	codec      any
	selfStatus func(ctx context.Context, tx any, in any) (string, error)
}

// FlowOption configures a FlowOf at construction time via New.
type FlowOption func(*flowConfig)

// FlowOf is the generic flow builder returned by New[In]. Its only type
// parameter is the flow's input type; step constructors that need additional
// type parameters (the transaction type, the entity type, an Activity's
// output type) cannot be methods on FlowOf — Go methods cannot declare their
// own type parameters — so they are package-level generic functions taking
// *FlowOf[In] as their first argument instead (see steps.go's UpdateSelf and
// its six siblings).
type FlowOf[In any] struct {
	name  string
	steps []*step
	codec Codec[In]

	// selfStatusReader is the type-erased adapter WithSelfStatus installs.
	// nil unless the flow declared one; a flow with a When(SelfWas(...))
	// condition but no reader fails at Exec time (D-10) rather than at
	// construction, so the missing-option error can name the flow.
	selfStatusReader func(ctx context.Context, tx any, in any) (string, error)
}

// New constructs a flow builder named name, typed to input In. With no
// options, In is (de)serialized via the default JSONCodec[In]; WithCodec
// overrides that resolution without In having to implement anything (D-17).
func New[In any](name string, opts ...FlowOption) *FlowOf[In] {
	cfg := &flowConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return &FlowOf[In]{
		name:             name,
		codec:            resolveCodec[In](cfg),
		selfStatusReader: cfg.selfStatus,
	}
}

// codecOf returns f's resolved codec. Unexported: the executor (this
// package) and Plan 04's metadata path are its only Phase-1 consumers.
func (f *FlowOf[In]) codecOf() Codec[In] {
	return f.codec
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
