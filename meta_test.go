package entflow_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent/schema"
	"github.com/smintz/entflow/meta"
)

// cancelOrderFlow returns the fixture CancelOrder flow via the same
// FlowsOf(schema.Order{}) path every other test in this package uses. Other
// test files in this package (describe_test.go, callshapes_test.go) reuse
// this helper.
//
// Since plan 02-08, Order{}.Flows() returns two flows — CancelOrder and the
// multi-step ProcessOrder crash-simulation fixture — sharing
// CancelOrderFlowRun's table (order_flows.go), so this helper selects
// CancelOrder by name rather than assuming a single-element slice.
func cancelOrderFlow(t *testing.T) entflow.Flow {
	t.Helper()
	flows := entflow.FlowsOf(schema.Order{})
	require.GreaterOrEqual(t, len(flows), 1)
	for _, f := range flows {
		if f.Name() == "CancelOrder" {
			return f
		}
	}
	t.Fatalf("schema.Order{}.Flows() does not declare a %q flow", "CancelOrder")
	return nil
}

// TestFlowMetaCoversDeclaredSteps proves Meta() reports the fixture's flow
// name and every declared step, in declaration order.
func TestFlowMetaCoversDeclaredSteps(t *testing.T) {
	m := cancelOrderFlow(t).Meta()

	require.Equal(t, "CancelOrder", m.Name)
	require.Equal(t, "Order", m.Owner)
	require.Equal(t, "*schema.CancelOrderRequest", m.InType)
	require.Empty(t, m.OutType, "Phase 1 declares no flow-level output type")
	require.Len(t, m.Steps, 3)

	names := make([]string, len(m.Steps))
	for i, s := range m.Steps {
		names[i] = s.Name
	}
	require.Equal(t, []string{"cancel", "refund", "order.cancelled"}, names)
}

// TestFlowMetaCancelStep proves the DB step's declaration data — kind,
// constructor, and transition claim — is readable off Meta().
func TestFlowMetaCancelStep(t *testing.T) {
	m := cancelOrderFlow(t).Meta()
	cancel := m.Steps[0]

	require.Equal(t, "cancel", cancel.Name)
	require.Equal(t, meta.KindDB, cancel.Kind)
	require.Equal(t, "UpdateSelf", cancel.Constructor)
	require.Equal(t, "cancelled", cancel.Transition)
	require.Empty(t, cancel.EmitTopic)
	require.Nil(t, cancel.Retry)
	require.Empty(t, cancel.Conditions)
}

// TestFlowMetaRefundStep proves the Activity step's condition and retry
// policy are readable off Meta() with their exact declared values.
func TestFlowMetaRefundStep(t *testing.T) {
	m := cancelOrderFlow(t).Meta()
	refund := m.Steps[1]

	require.Equal(t, "refund", refund.Name)
	require.Equal(t, meta.KindActivity, refund.Kind)
	require.Equal(t, "Activity", refund.Constructor)
	require.Empty(t, refund.Transition)
	require.Empty(t, refund.EmitTopic)

	require.Len(t, refund.Conditions, 1)
	require.Equal(t, "self_was", refund.Conditions[0].Kind)
	require.Equal(t, "paid", refund.Conditions[0].Value)

	require.NotNil(t, refund.Retry)
	require.Equal(t, 5, refund.Retry.MaxAttempts)
	require.Equal(t, time.Second, refund.Retry.Initial)
	require.Equal(t, time.Minute, refund.Retry.Max)
}

// TestFlowMetaEmitStep proves the Emit step's emit topic and After
// dependency are readable off Meta().
func TestFlowMetaEmitStep(t *testing.T) {
	m := cancelOrderFlow(t).Meta()
	emit := m.Steps[2]

	require.Equal(t, "order.cancelled", emit.Name)
	require.Equal(t, meta.KindEmit, emit.Kind)
	require.Equal(t, "Emit", emit.Constructor)
	require.Equal(t, "order.cancelled", emit.EmitTopic)
	require.Equal(t, []string{"cancel"}, emit.DependsOn)
	require.Nil(t, emit.Retry)
}

// TestMetaIsIndependentSnapshot proves D-19's immutable-snapshot guarantee:
// mutating slices inside a returned FlowMeta — its own Steps slice, and a
// StepMeta's DependsOn slice — never reaches back into the live flow, so a
// second Meta() call is unaffected.
func TestMetaIsIndependentSnapshot(t *testing.T) {
	flow := cancelOrderFlow(t)
	m := flow.Meta()

	m.Steps[0].Name = "mutated"
	m.Steps[2].DependsOn[0] = "mutated-dep"
	m.Steps = append(m.Steps, meta.StepMeta{Name: "injected"})

	again := flow.Meta()
	require.Len(t, again.Steps, 3)
	require.Equal(t, "cancel", again.Steps[0].Name)
	require.Equal(t, []string{"cancel"}, again.Steps[2].DependsOn)
}

// TestMetaPackageDependenciesAreStdlibOnly proves META-01's structural
// boundary: the meta package's own non-test dependency set — computed with
// `go list -deps github.com/smintz/entflow/meta` — is entirely standard
// library, classified by the canonical dot-in-first-segment rule (a package
// path's first segment contains a dot only if it names a module host, e.g.
// "entgo.io" or "github.com").
func TestMetaPackageDependenciesAreStdlibOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/smintz/entflow/meta").CombinedOutput()
	require.NoError(t, err, "go list -deps github.com/smintz/entflow/meta: %s", out)

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || line == "github.com/smintz/entflow/meta" {
			continue // the package under test itself, not a dependency
		}
		first := strings.SplitN(line, "/", 2)[0]
		require.False(t, strings.Contains(first, "."), "meta package dependency %q is not standard library", line)
	}
}
