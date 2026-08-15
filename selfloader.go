package entflow

import (
	"context"
	"fmt"
	"reflect"
)

// WithSelfLoader declares how a flow re-reads its owning entity, live, from
// inside each step's own transaction — the mechanism behind Self[T]. Built
// exactly like WithSelfStatus (exec.go): fn's declared input type is
// recorded on flowConfig.selfLoaderInType so New[In] can eagerly reject a
// mismatched declaration at construction time (WR-04, mirroring
// WithSelfStatus's and WithOwnerRef's identical guarantee), and the closure
// itself is captured behind a type-erased adapter that resolves both tx and
// in back with comma-ok assertions naming the actual and wanted types on
// failure. This is the third instance of the exact same erasure pattern
// (WithSelfStatus, WithOwnerRef, WithSelfLoader) — no second mechanism is
// invented.
//
// Unlike WithSelfStatus, which reads a status string ONCE at flow entry and
// persists it as a snapshot (D-10/D-42), WithSelfLoader's fn is invoked
// fresh on EVERY claim, inside that claim's own transaction, before the
// step closure runs — see Self's doc comment for the full three-way
// asymmetry with Result[T] and a SelfWas condition.
func WithSelfLoader[In, TX, Self any](fn func(ctx context.Context, tx TX, in In) (Self, error)) FlowOption {
	return func(cfg *flowConfig) {
		cfg.selfLoaderInType = reflect.TypeFor[In]()
		cfg.selfLoader = func(ctx context.Context, tx any, in any) (any, error) {
			typedTx, ok := tx.(TX)
			if !ok {
				return nil, fmt.Errorf("entflow: WithSelfLoader: tx has type %T, want %s", tx, reflect.TypeFor[TX]())
			}
			typedIn, ok := in.(In)
			if !ok {
				return nil, fmt.Errorf("entflow: WithSelfLoader: in has type %T, want %s", in, reflect.TypeFor[In]())
			}
			return fn(ctx, typedTx, typedIn)
		}
	}
}

// selfKey is the unexported context key type under which the current step's
// live self value is attached — a sibling of resultsKey (result.go), but
// set fresh once per step's own transaction by Runner.ExecStep (via
// StepCall.Self), rather than derived once per whole execution the way a
// resultStore is.
type selfKey struct{}

// setSelf attaches self to ctx under selfKey. Unexported: ExecStep is its
// only caller, immediately before invoking the step closure — an
// application never calls this directly, the same way it never calls
// putResult directly.
func setSelf(ctx context.Context, self any) context.Context {
	return context.WithValue(ctx, selfKey{}, self)
}

// Self retrieves the current step's live owning entity from ctx: the value
// f's declared WithSelfLoader read inside THIS step's own transaction, at
// the start of the claim that is currently executing. It is a fresh read on
// every claim, never cached across claims and never rehydrated from
// persisted JSON — the opposite end of the spectrum from Result[T] (inert
// JSON-round-tripped data, D-40) and from a SelfWas condition (a snapshot
// frozen once at flow entry and persisted on the run row, D-10/D-42). Three
// different guarantees, all honest, none of them accidental: a reader who
// does not know which one they are holding will write a bug.
//
// Self never panics: a context carrying no self value — a flow that calls
// Self without declaring WithSelfLoader — yields ErrNoSelfLoader, and a
// stored value whose type does not match T yields a descriptive error via
// the same comma-ok discipline entflow uses everywhere else.
func Self[T any](ctx context.Context) (T, error) {
	var zero T
	v := ctx.Value(selfKey{})
	if v == nil {
		return zero, ErrNoSelfLoader
	}
	typed, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("entflow: Self: loaded value has type %T, want %s", v, reflect.TypeFor[T]())
	}
	return typed, nil
}
