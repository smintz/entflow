package entflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/ent/schema"
	"github.com/smintz/entflow/internal/testdata/entclient"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
	"github.com/smintz/entflow/worker"
)

// newSpanRecorder returns an in-memory OTel SDK span recorder and a
// TracerProvider wired to it, for asserting exactly which spans
// worker/dbstep.go's claim path produced (D-58). The SDK is imported ONLY
// from this test file — it never enters entflow's non-test dependency
// graph, which deps_test.go's TestNoTransportDeps/TestTracingDependency-
// Narrowness both assert mechanically, not just by file-naming convention.
func newSpanRecorder() (*tracetest.SpanRecorder, trace.TracerProvider) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	return rec, tp
}

// findSpan returns the single ended span named name, failing the test if
// zero or more than one match — every assertion in this file wants an exact
// span, never "at least one."
func findSpan(t *testing.T, ended []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	var found []sdktrace.ReadOnlySpan
	for _, s := range ended {
		if s.Name() == name {
			found = append(found, s)
		}
	}
	require.Len(t, found, 1, "expected exactly one span named %q among %d ended spans", name, len(ended))
	return found[0]
}

// attrValue returns the value of the first attribute on s carrying key,
// failing the test if absent.
func attrValue(t *testing.T, s sdktrace.ReadOnlySpan, key attribute.Key) attribute.Value {
	t.Helper()
	for _, kv := range s.Attributes() {
		if kv.Key == key {
			return kv.Value
		}
	}
	t.Fatalf("span %q missing attribute %q", s.Name(), key)
	return attribute.Value{}
}

// observabilityFixtureInput is this file's own local flow input, mirroring
// durablerun_test.go's fixtureInput convention.
type observabilityFixtureInput struct {
	OrderID int
}

// newObservabilityFailingFlow returns a single-DB-step flow whose one step
// is named "cancel" — so its failed:<step> state (failed:cancel) is a value
// CancelOrderFlowRun's own RunMixin-derived state enum already contains,
// exactly like worker/retry_test.go's newRetryFlow — and whose closure
// always returns stepErr, never touching the database.
func newObservabilityFailingFlow(stepErr error) *entflow.FlowOf[*observabilityFixtureInput] {
	f := entflow.New[*observabilityFixtureInput]("FailingFixture",
		entflow.WithOwnerRef(func(in *observabilityFixtureInput) (int, error) {
			return in.OrderID, nil
		}),
	)
	entflow.Check(f, "cancel", func(ctx context.Context, tx *ent.Tx, in *observabilityFixtureInput) error {
		return stepErr
	})
	return f
}

// TestSingleStepRunProducesRootAndOneChildWithAttributes proves the common
// case: a single-DB-step run emits exactly one root span and one child
// span, the child is named exactly workflow.CancelOrder.cancel, carries the
// five D-58 attributes with the expected values, and no attribute value
// equals or contains the run's serialized input or any recorded step
// result (T-02-03's prohibition).
func TestSingleStepRunProducesRootAndOneChildWithAttributes(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	cancelOrder := cancelOrderFlowTyped(t)
	require.NoError(t, eng.Register(cancelOrder, store))

	owner, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
	require.NoError(t, err)

	run, err := entflow.Start(ctx, eng, cancelOrder, &schema.CancelOrderRequest{OrderID: owner.ID})
	require.NoError(t, err)

	rec, tp := newSpanRecorder()
	w, err := worker.New(eng, worker.Options{
		Dialect:        "sqlite",
		Concurrency:    1,
		ClaimStrategy:  worker.SQLiteStrategy(),
		TracerProvider: tp,
	})
	require.NoError(t, err)

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed)

	ended := rec.Ended()
	require.Len(t, ended, 2, "a single-step run must emit exactly one root span and one child span")

	root := findSpan(t, ended, worker.RootSpanName("CancelOrder"))
	child := findSpan(t, ended, worker.SpanName("CancelOrder", "cancel"))

	require.Equal(t, root.SpanContext().TraceID(), child.SpanContext().TraceID(),
		"the child span must belong to the root span's trace")
	require.Equal(t, root.SpanContext().SpanID(), child.Parent().SpanID(),
		"the child span's parent must be the root span")

	require.Equal(t, "cancel", attrValue(t, child, worker.AttrStep).AsString())
	require.Equal(t, "CancelOrder", attrValue(t, child, worker.AttrFlow).AsString())
	require.Equal(t, "done", attrValue(t, child, worker.AttrState).AsString())
	require.EqualValues(t, 0, attrValue(t, child, worker.AttrAttempt).AsInt64())
	require.NotEmpty(t, attrValue(t, child, worker.AttrRunID).AsString())

	require.Equal(t, "done", attrValue(t, root, worker.AttrState).AsString(),
		"the run's terminal state is an attribute on the root span, since this run's first claim is also its last")

	persisted, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.NotEmpty(t, persisted.Input)
	forbidden := []string{string(persisted.Input)}
	for _, raw := range persisted.Results {
		forbidden = append(forbidden, string(raw))
	}
	for _, s := range ended {
		for _, kv := range s.Attributes() {
			rendered := kv.Value.Emit()
			for _, f := range forbidden {
				require.NotContains(t, rendered, f,
					"span %q attribute %q must never carry the run's input or a step result", s.Name(), kv.Key)
			}
		}
	}
}

