package worker_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	// Registers the "sqlite" database/sql driver name for this test binary.
	// worker_test's other tests reach SQLite through entclient.New
	// (durablerun_test.go, package entflow_test) or Postgres through
	// pgtest.Start — neither is imported by every file in this package, so
	// this suite blank-imports modernc.org/sqlite directly rather than
	// depending on another test file happening to have imported it first.
	_ "modernc.org/sqlite"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/testdata/ent"
	"github.com/smintz/entflow/internal/testdata/ent/cancelorderflowrun"
	"github.com/smintz/entflow/internal/testdata/pgtest"
	"github.com/smintz/entflow/worker"
)

// conformanceCase names one dialect row of the D-38 conformance matrix:
// either a working (newClient, strategy) pair the suite actually exercises,
// or a skip reason for a dialect the matrix documents as compatible but
// uncertified (MySQL, D-35).
type conformanceCase struct {
	// dialect names the subtest — one per row of docs/dialects.md's matrix.
	dialect string
	// mechanism is the mutual-exclusion mechanism this dialect's claim
	// strategy relies on, logged via t.Log so a reader of CI output can tell
	// a Postgres pass from a SQLite pass without reading the source.
	mechanism string
	// newClient constructs a fresh, migrated *ent.Client for this dialect.
	// Nil for a skipped row.
	newClient func(t *testing.T) *ent.Client
	// strategy returns the ClaimStrategy this dialect's runs are claimed
	// with. Nil for a skipped row.
	strategy func() worker.ClaimStrategy
	// skip, when non-empty, makes the subtest call t.Skip(skip) instead of
	// running the two properties below — MySQL's row (D-35: compatible,
	// uncertified; no MySQL container or driver runs in this phase).
	skip string
}

// conformanceCases is the D-38 matrix, one row per dialect docs/dialects.md
// ratifies. A MySQL row that is explicitly present and explicitly skipped,
// naming why, is the honest representation of "compatible, uncertified" —
// an absent row would let a reader assume coverage that does not exist.
func conformanceCases() []conformanceCase {
	return []conformanceCase{
		{
			dialect:   "postgres",
			mechanism: "row-level FOR UPDATE SKIP LOCKED (D-36) — a second concurrent claimer skips the already-locked row rather than blocking on it",
			newClient: pgtest.Start,
			strategy:  worker.SkipLockedStrategy,
		},
		{
			dialect: "sqlite",
			mechanism: "whole-database write-lock serialization under a BEGIN IMMEDIATE transaction — the DSN's _txlock=immediate parameter (applies to every transaction on the handle, read-only ones included, per worker.SQLiteStrategy's own doc comment) makes even SQLiteStrategy's bare SELECT participate in that lock; _busy_timeout makes a transaction that loses the race for it block and retry instead of failing immediately with SQLITE_BUSY",
			newClient: newSQLiteConformanceClient,
			strategy:  worker.SQLiteStrategy,
		},
		{
			dialect: "mysql",
			skip:    "MySQL shares the Postgres FOR UPDATE SKIP LOCKED statement (D-36) and worker.StrategyForDialect(\"mysql\") returns a working strategy, but no MySQL container or driver runs in this phase — this row is compatible and explicitly uncertified by the release gate, not silently absent (D-35, docs/dialects.md)",
		},
	}
}

