package entflow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type order struct {
	ID string
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
	putResult(rs, "cancel", &order{ID: "1"})

	got, err := Result[*order](ctx, "nope")
	require.Nil(t, got)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnknownStep)
	require.Contains(t, err.Error(), "nope")
}

func TestResultTypeMismatch(t *testing.T) {
	ctx, rs := withResults(context.Background())
	putResult(rs, "cancel", &order{ID: "1"})

	got, err := Result[string](ctx, "cancel")
	require.Empty(t, got)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrResultTypeMismatch)
	require.Contains(t, err.Error(), "order")
	require.Contains(t, err.Error(), "string")
}

func TestResultTypedHit(t *testing.T) {
	ctx, rs := withResults(context.Background())
	want := &order{ID: "1"}
	putResult(rs, "cancel", want)

	got, err := Result[*order](ctx, "cancel")
	require.NoError(t, err)
	require.Same(t, want, got)
}

func TestResultSentinelsAreDistinct(t *testing.T) {
	ctx, rs := withResults(context.Background())
	putResult(rs, "cancel", &order{ID: "1"})

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
	putResult(rs, "cancel", nilOrder)

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
