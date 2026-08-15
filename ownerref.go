package entflow

import (
	"fmt"
	"reflect"
)

// WithOwnerRef declares how a flow's input value resolves to the ID of its
// owning aggregate row — the D-26 lineage edge a run row carries to the
// entity it belongs to. fn is captured with the same type-erasure pattern
// WithSelfStatus (exec.go) uses: In is recorded on flowConfig.ownerRefInType
// so New[In] can reject a mismatched declaration at construction time,
// mirroring WithSelfStatus's WR-04 guarantee — a reader built for another
// flow's input type fails where it was written, not the first time a run row
// tries to persist.
//
// The lineage edge is declared, never inferred: codegen must never infer
// facts by inspecting closure bodies, so a flow that wants its run row
// linked to an owning aggregate says so explicitly via this option.
func WithOwnerRef[In, ID any](fn func(In) (ID, error)) FlowOption {
	return func(cfg *flowConfig) {
		cfg.ownerRefInType = reflect.TypeFor[In]()
		cfg.ownerRef = func(in any) (any, error) {
			typedIn, ok := in.(In)
			if !ok {
				return nil, fmt.Errorf("entflow: WithOwnerRef: in has type %T, want %s", in, reflect.TypeFor[In]())
			}
			id, err := fn(typedIn)
			if err != nil {
				return nil, err
			}
			return id, nil
		}
	}
}
