package entflow_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/ent/schema"
	"github.com/smintz/entflow/internal/testdata/entclient"
)

// txCounter is a transaction double satisfying entflow.Tx that never
// touches a real database — used to prove Exec never calls Commit or
// Rollback (D-08), and as the TX type for step closures that don't need a
// real ent client.
type txCounter struct {
	commits   int
	rollbacks int
}

func (t *txCounter) Commit() error   { t.commits++; return nil }
func (t *txCounter) Rollback() error { t.rollbacks++; return nil }

// txCounterOpener adapts a *txCounter to entflow.TxOpener[*txCounter].
type txCounterOpener struct {
	tx txCounter
}

func (o *txCounterOpener) Tx(ctx context.Context) (*txCounter, error) {
	return &o.tx, nil
}

// failingRollbackTx is a transaction double whose Rollback always errors,
// used to prove RunInTx joins a rollback failure into a panic that escapes
// Exec.
type failingRollbackTx struct {
	commits   int
	rollbacks int
}

func (t *failingRollbackTx) Commit() error   { t.commits++; return nil }
func (t *failingRollbackTx) Rollback() error { t.rollbacks++; return errors.New("rollback failed") }

type failingRollbackOpener struct {
	tx failingRollbackTx
}

func (o *failingRollbackOpener) Tx(ctx context.Context) (*failingRollbackTx, error) {
	return &o.tx, nil
}

// unregisteredService is never Provide'd on any registry in this file — it
// exists solely to trigger Use[T]'s ErrNotProvided panic.
type unregisteredService struct{}

// TestRequiresDurableRunCancelOrderFlow proves the intended Phase 1 demo:
// the real worked-example CancelOrder flow (entflow.md §3.1, in full, with
// its refund Activity and order.cancelled Emit) is declarable and
// describable, but Exec refuses it with ErrRequiresDurableRun before
// executing anything, and the seeded order's status is left untouched.
func TestRequiresDurableRunCancelOrderFlow(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)

	seeded, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
	require.NoError(t, err)

	flows := entflow.FlowsOf(schema.Order{})
	require.Len(t, flows, 1)
	flow, ok := flows[0].(*entflow.FlowOf[*schema.CancelOrderRequest])
	require.True(t, ok, "expected *entflow.FlowOf[*schema.CancelOrderRequest], got %T", flows[0])

	tx, err := client.Tx(ctx)
	require.NoError(t, err)

	err = flow.Exec(ctx, tx, &schema.CancelOrderRequest{OrderID: seeded.ID})
	require.Error(t, err)
	require.ErrorIs(t, err, entflow.ErrRequiresDurableRun)

	require.NoError(t, tx.Rollback())

	got, err := client.Order.Get(ctx, seeded.ID)
	require.NoError(t, err)
	require.Equal(t, order.StatusPaid, got.Status, "no step should have executed")
}

// TestRequiresDurableRunNoStepInvoked proves the refusal happens before any
// step's closure runs — not merely before commit — using invocation
// counters on both a DB step and an Activity step.
func TestRequiresDurableRunNoStepInvoked(t *testing.T) {
	var dbInvocations, activityInvocations int

	f := entflow.New[int]("DurableOnly")
	entflow.UpdateSelf(f, "mutate", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		dbInvocations++
		return in, nil
	})
	entflow.Activity(f, "call", func(ctx context.Context, self int, att entflow.Attempt) (entflow.JSON[int], error) {
		activityInvocations++
		return entflow.NewJSON(0), nil
	})

	tx := &txCounter{}
	err := f.Exec(context.Background(), tx, 1)
	require.Error(t, err)
	require.ErrorIs(t, err, entflow.ErrRequiresDurableRun)
	require.Equal(t, 0, dbInvocations)
	require.Equal(t, 0, activityInvocations)
	require.Equal(t, 0, tx.commits)
	require.Equal(t, 0, tx.rollbacks)
}

// TestExecDoesNotCallCommitOrRollback proves Exec leaves the transaction
// untouched for the caller (D-08) even on a successful DB-only run.
func TestExecDoesNotCallCommitOrRollback(t *testing.T) {
	f := entflow.New[int]("Plain")
	entflow.UpdateSelf(f, "mutate", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		return in, nil
	})

	tx := &txCounter{}
	err := f.Exec(context.Background(), tx, 1)
	require.NoError(t, err)
	require.Equal(t, 0, tx.commits)
	require.Equal(t, 0, tx.rollbacks)
}

