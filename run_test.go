package entflow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// cancelOrderShapedFlow builds a flow with the exact step names, kinds, and
// declaration order as internal/testdata/ent/schema/order_flows.go's real
// CancelOrder flow ("cancel" DB, "refund" Activity, "order.cancelled" Emit).
// package entflow cannot import internal/testdata/ent/schema directly — that
// package imports entflow, and importing it back here would be the exact
// import cycle Order's Hooks() doc comment exists to avoid — so this helper
// reconstructs CancelOrder's shape locally, the same way activity_test.go
// and exec_test.go's package-entflow tests already build ad hoc flows for
// unexported-seam access.
func cancelOrderShapedFlow() *FlowOf[string] {
	f := New[string]("CancelOrder")
	UpdateSelf(f, "cancel", func(ctx context.Context, tx any, in string) (string, error) {
		return "", nil
	}, Transition("cancelled"))
	Activity(f, "refund", func(ctx context.Context, self string, att Attempt) (JSON[int], error) {
		return NewJSON(0), nil
	})
	Emit(f, "order.cancelled", After("cancel"))
	return f
}

// TestRunStatesDerivedFromStepGraph proves RunStates(f) is genuinely
// derived from f's declared step graph — not a hand-copied list — by
// asserting the four base values plus one RunStateFailed value per declared
// step, in declaration order.
func TestRunStatesDerivedFromStepGraph(t *testing.T) {
	f := cancelOrderShapedFlow()

	got := RunStates(f)

	require.Equal(t, []string{
		RunStatePending,
		RunStateRunning,
		RunStateDone,
		RunStateCancelled,
		"failed:cancel",
		"failed:refund",
		"failed:order.cancelled",
	}, got)
	require.Contains(t, got, "failed:cancel")
	require.Contains(t, got, "failed:refund")
}

// TestRunStateFailedProducesColonForm proves the literal colon separator
// D-27 requires.
func TestRunStateFailedProducesColonForm(t *testing.T) {
	require.Equal(t, "failed:refund", RunStateFailed("refund"))
}

// TestGoIdentDerivesValidIdentifierFromPunctuatedStepName proves goIdent
// turns a dot-bearing step name like Emit's topic-as-name into a valid,
// exported Go identifier by splitting on non-identifier runes.
func TestGoIdentDerivesValidIdentifierFromPunctuatedStepName(t *testing.T) {
	require.Equal(t, "OrderCancelled", goIdent("CancelOrder", "order.cancelled"))
	require.Equal(t, "Cancel", goIdent("CancelOrder", "cancel"))
}

// TestGoIdentPanicsOnAllPunctuationStepName proves goIdent panics with a
// descriptive message, naming both the flow and the offending step, rather
// than silently producing an empty or invalid Go identifier that would fail
// deep inside ent's own codegen with no reference back to the cause.
func TestGoIdentPanicsOnAllPunctuationStepName(t *testing.T) {
	require.PanicsWithError(t,
		`entflow: RunMixin("CancelOrder"): step "..." cannot produce a valid Go identifier for its failed:<step> enum value`,
		func() { goIdent("CancelOrder", "...") },
	)
}

// TestRunMixinBuildsFieldsAndIndexes proves RunMixin(f) returns an ent.Mixin
// whose Fields() carries the D-28 framework-owned columns plus the D-42
// self_was column, and whose Indexes() carries the D-34 partial index —
// without requiring a real generated ent package (this test exercises the
// mixin's builder output directly, package cancelorderflowrun_test in
// entflow_test proves it survives real codegen and migration).
func TestRunMixinBuildsFieldsAndIndexes(t *testing.T) {
	f := cancelOrderShapedFlow()
	m := RunMixin(f)

	fields := m.Fields()
	names := make(map[string]bool, len(fields))
	for _, fld := range fields {
		names[fld.Descriptor().Name] = true
	}
	for _, want := range []string{
		"state", "input", "current_step", "attempt", "last_error", "results",
		"retry_after", "trace_context", "self_was", "created_at",
		"updated_at", "started_at", "finished_at",
	} {
		require.True(t, names[want], "RunMixin fields missing %q", want)
	}

	require.Len(t, m.Indexes(), 1)
	require.Equal(t, []string{"state", "retry_after"}, m.Indexes()[0].Descriptor().Fields)

	// RunMixin never declares Edges() — the owner-aggregate edge belongs to
	// the concrete schema, never the library-supplied mixin.
	require.Empty(t, m.Edges())
}

// TestOwnerRefMismatchedInputTypePanicsFromNew proves WithOwnerRef gets the
// same declaration-time mismatch protection WithSelfStatus already has
// (WR-04): a reader built for another flow's input type fails at New, not
// deferred to whenever a run row first tries to persist.
func TestOwnerRefMismatchedInputTypePanicsFromNew(t *testing.T) {
	mismatched := WithOwnerRef(func(in int) (int, error) {
		return in, nil
	})

	require.Panics(t, func() {
		New[string]("CancelOrder", mismatched)
	})
}