// TestAllStepsSkippedProducesRootAndZeroChildren proves D-58's empty edge: a
// run whose every remaining step's condition evaluates false still gets its
// root span, and zero child spans.
func TestAllStepsSkippedProducesRootAndZeroChildren(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	allSkipped := newAllSkippedFlow()
	require.NoError(t, eng.Register(allSkipped, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)

	_, err = entflow.Start(ctx, eng, allSkipped, &fixtureInput{OrderID: owner.ID})
	require.NoError(t, err)

	rec, tp := newSpanRecorder()
	w, err := worker.New(eng, worker.Options{
		Dialect:        "sqlite",
		Concurrency:    1,
		ClaimStrategy:  worker.SQLiteStrategy(),
		TracerProvider: tp,
	})
	require.NoError(t, err)

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed)

	ended := rec.Ended()
	require.Len(t, ended, 1, "a run whose every step is skipped must emit exactly one span: its root")
	require.Equal(t, worker.RootSpanName("AllSkipped"), ended[0].Name())
}

// TestTwoStepRunChildSpansInOrderShareRootTrace proves D-58's boundary edge
// and D-59's cross-claim continuity together: a two-DB-step run produces
// exactly two child spans under one root, in step-execution order, and the
// second claim's child span — restored from the persisted trace context
// rather than an in-process parent — shares the first claim's root as its
// parent, proving the resume boundary is crossed.
func TestTwoStepRunChildSpansInOrderShareRootTrace(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	multiStep := newMultiStepFlow()
	require.NoError(t, eng.Register(multiStep, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)

	_, err = entflow.Start(ctx, eng, multiStep, &fixtureInput{OrderID: owner.ID})
	require.NoError(t, err)

	rec, tp := newSpanRecorder()
	w, err := worker.New(eng, worker.Options{
		Dialect:        "sqlite",
		Concurrency:    1,
		ClaimStrategy:  worker.SQLiteStrategy(),
		TracerProvider: tp,
	})
	require.NoError(t, err)

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed, "first claim (step1) should succeed")

	claimed, err = w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed, "second claim (step2), a simulated resume in a later claim, should succeed")

	ended := rec.Ended()
	root := findSpan(t, ended, worker.RootSpanName("MultiStep"))
	child1 := findSpan(t, ended, worker.SpanName("MultiStep", "step1"))
	child2 := findSpan(t, ended, worker.SpanName("MultiStep", "step2"))

	var childCount int
	for _, s := range ended {
		if s.Name() == worker.SpanName("MultiStep", "step1") || s.Name() == worker.SpanName("MultiStep", "step2") {
			childCount++
		}
	}
	require.Equal(t, 2, childCount, "a two-executed-step run must emit exactly two child spans")

	require.True(t, child1.StartTime().Before(child2.StartTime()) || child1.StartTime().Equal(child2.StartTime()),
		"step1's span must start no later than step2's, matching step-execution order")

	require.Equal(t, root.SpanContext().TraceID(), child1.SpanContext().TraceID())
	require.Equal(t, root.SpanContext().TraceID(), child2.SpanContext().TraceID(),
		"a child span produced by a later, resumed claim must still belong to the first claim's trace")
	require.Equal(t, root.SpanContext().SpanID(), child2.Parent().SpanID(),
		"the resumed claim's child span must be parented to the original root span, restored from the persisted trace context")
}

// TestFailingStepSpanRecordsErrorAndErrorStatus proves a failing step's
// child span carries a recorded error and an error status, with the
// recorded message naming the step — via StepError's own rendered message,
// never the run's raw input.
func TestFailingStepSpanRecordsErrorAndErrorStatus(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	boom := errors.New("boom")
	failing := newObservabilityFailingFlow(boom)
	require.NoError(t, eng.Register(failing, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)

	run, err := entflow.Start(ctx, eng, failing, &observabilityFixtureInput{OrderID: owner.ID})
	require.NoError(t, err)

	rec, tp := newSpanRecorder()
	w, err := worker.New(eng, worker.Options{
		Dialect:        "sqlite",
		Concurrency:    1,
		ClaimStrategy:  worker.SQLiteStrategy(),
		TracerProvider: tp,
	})
	require.NoError(t, err)

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed)

	ended := rec.Ended()
	child := findSpan(t, ended, worker.SpanName("FailingFixture", "cancel"))

	require.Equal(t, codes.Error, child.Status().Code)
	require.Contains(t, child.Status().Description, "cancel", "the recorded status message must name the failing step")
	require.NotContains(t, child.Status().Description, boom.Error()+boom.Error(), "sanity: not a doubled message")

	require.NotEmpty(t, child.Events(), "RecordError must add a span event")

	finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, cancelorderflowrun.StateFailedCancel, finalRun.State)
}

// TestPollCycleClaimingNothingProducesNoSpans proves an empty poll — no run
// claimable — is not a run event: it starts no span at all, so span volume
// tracks work rather than poll rate (T-02-16).
func TestPollCycleClaimingNothingProducesNoSpans(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	cancelOrder := cancelOrderFlowTyped(t)
	require.NoError(t, eng.Register(cancelOrder, store))

	rec, tp := newSpanRecorder()
	w, err := worker.New(eng, worker.Options{
		Dialect:        "sqlite",
		Concurrency:    1,
		ClaimStrategy:  worker.SQLiteStrategy(),
		TracerProvider: tp,
	})
	require.NoError(t, err)

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.False(t, claimed, "no run was ever started, so nothing should be claimable")
	require.Empty(t, rec.Ended(), "an empty poll cycle must not emit any span")
}
