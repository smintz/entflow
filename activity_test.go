package entflow

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestActivityRecordsKindAndRetry proves the behavior block's first bullet:
// Activity compiles with no explicit type arguments, records the activity
// kind, and holds the retry policy's exact parameters.
func TestActivityRecordsKindAndRetry(t *testing.T) {
	f := New[string]("Refund")
	Activity(f, "refund",
		func(ctx context.Context, self string, att Attempt) (JSON[int], error) {
			return NewJSON(0), nil
		},
		When(SelfWas("paid")),
		Retry(Backoff(5, time.Second, time.Minute)),
	)

	require.Len(t, f.steps, 1)
	s := f.steps[0]
	require.Equal(t, "refund", s.name)
	require.Equal(t, KindActivity, s.kind)
	require.Equal(t, "Activity", s.constructor)
	require.NotNil(t, s.retry)
	require.Equal(t, 5, s.retry.MaxAttempts)
	require.Equal(t, time.Second, s.retry.Initial)
	require.Equal(t, time.Minute, s.retry.Max)
	require.Len(t, s.conditions, 1)
	require.Equal(t, string(conditionSelfWas), s.conditions[0].Kind)
	require.Equal(t, "paid", s.conditions[0].Value)
}

// TestEmitRecordsTopic proves Emit records the emit kind and the literal
// topic string, with After edges applied like any other step.
func TestEmitRecordsTopic(t *testing.T) {
	f := New[string]("CancelOrder")
	Emit(f, "order.cancelled", After("cancel"))

	require.Len(t, f.steps, 1)
	s := f.steps[0]
	require.Equal(t, "order.cancelled", s.name)
	require.Equal(t, KindEmit, s.kind)
	require.Equal(t, "Emit", s.constructor)
	require.Equal(t, "order.cancelled", s.emitTopic)
	require.Equal(t, []string{"cancel"}, s.dependsOn)
}

// TestJSONWrapperRoundTrip proves NewJSON's value round-trips through the
// core JSON codec transparently — no wrapper envelope on the wire.
func TestJSONWrapperRoundTrip(t *testing.T) {
	type refundResult struct {
		RefundID string `json:"refund_id"`
	}
	wrapped := NewJSON(refundResult{RefundID: "re_123"})

	b, err := wrapped.MarshalJSON()
	require.NoError(t, err)
	require.JSONEq(t, `{"refund_id":"re_123"}`, string(b))

	var decoded JSON[refundResult]
	require.NoError(t, decoded.UnmarshalJSON(b))
	require.Equal(t, "re_123", decoded.Value.RefundID)
}

// TestAttemptIdempotencyKey proves Attempt.IdempotencyKey() derives
// runID:stepName:attempt exactly, matching ACT-02.
func TestAttemptIdempotencyKey(t *testing.T) {
	att := Attempt{runID: "run-1", step: "refund", attempt: 3}
	require.Equal(t, "run-1:refund:3", att.IdempotencyKey())
}

// TestBackoffValidation proves Backoff validates maxAttempts >= 1 and
// initial <= max, panicking with an error value on violation.
func TestBackoffValidation(t *testing.T) {
	require.NotPanics(t, func() { Backoff(1, time.Second, time.Second) })

	requirePanicsWithError(t, func() { Backoff(0, time.Second, time.Minute) })
	requirePanicsWithError(t, func() { Backoff(3, time.Minute, time.Second) })
}

// TestRetryPolicyFixedSurface holds CORE-09's retry surface to exactly the
// three parameters Backoff exposes — no jitter knobs, no per-error-class
// policies.
func TestRetryPolicyFixedSurface(t *testing.T) {
	typ := reflect.TypeOf(RetryPolicy{})
	require.Equal(t, 3, typ.NumField())
}

// TestRetryOnDBStepPanics proves Retry is rejected on a DB step at
// declaration time — retry policies belong to Activities, not the
// transaction's own retry semantics.
func TestRetryOnDBStepPanics(t *testing.T) {
	f := New[string]("X")
	requirePanicsWithError(t, func() {
		UpdateSelf(f, "mutate", func(ctx context.Context, tx string, in string) (string, error) {
			return "", nil
		}, Retry(Backoff(3, time.Second, time.Minute)))
	})
}

// TestRetryRevalidatesBypassedPolicy proves Retry re-validates a RetryPolicy
// constructed directly (bypassing Backoff's own validation, possible because
// every RetryPolicy field is exported) rather than trusting whatever it is
// handed (WR-03).
func TestRetryRevalidatesBypassedPolicy(t *testing.T) {
	f := New[string]("Refund")
	requirePanicsWithError(t, func() {
		Activity(f, "refund",
			func(ctx context.Context, self string, att Attempt) (JSON[int], error) {
				return NewJSON(0), nil
			},
			Retry(RetryPolicy{MaxAttempts: 0, Initial: time.Hour, Max: time.Second}),
		)
	})
}

// TestRetryOnEmitStepPanics is TestRetryOnDBStepPanics's Emit-shaped twin:
// Retry's doc comment states retry policies apply to Activities only, so an
// Emit step (which never mutates the database and is not an Activity either)
// must be rejected at declaration time exactly like a DB step is (WR-02).
func TestRetryOnEmitStepPanics(t *testing.T) {
	f := New[string]("X")
	requirePanicsWithError(t, func() {
		Emit(f, "order.cancelled", Retry(Backoff(3, time.Second, time.Minute)))
	})
}

// TestActivityCompileFail proves CORE-04 is machine-checked, not asserted in
// prose: internal/testdata/compilefail/activity_tx.go declares an Activity
// closure with a transaction parameter, and building it must fail, naming
// Activity and reporting a signature mismatch.
func TestActivityCompileFail(t *testing.T) {
	cmd := exec.Command("go", "build", "./internal/testdata/compilefail/")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "expected internal/testdata/compilefail to fail to compile, output: %s", out)
	require.Contains(t, string(out), "Activity")
	require.Contains(t, string(out), "does not match")
}

// repoRoot returns the module root, so TestActivityCompileFail's `go build`
// invocation resolves ./internal/testdata/compilefail/ correctly regardless
// of the test binary's working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").CombinedOutput()
	require.NoError(t, err, "go list -m: %s", out)
	return strings.TrimSpace(string(out))
}
