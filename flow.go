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
	"reflect"

	"github.com/smintz/entflow/meta"
)

// Flow is the schema-facing interface returned from a schema's Flows()
// method: func (Order) Flows() []entflow.Flow. Meta() and Describe() make a
// heterogeneous []Flow directly useful to codegen and to entconnect without
// a type assertion back to a concrete *FlowOf[In].
type Flow interface {
	Name() string
	Meta() meta.FlowMeta
	Describe() string
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
	// selfStatusInType is the In type WithSelfStatus's fn was declared
	// against — set alongside selfStatus so New[In] can eagerly compare it to
	// its own In, giving WithSelfStatus the same declaration-time mismatch
	// protection WithCodec already has via resolveCodec's type assertion
	// (WR-04). nil unless a WithSelfStatus option was applied.
	selfStatusInType reflect.Type
	owner            string
	// ownerRef is the type-erased adapter WithOwnerRef installs, resolving
	// a flow's input value to its owning aggregate's ID. nil unless a
	// WithOwnerRef option was applied.
	ownerRef func(in any) (any, error)
	// ownerRefInType is the In type WithOwnerRef's fn was declared
	// against — set alongside ownerRef so New[In] can eagerly compare it
	// to its own In, mirroring selfStatusInType's declaration-time
	// mismatch protection (WR-04).
	ownerRefInType reflect.Type
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

	// owner is the name of the entity this flow was declared on, supplied
	// via WithOwner. Empty unless the caller declared one.
	owner string

	// ownerRef is the type-erased adapter WithOwnerRef installs, resolving
	// this flow's input value to its owning aggregate's ID (D-26). nil
	// unless the flow declared one.
	ownerRef func(in any) (any, error)
}

// New constructs a flow builder named name, typed to input In. With no
// options, In is (de)serialized via the default JSONCodec[In]; WithCodec
// overrides that resolution without In having to implement anything (D-17).
func New[In any](name string, opts ...FlowOption) *FlowOf[In] {
	cfg := &flowConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.selfStatus != nil && cfg.selfStatusInType != reflect.TypeFor[In]() {
		panic(fmt.Errorf("entflow: New[%s](%q): WithSelfStatus supplied a reader declared for input type %s, which does not match", reflect.TypeFor[In](), name, cfg.selfStatusInType))
	}
	if cfg.ownerRef != nil && cfg.ownerRefInType != reflect.TypeFor[In]() {
		panic(fmt.Errorf("entflow: New[%s](%q): WithOwnerRef supplied a reader declared for input type %s, which does not match", reflect.TypeFor[In](), name, cfg.ownerRefInType))
	}
	return &FlowOf[In]{
		name:             name,
		codec:            resolveCodec[In](cfg),
		selfStatusReader: cfg.selfStatus,
		owner:            cfg.owner,
		ownerRef:         cfg.ownerRef,
	}
}

// WithOwner declares the name of the entity a flow is owned by — flow
// metadata's Owner field. Phase 1 has no automatic binding from a flow to
// its owning entity (no codegen exists yet to infer it from), so this is an
// explicit FlowOption rather than inference over the schema type — the same
// reasoning that produced WithSelfStatus (exec.go).
func WithOwner(name string) FlowOption {
	return func(cfg *flowConfig) {
		cfg.owner = name
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

// Meta returns an immutable snapshot of f's declared structure — every
// step's name, kind, dependency edges, transition claim, emit topic, retry
// policy, and conditions — read entirely off each step's DATA fields, never
// off its run closure field (step.go documents why that separation makes
// this structural, not conventional, per CORE-11/D-19). Every slice is a
// fresh copy, so a caller mutating a returned FlowMeta's Steps, or a
// StepMeta's DependsOn or Conditions, can never reach back into f's live
// declaration.
func (f *FlowOf[In]) Meta() meta.FlowMeta {
	steps := make([]meta.StepMeta, len(f.steps))
	for i, s := range f.steps {
		steps[i] = stepMetaOf(s)
	}
	return meta.FlowMeta{
		Name:   f.name,
		Owner:  f.owner,
		InType: reflect.TypeFor[In]().String(),
		Steps:  steps,
	}
}

// stepMetaOf projects s's declaration data into a meta.StepMeta, deep-copying
// every slice so the returned value is independent of s and can never be
// used to mutate the live step.
func stepMetaOf(s *step) meta.StepMeta {
	var dependsOn []string
	if len(s.dependsOn) > 0 {
		dependsOn = append([]string(nil), s.dependsOn...)
	}

	var conditions []meta.ConditionMeta
	for _, c := range s.conditions {
		conditions = append(conditions, meta.ConditionMeta{Kind: c.Kind, Value: c.Value})
	}

	var retry *meta.RetryMeta
	if s.retry != nil {
		retry = &meta.RetryMeta{
			MaxAttempts: s.retry.MaxAttempts,
			Initial:     s.retry.Initial,
			Max:         s.retry.Max,
		}
	}

	return meta.StepMeta{
		Name:        s.name,
		Kind:        meta.Kind(s.kind),
		Constructor: s.constructor,
		DependsOn:   dependsOn,
		Transition:  s.transition,
		EmitTopic:   s.emitTopic,
		Conditions:  conditions,
		Retry:       retry,
	}
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
