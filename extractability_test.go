package entflow_test

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/meta"
)

// TestNoClosureInMeta is D-19's structural mechanism: a reflective walk over
// every meta type — recursing into nested structs, slice/array element
// types, and pointer element types — proves no field anywhere in the
// metadata contract is function-typed. This is not merely "the current
// projection happens to skip closures" — it proves there is nowhere in these
// types for a closure to live.
func TestNoClosureInMeta(t *testing.T) {
	visited := map[reflect.Type]bool{}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(meta.FlowMeta{}),
		reflect.TypeOf(meta.StepMeta{}),
		reflect.TypeOf(meta.ConditionMeta{}),
		reflect.TypeOf(meta.RetryMeta{}),
	} {
		assertNoFuncField(t, typ, visited)
	}
}

// assertNoFuncField recurses into typ, failing if typ itself (after
// unwrapping pointers) is a function type, or if any struct field, slice
// element, array element, or pointer element it contains is. visited guards
// against infinite recursion on self-referential types.
func assertNoFuncField(t *testing.T, typ reflect.Type, visited map[reflect.Type]bool) {
	t.Helper()

	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if visited[typ] {
		return
	}
	visited[typ] = true

	require.NotEqual(t, reflect.Func, typ.Kind(), "type %s is function-typed — CORE-11 requires metadata types to hold no closures", typ)

	switch typ.Kind() {
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			require.NotEqual(t, reflect.Func, f.Type.Kind(), "field %s.%s is function-typed — CORE-11 requires metadata types to hold no closures", typ, f.Name)
			assertNoFuncField(t, f.Type, visited)
		}
	case reflect.Slice, reflect.Array:
		assertNoFuncField(t, typ.Elem(), visited)
	}
}

// countingSchema builds a flow using the same constructors the real fixture
// uses (UpdateSelf, Activity, Emit), where every DB-step closure and the
// Activity closure increment invocations via atomic.AddInt32. It satisfies
// the interface{ Flows() []entflow.Flow } contract entflow.FlowsOf expects,
// so this test exercises the exact same retrieval path
// TestRequiresDurableRunCancelOrderFlow (exec_test.go) does.
type countingSchema struct {
	invocations *int32
}

func (s countingSchema) Flows() []entflow.Flow {
	f := entflow.New[int]("Counting", entflow.WithOwner("Counting"))
	entflow.UpdateSelf(f, "cancel", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		atomic.AddInt32(s.invocations, 1)
		return in, nil
	}, entflow.Transition("cancelled"))
	entflow.Activity(f, "refund",
		func(ctx context.Context, self int, att entflow.Attempt) (entflow.JSON[int], error) {
			atomic.AddInt32(s.invocations, 1)
			return entflow.NewJSON(0), nil
		},
		entflow.When(entflow.SelfWas("paid")),
		entflow.Retry(entflow.Backoff(5, time.Second, time.Minute)),
	)
	entflow.Emit(f, "order.cancelled", entflow.After("cancel"))
	return []entflow.Flow{f}
}

// TestClosuresNeverInvoked is D-19's behavioural mechanism: building,
// retrieving via FlowsOf, taking Meta(), and rendering Describe() on a flow
// whose every DB-step and Activity closure increments a shared counter
// leaves that counter at exactly zero. This is the exact assertion
// STATE.md's [Phase 1 -> Phase 5] cross-phase blocker requires — Phase 5's
// entity-injection spike depends on this holding.
func TestClosuresNeverInvoked(t *testing.T) {
	var invocations int32
	sch := countingSchema{invocations: &invocations}

	flows := entflow.FlowsOf(sch)
	require.Len(t, flows, 1)
	require.EqualValues(t, 0, invocations, "FlowsOf must not invoke any step closure")

	_ = flows[0].Meta()
	require.EqualValues(t, 0, invocations, "Meta must not invoke any step closure")

	_ = flows[0].Describe()
	require.EqualValues(t, 0, invocations, "Describe must not invoke any step closure")
}
