package entflow_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/entclient"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
	"github.com/smintz/entflow/worker"
)

// selfLoaderInput is the input type this file's local fixture flows share —
// deliberately independent of schema.CancelOrderRequest, mirroring
// durablerun_test.go's fixtureInput convention.
type selfLoaderInput struct {
	OrderID int
}

// newSelfLoaderMultiStepFlow declares a two-DB-step flow, sharing the
// CancelOrderFlowRun table the same way durablerun_test.go's
// newMultiStepFlow does, whose second step reads Self[*ent.Order] and
// records the status it observed onto payment_intent_id — the only channel
// available to prove what a step, executing inside its own worker claim and
// transaction, actually saw.
func newSelfLoaderMultiStepFlow() *entflow.FlowOf[*selfLoaderInput] {
	f := entflow.New[*selfLoaderInput]("SelfLoaderMultiStep",
		entflow.WithOwnerRef(func(in *selfLoaderInput) (int, error) {
			return in.OrderID, nil
		}),
		entflow.WithSelfLoader(func(ctx context.Context, tx *ent.Tx, in *selfLoaderInput) (*ent.Order, error) {
			return tx.Order.Get(ctx, in.OrderID)
		}),
	)
	entflow.Step(f, "mutate", func(ctx context.Context, tx *ent.Tx, in *selfLoaderInput) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).SetStatus(order.StatusPaid).Save(ctx)
	})
	entflow.Step(f, "observe", func(ctx context.Context, tx *ent.Tx, in *selfLoaderInput) (*ent.Order, error) {
		self, err := entflow.Self[*ent.Order](ctx)
		if err != nil {
			return nil, err
		}
		return tx.Order.UpdateOneID(in.OrderID).SetPaymentIntentID("observed:" + string(self.Status)).Save(ctx)
	}, entflow.After("mutate"))
	return f
}

// newNoSelfLoaderFlow declares no WithSelfLoader at all — its one step calls
// Self[T] anyway, to prove the resulting error rather than a panic. Its step
// is deliberately named "cancel", not "observe": since plan 02-04 (Task 3),
// a step closure's error fails the run to a failed:<step> state, and this
// flow shares the CancelOrderFlowRun table (entflowfixture.New) with the
// real CancelOrder flow — whose RunMixin-derived `state` enum only contains
// failed:<step> values for CancelOrder's OWN declared step names (cancel,
// refund, order.cancelled). "cancel" is the one name every fixture flow
// sharing this table can safely fail under.
func newNoSelfLoaderFlow() *entflow.FlowOf[*selfLoaderInput] {
	f := entflow.New[*selfLoaderInput]("NoSelfLoader",
		entflow.WithOwnerRef(func(in *selfLoaderInput) (int, error) {
			return in.OrderID, nil
		}),
	)
	entflow.Step(f, "cancel", func(ctx context.Context, tx *ent.Tx, in *selfLoaderInput) (*ent.Order, error) {
		if _, err := entflow.Self[*ent.Order](ctx); err != nil {
			return nil, err
		}
		return tx.Order.Get(ctx, in.OrderID)
	})
	return f
}

// newSelfWasSnapshotFlow declares a two-DB-step flow whose second step is
// gated on the ENTRY-time status snapshot ("paid"), while its first step
// mutates the entity's live status away from that value ("shipped") before
// the gated step's own claim ever runs. If the gate evaluated against a
// live re-read (a bug) it would see "shipped" and skip; the entry snapshot
// must keep it true.
func newSelfWasSnapshotFlow() *entflow.FlowOf[*selfLoaderInput] {
	f := entflow.New[*selfLoaderInput]("SelfWasSnapshot",
		entflow.WithOwnerRef(func(in *selfLoaderInput) (int, error) {
			return in.OrderID, nil
		}),
		entflow.WithSelfStatus(func(ctx context.Context, tx *ent.Tx, in *selfLoaderInput) (string, error) {
			row, err := tx.Order.Get(ctx, in.OrderID)
			if err != nil {
				return "", err
			}
			return string(row.Status), nil
		}),
	)
	entflow.Step(f, "mutate", func(ctx context.Context, tx *ent.Tx, in *selfLoaderInput) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).SetStatus(order.StatusShipped).Save(ctx)
	})
	entflow.Step(f, "gated", func(ctx context.Context, tx *ent.Tx, in *selfLoaderInput) (*ent.Order, error) {
		return tx.Order.UpdateOneID(in.OrderID).SetPaymentIntentID("gated-ran").Save(ctx)
	}, entflow.When(entflow.SelfWas("paid")), entflow.After("mutate"))
	return f
}

func newSelfLoaderWorker(t *testing.T, eng *entflow.Engine) *worker.Worker {
	t.Helper()
	w, err := worker.New(eng, worker.Options{
		Dialect:       "sqlite",
		Concurrency:   1,
		ClaimStrategy: worker.SQLiteStrategy(),
	})
	require.NoError(t, err)
	return w
}

