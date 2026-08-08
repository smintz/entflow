package entflow_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
)

type widget struct{ name string }

type refunder interface {
	Refund() string
}

type stripeClient struct{}

func (*stripeClient) Refund() string { return "refunded" }

func TestProvideUseRoundTrip(t *testing.T) {
	reg := entflow.NewRegistry()
	w := &widget{name: "gadget"}
	entflow.Provide(reg, w)

	ctx := entflow.WithRegistry(context.Background(), reg)
	got := entflow.Use[*widget](ctx)
	require.Same(t, w, got)
}

func TestRegistriesAreIndependent(t *testing.T) {
	regA := entflow.NewRegistry()
	regB := entflow.NewRegistry()

	entflow.Provide(regA, &widget{name: "a"})

	ctxA := entflow.WithRegistry(context.Background(), regA)
	ctxB := entflow.WithRegistry(context.Background(), regB)

	got := entflow.Use[*widget](ctxA)
	require.Equal(t, "a", got.name)

	_, err := entflow.TryUse[*widget](ctxB)
	require.Error(t, err)
	require.ErrorIs(t, err, entflow.ErrNotProvided)
}

func TestTryUseNoRegistry(t *testing.T) {
	ctx := context.Background()
	got, err := entflow.TryUse[*widget](ctx)
	require.Nil(t, got)
	require.Error(t, err)
	require.ErrorIs(t, err, entflow.ErrNoRegistry)
}

func TestTryUseNotProvided(t *testing.T) {
	reg := entflow.NewRegistry()
	ctx := entflow.WithRegistry(context.Background(), reg)

	got, err := entflow.TryUse[*widget](ctx)
	require.Nil(t, got)
	require.Error(t, err)
	require.ErrorIs(t, err, entflow.ErrNotProvided)
}

func TestUsePanicsWithNoRegistry(t *testing.T) {
	ctx := context.Background()

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		entflow.Use[*widget](ctx)
	}()

	require.NotNil(t, recovered)
	err, ok := recovered.(error)
	require.True(t, ok, "recovered panic value must be an error, got %T", recovered)
	require.True(t, errors.Is(err, entflow.ErrNoRegistry))
}

func TestUsePanicsWithNotProvided(t *testing.T) {
	reg := entflow.NewRegistry()
	ctx := entflow.WithRegistry(context.Background(), reg)

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		entflow.Use[*widget](ctx)
	}()

	require.NotNil(t, recovered)
	err, ok := recovered.(error)
	require.True(t, ok, "recovered panic value must be an error, got %T", recovered)
	require.True(t, errors.Is(err, entflow.ErrNotProvided))
}

func TestProvideInterfaceType(t *testing.T) {
	reg := entflow.NewRegistry()
	entflow.Provide[refunder](reg, &stripeClient{})

	ctx := entflow.WithRegistry(context.Background(), reg)
	got := entflow.Use[refunder](ctx)
	require.Equal(t, "refunded", got.Refund())

	// The concrete type must NOT be independently retrievable — Provide keys
	// on the type parameter T, not on the dynamic type of v.
	_, err := entflow.TryUse[*stripeClient](ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, entflow.ErrNotProvided)
}

func TestRegistryConcurrentAccess(t *testing.T) {
	reg := entflow.NewRegistry()
	ctx := entflow.WithRegistry(context.Background(), reg)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			entflow.Provide(reg, &widget{name: fmt.Sprintf("w-%d", i)})
		}()
		go func() {
			defer wg.Done()
			_, _ = entflow.TryUse[*widget](ctx)
		}()
	}
	wg.Wait()

	got := entflow.Use[*widget](ctx)
	require.NotNil(t, got)
}