// newSQLiteConformanceClient opens a fresh, file-backed SQLite ent client
// (in t.TempDir(), never :memory:) whose DSN carries both _txlock=immediate
// and _busy_timeout, deliberately independent of
// internal/testdata/entclient.New's own DSN (which sets only
// _txlock=immediate): _txlock=immediate is what makes SQLiteStrategy's bare
// SELECT participate in SQLite's whole-database write-lock serialization at
// all; _busy_timeout is what the no-double-claim property below additionally
// needs — it launches K claim transactions concurrently and expects every
// one to either claim a distinct run or observe none, never to error, which
// requires a transaction that loses the race for the write lock to BLOCK and
// retry rather than fail immediately with SQLite's zero-timeout default.
//
// A real file, not entclient.New's "mode=memory&cache=shared" DSN, is
// deliberate: shared-cache in-memory mode enforces its OWN table-level
// locking distinct from ordinary file locking, which surfaces as
// SQLITE_LOCKED ("database table is locked") rather than SQLITE_BUSY on
// lock contention between connections — and SQLite's busy-handler retry
// (_busy_timeout) does not reliably cover that shared-cache-specific error
// path. A file-backed database uses ordinary whole-file locking, where
// _busy_timeout's blocking-retry behavior is well-defined.
func newSQLiteConformanceClient(t *testing.T) *ent.Client {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "entflow_conformance.db")
	dsn := "file:" + dbPath + "?_fk=1&_txlock=immediate&_busy_timeout=5000"

	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(drv))
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.Schema.Create(context.Background()))
	return client
}

// TestClaimStrategyConformance is the D-38 claim-strategy conformance suite:
// one table-driven suite proving, against every dialect in the matrix, that
// no two concurrent claimers ever take the same row and that a rolled-back
// claim leaves its row claimable — so "the strategy is correct" is a claim
// the code makes about itself, not a sentence in docs/dialects.md.
func TestClaimStrategyConformance(t *testing.T) {
	for _, tc := range conformanceCases() {
		t.Run(tc.dialect, func(t *testing.T) {
			if tc.skip != "" {
				t.Skip(tc.skip)
			}
			if tc.dialect == "postgres" {
				pgtest.SkipUnlessDockerAvailable(t)
			}
			t.Logf("dialect=%s mutual-exclusion-mechanism=%s", tc.dialect, tc.mechanism)

			t.Run("no double claim", func(t *testing.T) {
				testNoDoubleClaim(t, tc)
			})
			t.Run("rolled back claim stays claimable", func(t *testing.T) {
				testRollbackLeavesClaimable(t, tc)
			})
		})
	}
}

// conformanceRun creates one CancelOrderFlowRun row (and its owning Order)
// directly through the generated client, in state, for either dialect —
// dialect-agnostic because both arms of the matrix run against the same
// generated internal/testdata/ent package, only the driver underneath
// differs.
func conformanceRun(t *testing.T, ctx context.Context, client *ent.Client, state cancelorderflowrun.State) *ent.CancelOrderFlowRun {
	t.Helper()
	owner, err := client.Order.Create().Save(ctx)
	require.NoError(t, err)
	run, err := client.CancelOrderFlowRun.Create().
		SetState(state).
		SetInput([]byte(`{}`)).
		SetOwnerID(owner.ID).
		Save(ctx)
	require.NoError(t, err)
	return run
}

