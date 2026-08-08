// Package meta defines the read-only flow-metadata contract that
// (*entflow.FlowOf[In]).Meta() projects onto, and the one package a
// different module (entconnect) is expected to import (META-01). It depends
// on the standard library ONLY — never entflow, never entgo.io/ent — so the
// one-directional boundary is structural, not conventional: nothing here can
// drag entflow's or ent's own dependency graph into a consumer that only
// wants flow shape data.
//
// Every type below is a value type or a slice of value types. No field
// anywhere in this package is function-typed — extractability_test.go proves
// this by reflection, and it is the structural half of CORE-11/D-19: code
// that only ever touches these types cannot reach a step closure, because
// there is nowhere in these shapes for one to live.
package meta

import "time"

// Kind identifies a step's safety class. It duplicates entflow's own
// StepKind rather than aliasing it — an alias would make this package import
// entflow, inverting the one-directional dependency META-01 requires.
// (*entflow.FlowOf[In]).Meta() maps between the two string-identical
// constant sets.
type Kind string

const (
	// KindDB identifies a step whose closure receives a transaction handle
	// and mutates the database inside it.
	KindDB Kind = "db"
	// KindActivity identifies a step whose closure performs an external
	// call and never receives a transaction handle.
	KindActivity Kind = "activity"
	// KindEmit identifies a fire-and-forget outbox write.
	KindEmit Kind = "emit"
)

// FlowMeta is the immutable, data-only snapshot of a declared flow's
// structure — flow name, owning entity, input Go type, and step topology —
// per META-01's decoupling contract (entflow.md §9).
// (*entflow.FlowOf[In]).Meta() returns a fresh FlowMeta on every call, with
// every slice deep-copied, so mutating a value returned by one call can
// never affect a later call or the live flow declaration (D-19).
type FlowMeta struct {
	// Name is the flow's declared name, e.g. "CancelOrder".
	Name string
	// Owner is the name of the entity the flow was declared on, supplied
	// explicitly via entflow.WithOwner. Phase 1 has no automatic binding
	// from a flow to its owning entity — inferring it by inspecting
	// closure bodies is exactly what ARCHITECTURE.md's Anti-Pattern 3
	// forbids.
	Owner string
	// InType renders the flow's input Go type, e.g.
	// "*schema.CancelOrderRequest".
	InType string
	// OutType renders the flow's declared output Go type. Empty in Phase
	// 1: no step kind declares a flow-level output type yet, and
	// inventing one now would fix a shape Phase 2's run-row response has
	// not yet determined.
	OutType string
	// Steps holds every declared step's metadata, in declaration order.
	Steps []StepMeta
}

// StepMeta is the immutable, data-only snapshot of a single declared step —
// every codegen-needed fact CORE-11 requires, and nothing else.
type StepMeta struct {
	// Name is the step's declared name (Emit's topic doubles as its name).
	Name string
	// Kind is the step's safety class.
	Kind Kind
	// Constructor is the name of the constructor that declared the step
	// (e.g. "UpdateSelf", "Activity", "Emit") — sugar over one mechanism
	// per kind, but the constructor name itself is a codegen-relevant
	// fact.
	Constructor string
	// DependsOn lists the names of steps this step's After(...) options
	// declared it must run after.
	DependsOn []string
	// Transition is the status value this step's mutation claims to move
	// the owning entity to, or empty if the step claims none.
	Transition string
	// EmitTopic is the outbox topic an Emit step publishes to, or empty
	// for every other kind.
	EmitTopic string
	// Conditions lists every When(...) predicate guarding this step.
	Conditions []ConditionMeta
	// Retry is the step's retry policy, or nil if it declared none.
	Retry *RetryMeta
}

// ConditionMeta is the data-only projection of an entflow.Condition.
type ConditionMeta struct {
	Kind  string
	Value string
}

// RetryMeta is the data-only projection of an entflow.RetryPolicy.
type RetryMeta struct {
	MaxAttempts int
	Initial     time.Duration
	Max         time.Duration
}
