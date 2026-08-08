package entflow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow/internal/testdata/ent"
	entorder "github.com/smintz/entflow/internal/testdata/ent/order"
)

// TestDBStepAllSevenConstructors declares one step per constructor against
// the fixture ent client's generated types with no explicit generic type
// arguments at any call site, and asserts each records a distinct
// constructor string equal to its exported name, and the DB kind.
func TestDBStepAllSevenConstructors(t *testing.T) {
	f := New[int]("AllConstructors")

	Step(f, "step1", func(ctx context.Context, tx *ent.Tx, in int) (*ent.Order, error) {
		return tx.Order.Create().SetStatus(entorder.StatusDraft).Save(ctx)
	})
	CreateSelf(f, "createSelf", func(ctx context.Context, tx *ent.Tx, in int) (*ent.Order, error) {
		return tx.Order.Create().SetStatus(entorder.StatusDraft).Save(ctx)
	})
	UpdateSelf(f, "updateSelf", func(ctx context.Context, tx *ent.Tx, in int) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in).SetStatus(entorder.StatusPending).Save(ctx)
	})
	Create(f, "create", func(ctx context.Context, tx *ent.Tx, in int) (*ent.Order, error) {
		return tx.Order.Create().SetStatus(entorder.StatusDraft).Save(ctx)
	})
	Update(f, "update", func(ctx context.Context, tx *ent.Tx, in int) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in).SetStatus(entorder.StatusPaid).Save(ctx)
	})
	Query(f, "query", func(ctx context.Context, tx *ent.Tx, in int) (*ent.Order, error) {
		return tx.Order.Get(ctx, in)
	})
	Check(f, "check", func(ctx context.Context, tx *ent.Tx, in int) error {
		_, err := tx.Order.Get(ctx, in)
		return err
	})

	require.Len(t, f.steps, 7)

	wantConstructors := []string{"Step", "CreateSelf", "UpdateSelf", "Create", "Update", "Query", "Check"}
	for i, want := range wantConstructors {
		s := f.steps[i]
		require.Equal(t, want, s.constructor, "step %d", i)
		require.Equal(t, KindDB, s.kind, "step %d (%s)", i, want)
		require.NotNil(t, s.run, "step %d (%s)", i, want)
	}
}

// TestDBStepTxTypeMismatch proves every DB-step adapter uses the comma-ok
// assertion form: invoking a step's run with the wrong tx type returns a
// descriptive error naming both types rather than panicking.
func TestDBStepTxTypeMismatch(t *testing.T) {
	f := New[int]("Mismatch")
	UpdateSelf(f, "mutate", func(ctx context.Context, tx *ent.Tx, in int) (*ent.Order, error) {
		return nil, nil
	})

	s := f.steps[0]
	_, err := s.run(context.Background(), "not-a-tx", 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mutate")
	require.Contains(t, err.Error(), "UpdateSelf")
}

// TestDBStepCheckReturnsNilResult proves Check's closure returns only an
// error, and its adapter returns a nil result value alongside it so the
// executor's result-recording path stays uniform.
func TestDBStepCheckReturnsNilResult(t *testing.T) {
	f := New[int]("CheckOnly")
	Check(f, "guard", func(ctx context.Context, tx *entTxStub, in int) error {
		return nil
	})

	s := f.steps[0]
	result, err := s.run(context.Background(), &entTxStub{}, 1)
	require.NoError(t, err)
	require.Nil(t, result)
}

// entTxStub is a minimal stand-in transaction type for tests that don't need
// a real ent client.
type entTxStub struct{}

// TestDuplicateStepNamePanics proves declaring two steps with the same name
// in one flow panics at declaration time with a recovered value that is an
// error.
func TestDuplicateStepNamePanics(t *testing.T) {
	f := New[int]("Dup")
	UpdateSelf(f, "same", func(ctx context.Context, tx *entTxStub, in int) (*entTxStub, error) {
		return nil, nil
	})

	requirePanicsWithError(t, func() {
		Query(f, "same", func(ctx context.Context, tx *entTxStub, in int) (*entTxStub, error) {
			return nil, nil
		})
	})
}

// TestReadOnlyTransitionOnQueryPanics proves declaring Transition(...) on a
// Query step panics at declaration time with a recovered value that is an
// error — a read-only step cannot legally claim a status transition.
func TestReadOnlyTransitionOnQueryPanics(t *testing.T) {
	f := New[int]("ReadOnly")
	requirePanicsWithError(t, func() {
		Query(f, "q", func(ctx context.Context, tx *entTxStub, in int) (*entTxStub, error) {
			return nil, nil
		}, Transition("cancelled"))
	})
}

// TestReadOnlyTransitionOnCheckPanics is
// TestReadOnlyTransitionOnQueryPanics's Check-shaped twin.
func TestReadOnlyTransitionOnCheckPanics(t *testing.T) {
	f := New[int]("ReadOnly")
	requirePanicsWithError(t, func() {
		Check(f, "c", func(ctx context.Context, tx *entTxStub, in int) error {
			return nil
		}, Transition("cancelled"))
	})
}

// TestDBStepEmptyNamePanics proves a step declared with an empty name
// panics at declaration time.
func TestDBStepEmptyNamePanics(t *testing.T) {
	f := New[int]("Empty")
	requirePanicsWithError(t, func() {
		UpdateSelf(f, "", func(ctx context.Context, tx *entTxStub, in int) (*entTxStub, error) {
			return nil, nil
		})
	})
}

// requirePanicsWithError calls fn and requires that it panicked with a
// recovered value that is an error (D-16's discipline: declaration-time
// programming mistakes panic with an error value, never a bare string).
// Shared by every _test.go file in this package that exercises a
// declaration-time panic.
func requirePanicsWithError(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected a panic")
		_, ok := r.(error)
		require.True(t, ok, "expected recovered value to be an error, got %T: %v", r, r)
	}()
	fn()
}