// testNoDoubleClaim proves property one: with K claimable runs and K
// concurrent claim transactions started together, the multiset of claimed
// IDs contains no duplicate, and every claimer either gets a distinct run or
// observes none.
func testNoDoubleClaim(t *testing.T, tc conformanceCase) {
	ctx := context.Background()
	client := tc.newClient(t)

	const k = 5
	runIDs := make(map[int64]bool, k)
	for i := 0; i < k; i++ {
		run := conformanceRun(t, ctx, client, cancelorderflowrun.StatePending)
		runIDs[int64(run.ID)] = true
	}

	states := []string{string(cancelorderflowrun.StatePending), string(cancelorderflowrun.StateRunning)}

	type outcome struct {
		id  int64
		ok  bool
		err error
	}
	outcomes := make([]outcome, k)

	// A single gate released all at once — deliberately NOT "wait until
	// every goroutine has already opened its transaction": SQLite's
	// immediate-mode BEGIN itself blocks until the write lock is free, so a
	// barrier requiring every goroutine to have completed BeginTx before
	// any of them may proceed would deadlock on SQLite (only one goroutine
	// can ever hold that lock at a time). Racing from before BeginTx lets
	// each dialect's own mechanism — SKIP LOCKED's row lock on Postgres,
	// BEGIN IMMEDIATE's whole-database write lock on SQLite — do the
	// actual serialization.
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	wg.Add(k)

	for i := 0; i < k; i++ {
		go func(i int) {
			defer wg.Done()
			start.Wait()

			tx, err := client.Tx(ctx)
			if err != nil {
				outcomes[i] = outcome{err: err}
				return
			}
			q, ok := any(tx).(entflow.RawQuerier)
			if !ok {
				outcomes[i] = outcome{err: fmt.Errorf("tx has type %T, does not satisfy entflow.RawQuerier", tx)}
				_ = tx.Rollback()
				return
			}

			strategy := tc.strategy()
			id, claimed, claimErr := strategy.Claim(ctx, q, claimTable(), states)
			if claimErr != nil {
				_ = tx.Rollback()
				outcomes[i] = outcome{err: claimErr}
				return
			}
			if !claimed {
				outcomes[i] = outcome{err: tx.Rollback()}
				return
			}

			// Mark the claimed run terminal within the SAME transaction,
			// before commit: the raw Claim statement alone only guarantees
			// no two transactions hold the SAME row concurrently, never
			// that a row stays claimed forever after one claimer commits
			// without changing anything — that forward-progress guarantee
			// is Advance's job in production (worker/dbstep.go), out of
			// scope for this claim-statement-only suite. Without this, a
			// straggler transaction that opens after the winner has
			// already committed would legitimately observe the row as
			// still pending and reclaim it — which is not the double-claim
			// bug this property tests for (two transactions holding the
			// row AT THE SAME TIME), so the test supplies the minimal
			// equivalent of Advance itself.
			if _, err := tx.CancelOrderFlowRun.UpdateOneID(int(id)).SetState(cancelorderflowrun.StateDone).Save(ctx); err != nil {
				_ = tx.Rollback()
				outcomes[i] = outcome{err: err}
				return
			}
			outcomes[i] = outcome{id: id, ok: true, err: tx.Commit()}
		}(i)
	}

	start.Done()
	wg.Wait()

	seen := make(map[int64]bool, k)
	claimedCount := 0
	for i, o := range outcomes {
		require.NoError(t, o.err, "claimer %d", i)
		if !o.ok {
			continue
		}
		require.True(t, runIDs[o.id], "claimer %d claimed an ID (%d) outside the K runs this test created", i, o.id)
		require.False(t, seen[o.id], "run %d claimed more than once — double claim", o.id)
		seen[o.id] = true
		claimedCount++
	}
	require.Equal(t, k, claimedCount, "every one of the K claimable runs must be claimed exactly once across K concurrent claimers")
}

// testRollbackLeavesClaimable proves property two: claiming a run and then
// rolling back the transaction, instead of committing it, leaves the row
// exactly as claimable as it was — the next claim returns the SAME run.
func testRollbackLeavesClaimable(t *testing.T, tc conformanceCase) {
	ctx := context.Background()
	client := tc.newClient(t)

	run := conformanceRun(t, ctx, client, cancelorderflowrun.StatePending)
	states := []string{string(cancelorderflowrun.StatePending), string(cancelorderflowrun.StateRunning)}
	strategy := tc.strategy()

	tx1, err := client.Tx(ctx)
	require.NoError(t, err)
	q1, ok := any(tx1).(entflow.RawQuerier)
	require.True(t, ok)

	id1, ok, err := strategy.Claim(ctx, q1, claimTable(), states)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, run.ID, id1)
	require.NoError(t, tx1.Rollback())

	tx2, err := client.Tx(ctx)
	require.NoError(t, err)
	q2, ok := any(tx2).(entflow.RawQuerier)
	require.True(t, ok)
	defer tx2.Rollback()

	id2, ok, err := strategy.Claim(ctx, q2, claimTable(), states)
	require.NoError(t, err)
	require.True(t, ok, "a rolled-back claim must leave the row claimable")
	require.EqualValues(t, run.ID, id2, "the next claim must return the SAME run the rolled-back claim released")
}