// TestAfterOrdersStepsByDependency proves steps declared out of order but
// wired with After execute in dependency order.
func TestAfterOrdersStepsByDependency(t *testing.T) {
	var ranOrder []string

	f := entflow.New[int]("Ordering")
	entflow.UpdateSelf(f, "b", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		ranOrder = append(ranOrder, "b")
		return in, nil
	}, entflow.After("a"))
	entflow.UpdateSelf(f, "a", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		ranOrder = append(ranOrder, "a")
		return in, nil
	})
	entflow.UpdateSelf(f, "c", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		ranOrder = append(ranOrder, "c")
		return in, nil
	})

	err := f.Exec(context.Background(), &txCounter{}, 1)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, ranOrder)
}

// TestAfterUnknownDependencyErrors proves an After edge naming a step that
// does not exist returns an error naming the missing step, before any step
// executes.
func TestAfterUnknownDependencyErrors(t *testing.T) {
	var ran bool
	f := entflow.New[int]("Unknown")
	entflow.UpdateSelf(f, "a", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		ran = true
		return in, nil
	}, entflow.After("missing"))

	err := f.Exec(context.Background(), &txCounter{}, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing")
	require.False(t, ran)
}

// TestTopoOrderCycleDetected proves a cycle in the After edges returns an
// error naming the participating steps, before any step executes.
func TestTopoOrderCycleDetected(t *testing.T) {
	var ran bool
	f := entflow.New[int]("Cycle")
	entflow.UpdateSelf(f, "a", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		ran = true
		return in, nil
	}, entflow.After("b"))
	entflow.UpdateSelf(f, "b", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		ran = true
		return in, nil
	}, entflow.After("a"))

	err := f.Exec(context.Background(), &txCounter{}, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "a")
	require.Contains(t, err.Error(), "b")
	require.False(t, ran)
}

// TestWhenSelfWasSkipsWhenMismatched proves When(SelfWas(...)) skips the
// step, and records no result for it, when the snapshot doesn't match. The
// assertion reads Result[int] from a ctx captured live from inside a later
// step's closure — the same ctx Exec's internal result store was derived
// onto — rather than a freshly-constructed context.Background(), which would
// carry no result store regardless of what Exec did and so would pass
// vacuously even if the "skipped step records no result" invariant were
// broken (WR-06; see TestResultAccessibleAcrossSteps for the same
// live-ctx-capture pattern used correctly for the cross-step-read case).
func TestWhenSelfWasSkipsWhenMismatched(t *testing.T) {
	var ran bool
	var observerCtx context.Context
	f := entflow.New[int]("Guarded", entflow.WithSelfStatus(func(ctx context.Context, tx *txCounter, in int) (string, error) {
		return "draft", nil
	}))
	entflow.UpdateSelf(f, "guarded", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		ran = true
		return in, nil
	}, entflow.When(entflow.SelfWas("paid")))
	entflow.UpdateSelf(f, "observer", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		observerCtx = ctx
		return in, nil
	}, entflow.After("guarded"))

	err := f.Exec(context.Background(), &txCounter{}, 1)
	require.NoError(t, err)
	require.False(t, ran)

	require.NotNil(t, observerCtx, "observer step must have run and captured Exec's live ctx")
	_, resultErr := entflow.Result[int](observerCtx, "guarded")
	require.Error(t, resultErr, "a skipped step must record no result")
	require.ErrorIs(t, resultErr, entflow.ErrUnknownStep)
}

// TestWhenSelfWasRunsWhenMatched is TestWhenSelfWasSkipsWhenMismatched's
// matching-snapshot twin.
func TestWhenSelfWasRunsWhenMatched(t *testing.T) {
	var ran bool
	f := entflow.New[int]("Guarded", entflow.WithSelfStatus(func(ctx context.Context, tx *txCounter, in int) (string, error) {
		return "paid", nil
	}))
	entflow.UpdateSelf(f, "guarded", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		ran = true
		return in, nil
	}, entflow.When(entflow.SelfWas("paid")))

	err := f.Exec(context.Background(), &txCounter{}, 1)
	require.NoError(t, err)
	require.True(t, ran)
}