// TestSelfIsLiveAcrossClaims proves D-41: Self[T] inside a step observes a
// mutation committed by an EARLIER step of the same run, from an EARLIER
// claim — a fresh, live re-read inside the current claim's own transaction,
// never a cached or persisted value.
func TestSelfIsLiveAcrossClaims(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	f := newSelfLoaderMultiStepFlow()
	require.NoError(t, eng.Register(f, store))

	owner, err := client.Order.Create().SetStatus(order.StatusDraft).Save(ctx)
	require.NoError(t, err)

	_, err = entflow.Start(ctx, eng, f, &selfLoaderInput{OrderID: owner.ID})
	require.NoError(t, err)

	w := newSelfLoaderWorker(t, eng)

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed, "first claim (mutate) should succeed")

	claimed, err = w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed, "second claim (observe) should succeed")

	finalOrder, err := client.Order.Get(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, "observed:paid", finalOrder.PaymentIntentID,
		"step two's Self[*ent.Order] must observe step one's mutation, committed in an earlier claim")
}

// TestSelfNoLoaderReturnsErrNoSelfLoader proves Self[T] on a context that
// was never given a self value — the case for a flow that declares no
// WithSelfLoader at all — returns ErrNoSelfLoader rather than panicking.
func TestSelfNoLoaderReturnsErrNoSelfLoader(t *testing.T) {
	_, err := entflow.Self[*ent.Order](context.Background())
	require.ErrorIs(t, err, entflow.ErrNoSelfLoader)
}

// TestFlowLoadSelfWithoutDeclaredLoaderReturnsErrNoSelfLoader proves the
// same guarantee one layer down, at the Runner.LoadSelf seam worker/
// dbstep.go calls: a flow with no declared WithSelfLoader reports
// ErrNoSelfLoader from LoadSelf itself, which worker/dbstep.go treats as
// "Self[T] unavailable this claim" rather than a claim failure (see
// worker/retry_test.go for the DUR-07-era proof that a step's OWN call to
// Self[T] failing that way now fails the RUN, not the worker's Go error
// return — a step failure is a domain outcome recorded on the run row,
// exactly like a successful advance).
func TestFlowLoadSelfWithoutDeclaredLoaderReturnsErrNoSelfLoader(t *testing.T) {
	f := newNoSelfLoaderFlow()
	input, err := json.Marshal(&selfLoaderInput{OrderID: 1})
	require.NoError(t, err)

	_, err = f.LoadSelf(context.Background(), nil, input)
	require.ErrorIs(t, err, entflow.ErrNoSelfLoader)
}

// TestNewPanicsOnSelfLoaderTypeMismatch proves WithSelfLoader's WR-04
// declaration-time guarantee: a reader declared against a different input
// type panics from New, not deferred to the first claim that reaches it.
func TestNewPanicsOnSelfLoaderTypeMismatch(t *testing.T) {
	type otherInput struct {
		Foo string
	}
	require.Panics(t, func() {
		entflow.New[*selfLoaderInput]("Mismatch",
			entflow.WithSelfLoader(func(ctx context.Context, tx *ent.Tx, in *otherInput) (*ent.Order, error) {
				return nil, nil
			}),
		)
	})
}

// TestSelfWasSnapshotSurvivesEntityMutationAndReload proves the deliberate
// asymmetry between Self[T] (live, above) and a SelfWas condition (D-10/
// D-42, a snapshot frozen at flow entry): the gated step's own claim runs
// AFTER the entity's live status has already changed, in a SEPARATE
// transaction from the one that took the snapshot — the gate must still
// evaluate true against the persisted entry value, proving the snapshot was
// read from the run row's self_was column across the reload, not
// recomputed against the now-different live status.
func TestSelfWasSnapshotSurvivesEntityMutationAndReload(t *testing.T) {
	ctx := entflowfixture.WithViewer(context.Background(), entflowfixture.AdminViewer())
	client := entclient.New(t)
	store := entflowfixture.New(client)
	eng := entflow.NewEngine()

	f := newSelfWasSnapshotFlow()
	require.NoError(t, eng.Register(f, store))

	owner, err := client.Order.Create().SetStatus(order.StatusPaid).Save(ctx)
	require.NoError(t, err)

	run, err := entflow.Start(ctx, eng, f, &selfLoaderInput{OrderID: owner.ID})
	require.NoError(t, err)

	w := newSelfLoaderWorker(t, eng)

	claimed, err := w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed, "first claim (mutate, takes the entry snapshot) should succeed")

	afterFirst, err := client.Order.Get(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, order.StatusShipped, afterFirst.Status, "mutate must have changed the live status away from the entry snapshot")

	claimed, err = w.ClaimOnce(ctx)
	require.NoError(t, err)
	require.True(t, claimed, "second claim (gated) should succeed")

	finalOrder, err := client.Order.Get(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, "gated-ran", finalOrder.PaymentIntentID,
		"the gated step must run: SelfWas(\"paid\") evaluates against the entry snapshot, not the now-\"shipped\" live status")

	finalRun, err := client.CancelOrderFlowRun.Get(ctx, run.ID.(int))
	require.NoError(t, err)
	require.Equal(t, "paid", finalRun.SelfWas, "the entry snapshot must be the value persisted on the run row's self_was column")
}
