package entflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type order struct {
	ID string
}

// orderWithSecret has an unexported field JSON cannot carry — the fixture
// TestResultUnexportedFieldDoesNotSurviveRoundTrip uses to make D-40's
// inertness contract an executable assertion rather than only a doc
// comment.
type orderWithSecret struct {
	ID     string
	secret string
}

func TestResultNoStore(t *testing.T) {
	ctx := context.Background()
	got, err := Result[string](ctx, "cancel")
	require.Empty(t, got)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoResultStore)
}

func TestResultUnknownStep(t *testing.T) {
	ctx, rs := withResults(context.Background())
	require.NoError(t, putResult(rs, "cancel", &order{ID: "1"}))

	got, err := Result[*order](ctx, "nope")
	require.Nil(t, got)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnknownStep)
	require.Contains(t, err.Error(), "nope")
}

// TestResultUnknownForSkippedStep proves the must_haves truth directly: a
// step skipped by a false condition never records a result, so a later step
// asking Result[T] for it gets ErrUnknownStep — the same sentinel a step
// name that never existed produces, since resultStore has no way to
// distinguish "skipped" from "never declared" and does not need to.
func TestResultUnknownForSkippedStep(t *testing.T) {
	type skipTx struct{}
	type skipIn struct{}

	f := New[*skipIn]("SkipFixture", WithSelfStatus(func(ctx context.Context, tx *skipTx, in *skipIn) (string, error) {
		return "actual-status", nil
	}))
	Step(f, "skipped", func(ctx context.Context, tx *skipTx, in *skipIn) (*order, error) {
		return &order{ID: "should-not-run"}, nil
	}, When(SelfWas("never-this-status")))

	var lookupErr error
	Step(f, "observer", func(ctx context.Context, tx *skipTx, in *skipIn) (*order, error) {
		_, lookupErr = Result[*order](ctx, "skipped")
		return &order{ID: "observer"}, nil
	}, After("skipped"))

	err := f.Exec(context.Background(), &skipTx{}, &skipIn{})
	require.NoError(t, err)
	require.ErrorIs(t, lookupErr, ErrUnknownStep)
}

// TestResultTypeMismatch's message names the step and the wanted type
// (Result[T]'s doc comment promise) — it can no longer name the ORIGINAL
// Go type the value was recorded under, because D-40 means the store holds
// only JSON bytes, not the live value order{} was boxed from.
func TestResultTypeMismatch(t *testing.T) {
	ctx, rs := withResults(context.Background())
	require.NoError(t, putResult(rs, "cancel", &order{ID: "1"}))

	got, err := Result[string](ctx, "cancel")
	require.Empty(t, got)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrResultTypeMismatch)
	require.Contains(t, err.Error(), "cancel")
	require.Contains(t, err.Error(), "string")
}

// TestResultTypedHit proves D-40 as a behavior change from Phase 1: a value
// obtained from Result[T] is equal to the value that produced it, but is
// NEVER the same pointer — the store holds a JSON round trip, not the live
// value, on every path, including this single uninterrupted in-process call.
func TestResultTypedHit(t *testing.T) {
	ctx, rs := withResults(context.Background())
	want := &order{ID: "1"}
	require.NoError(t, putResult(rs, "cancel", want))

	got, err := Result[*order](ctx, "cancel")
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.NotSame(t, want, got, "Result[T] must decode a fresh value from JSON (D-40) — never the same pointer as the live value that produced it")
}

// TestResultUnexportedFieldDoesNotSurviveRoundTrip makes D-40's inertness
// contract an executable assertion: an unexported field cannot be carried
// by encoding/json, so it silently does not survive rather than "appearing
// to work" the way a live in-memory value would.
func TestResultUnexportedFieldDoesNotSurviveRoundTrip(t *testing.T) {
	ctx, rs := withResults(context.Background())
	want := &orderWithSecret{ID: "1", secret: "do-not-persist"}
	require.NoError(t, putResult(rs, "cancel", want))

	got, err := Result[*orderWithSecret](ctx, "cancel")
	require.NoError(t, err)
	require.Equal(t, "1", got.ID)
	require.Empty(t, got.secret, "an unexported field must not survive the JSON round trip")
}

// TestResultRoundTripsAfterReload proves a result written by one claim is
// readable, and equal, after the run is reloaded in a different
// transaction: withResultsFrom is exactly the hydration worker/dbstep.go
// performs, via Runner.ExecStep, at the start of every later claim, from
// the run row's persisted results column — this test feeds it the same
// already-marshalled bytes a reload would produce, with no live value in
// sight.
func TestResultRoundTripsAfterReload(t *testing.T) {
	_, rs := withResults(context.Background())
	require.NoError(t, putResult(rs, "cancel", &order{ID: "1"}))

	rs.mu.Lock()
	persisted := map[string]json.RawMessage{"cancel": rs.results["cancel"]}
	rs.mu.Unlock()

	reloaded := withResultsFrom(context.Background(), persisted)

	got, err := Result[*order](reloaded, "cancel")
	require.NoError(t, err)
	require.Equal(t, &order{ID: "1"}, got)
}

func TestResultSentinelsAreDistinct(t *testing.T) {
	ctx, rs := withResults(context.Background())
	require.NoError(t, putResult(rs, "cancel", &order{ID: "1"}))

	_, unknownErr := Result[*order](ctx, "nope")
	require.ErrorIs(t, unknownErr, ErrUnknownStep)
	require.False(t, errors.Is(unknownErr, ErrResultTypeMismatch))

	_, mismatchErr := Result[string](ctx, "cancel")
	require.ErrorIs(t, mismatchErr, ErrResultTypeMismatch)
	require.False(t, errors.Is(mismatchErr, ErrUnknownStep))
}

func TestResultNeverPanicsOnNilStoredValue(t *testing.T) {
	ctx, rs := withResults(context.Background())
	var nilOrder *order
	require.NoError(t, putResult(rs, "cancel", nilOrder))

	require.NotPanics(t, func() {
		got, err := Result[*order](ctx, "cancel")
		require.NoError(t, err)
		require.Nil(t, got)
	})
}

func TestStepErrorUnwrap(t *testing.T) {
	stepErr := &StepError{Step: "cancel", Kind: KindDB, Err: ErrNotProvided}
	require.True(t, errors.Is(stepErr, ErrNotProvided))
}

func TestStepErrorAsRecoversFields(t *testing.T) {
	orig := &StepError{Step: "cancel", Kind: KindActivity, Err: errors.New("boom")}
	wrapped := fmt.Errorf("wrapping: %w", orig)

	var got *StepError
	require.True(t, errors.As(wrapped, &got))
	require.Equal(t, "cancel", got.Step)
	require.Equal(t, KindActivity, got.Kind)
}

func TestErrRequiresDurableRunSentinel(t *testing.T) {
	require.Error(t, ErrRequiresDurableRun)
}