// TestWhenConditionIsDataOnly proves Condition is a data-only struct: no
// field's reflect.Kind is Func, so Plan 04's metadata path can render it
// without evaluating anything.
func TestWhenConditionIsDataOnly(t *testing.T) {
	typ := reflect.TypeOf(entflow.Condition{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		require.NotEqual(t, reflect.Func, field.Type.Kind(), "field %s must not be a func", field.Name)
	}
}

// TestSelfWasSnapshotIsEntryValue proves the snapshot is read once, before
// any step runs, and a step that mutates the status does not change what a
// later SelfWas sees.
func TestSelfWasSnapshotIsEntryValue(t *testing.T) {
	status := "draft"
	f := entflow.New[int]("Snapshot", entflow.WithSelfStatus(func(ctx context.Context, tx *txCounter, in int) (string, error) {
		return status, nil
	}))
	entflow.UpdateSelf(f, "mutate", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		status = "cancelled" // mutates the "live" status after the snapshot was taken
		return in, nil
	})
	var secondRan bool
	entflow.UpdateSelf(f, "guarded", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		secondRan = true
		return in, nil
	}, entflow.After("mutate"), entflow.When(entflow.SelfWas("draft")))

	err := f.Exec(context.Background(), &txCounter{}, 1)
	require.NoError(t, err)
	require.True(t, secondRan, "guarded step must run against the ENTRY snapshot, not the mutated live value")
}

// TestSelfWasWithoutReaderErrors proves a flow using When(SelfWas(...)) with
// no WithSelfStatus option returns a declaration error naming the missing
// option.
func TestSelfWasWithoutReaderErrors(t *testing.T) {
	f := entflow.New[int]("NoReader")
	entflow.UpdateSelf(f, "guarded", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		return in, nil
	}, entflow.When(entflow.SelfWas("paid")))

	err := f.Exec(context.Background(), &txCounter{}, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "WithSelfStatus")
}

// TestNewPanicsOnSelfStatusInTypeMismatch proves New[In] eagerly rejects a
// WithSelfStatus reader declared for a different input type at the flow's
// construction site — the same declaration-time protection WithCodec already
// gets via resolveCodec's type assertion (WR-04). Before the fix, this
// mismatch was invisible until a SelfWas-gated step actually ran and hit the
// reader's own runtime type-assertion error.
func TestNewPanicsOnSelfStatusInTypeMismatch(t *testing.T) {
	badOpt := entflow.WithSelfStatus(func(ctx context.Context, tx *txCounter, in string) (string, error) {
		return "draft", nil
	})

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		entflow.New[int]("Mismatched", badOpt)
	}()

	require.NotNil(t, recovered, "expected New to panic eagerly on a WithSelfStatus In-type mismatch")
	err, ok := recovered.(error)
	require.True(t, ok, "recovered panic value must be an error, got %T", recovered)
	require.Contains(t, err.Error(), "WithSelfStatus")
}

// TestRecoverStepPanicYieldsStepError proves a panic inside a step closure,
// with an arbitrary value, is recovered at that step's boundary and
// returned as a *StepError whose Step and Kind match, with a non-empty
// Stack.
func TestRecoverStepPanicYieldsStepError(t *testing.T) {
	f := entflow.New[int]("Panicky")
	entflow.UpdateSelf(f, "boom", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		panic("arbitrary panic value")
	})

	err := f.Exec(context.Background(), &txCounter{}, 1)
	require.Error(t, err)

	var stepErr *entflow.StepError
	require.ErrorAs(t, err, &stepErr)
	require.Equal(t, "boom", stepErr.Step)
	require.Equal(t, entflow.KindDB, stepErr.Kind)
	require.NotEmpty(t, stepErr.Stack)
}

// TestRecoverUsePanicSurfacesAsNotProvided proves Use[T]'s panic on a
// missing provider is recovered at the step's boundary and its error
// identity survives: errors.Is(err, ErrNotProvided) still resolves after
// conversion to a *StepError.
func TestRecoverUsePanicSurfacesAsNotProvided(t *testing.T) {
	reg := entflow.NewRegistry()
	ctx := entflow.WithRegistry(context.Background(), reg)

	f := entflow.New[int]("DIFailure")
	entflow.UpdateSelf(f, "needsService", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		entflow.Use[*unregisteredService](ctx)
		return in, nil
	})

	err := f.Exec(ctx, &txCounter{}, 1)
	require.Error(t, err)
	require.ErrorIs(t, err, entflow.ErrNotProvided)

	var stepErr *entflow.StepError
	require.ErrorAs(t, err, &stepErr)
	require.Equal(t, "needsService", stepErr.Step)
}

// TestResultAccessibleAcrossSteps proves a later step reads an earlier
// step's typed result through entflow.Result[T](ctx, "step") during a real
// execution.
func TestResultAccessibleAcrossSteps(t *testing.T) {
	f := entflow.New[int]("ResultChain")
	entflow.UpdateSelf(f, "cancel", func(ctx context.Context, tx *txCounter, in int) (string, error) {
		return "cancelled-value", nil
	})

	var got string
	entflow.UpdateSelf(f, "readResult", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		v, err := entflow.Result[string](ctx, "cancel")
		require.NoError(t, err)
		got = v
		return in, nil
	}, entflow.After("cancel"))

	err := f.Exec(context.Background(), &txCounter{}, 1)
	require.NoError(t, err)
	require.Equal(t, "cancelled-value", got)
}

// TestRunInTxCommitsAndIsReadable proves RunInTx commits on success and the
// mutation is readable through the non-transactional client afterwards.
// This flow is declared locally, not via the fixture schema's CancelOrder
// (which now requires a durable run per D-13) — a plain DB-only flow is
// what proves RunInTx's commit/rollback semantics.
func TestRunInTxCommitsAndIsReadable(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)

	seeded, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
	require.NoError(t, err)

	f := entflow.New[int]("CommitProof")
	entflow.UpdateSelf(f, "cancel", func(ctx context.Context, tx *ent.Tx, in int) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in).SetStatus(order.StatusCancelled).Save(ctx)
	})

	err = entflow.RunInTx(ctx, f, client, seeded.ID)
	require.NoError(t, err)

	got, err := client.Order.Get(ctx, seeded.ID)
	require.NoError(t, err)
	require.Equal(t, order.StatusCancelled, got.Status)
}

// TestRunInTxRollsBackOnStepError proves RunInTx rolls back on any step
// error and the mutation is NOT readable afterwards — the all-or-nothing
// guarantee, proven against the real database.
func TestRunInTxRollsBackOnStepError(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)

	seeded, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
	require.NoError(t, err)

	f := entflow.New[int]("RollbackProof")
	entflow.UpdateSelf(f, "cancel", func(ctx context.Context, tx *ent.Tx, in int) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in).SetStatus(order.StatusCancelled).Save(ctx)
	})
	entflow.Check(f, "fail", func(ctx context.Context, tx *ent.Tx, in int) error {
		return errors.New("boom")
	}, entflow.After("cancel"))

	err = entflow.RunInTx(ctx, f, client, seeded.ID)
	require.Error(t, err)

	got, err := client.Order.Get(ctx, seeded.ID)
	require.NoError(t, err)
	require.Equal(t, order.StatusPaid, got.Status, "rollback must undo the cancel step's mutation")
}

// TestRunInTxRollsBackAndRepanicsOnEscapedPanic proves RunInTx rolls back
// and re-panics on a panic that escapes Exec (here, a WithSelfStatus reader
// panicking — a path outside runStep's per-step recover boundary).
func TestRunInTxRollsBackAndRepanicsOnEscapedPanic(t *testing.T) {
	f := entflow.New[int]("EscapedPanic", entflow.WithSelfStatus(func(ctx context.Context, tx *txCounter, in int) (string, error) {
		panic("reader exploded")
	}))
	entflow.UpdateSelf(f, "guarded", func(ctx context.Context, tx *txCounter, in int) (int, error) {
		return in, nil
	}, entflow.When(entflow.SelfWas("paid")))

	opener := &txCounterOpener{}

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = entflow.RunInTx(context.Background(), f, opener, 1)
	}()

	require.NotNil(t, recovered, "expected RunInTx to re-panic")
	require.Equal(t, 1, opener.tx.rollbacks)
	require.Equal(t, 0, opener.tx.commits)
}

// TestRunInTxJoinsRollbackErrorWhenPanicAndRollbackBothFail proves that
// when a panic escapes Exec AND the rollback triggered to handle it also
// fails, the rollback error is joined into what's re-panicked rather than
// either failure being discarded.
func TestRunInTxJoinsRollbackErrorWhenPanicAndRollbackBothFail(t *testing.T) {
	f := entflow.New[int]("DoubleFailure", entflow.WithSelfStatus(func(ctx context.Context, tx *failingRollbackTx, in int) (string, error) {
		panic("reader exploded")
	}))
	entflow.UpdateSelf(f, "guarded", func(ctx context.Context, tx *failingRollbackTx, in int) (int, error) {
		return in, nil
	}, entflow.When(entflow.SelfWas("paid")))

	opener := &failingRollbackOpener{}

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = entflow.RunInTx(context.Background(), f, opener, 1)
	}()

	require.NotNil(t, recovered)
	err, ok := recovered.(error)
	require.True(t, ok, "expected the re-panicked value to be an error carrying both failures")
	require.Contains(t, err.Error(), "reader exploded")
	require.Contains(t, err.Error(), "rollback failed")
}
