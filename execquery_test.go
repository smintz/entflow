package entflow_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow/internal/testdata/ent/order"
	"github.com/smintz/entflow/internal/testdata/entclient"
)

// TestExecQuerySharesTheEntTransaction proves D-30 is mechanically reachable:
// a raw sql/execquery QueryContext SELECT issued on an open *ent.Tx sees a
// row created through that same transaction's generated builder before it is
// committed, while the identical SELECT issued through the top-level
// *ent.Client (outside the transaction) never observes it. This is the one
// mechanism the rest of Phase 2 is impossible without — the claim query and
// the step mutation must share a single transaction.
func TestExecQuerySharesTheEntTransaction(t *testing.T) {
	ctx := context.Background()
	client := entclient.New(t)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)

	_, err = tx.Order.Create().
		SetStatus(order.StatusDraft).
		Save(ctx)
	require.NoError(t, err)

	// The uncommitted row is visible on the same transaction's raw SQL path.
	txRows, err := tx.QueryContext(ctx, "SELECT COUNT(*) FROM orders")
	require.NoError(t, err)
	txCount, err := readCount(txRows)
	require.NoError(t, err)
	require.Equal(t, 1, txCount)

	// A read through the top-level client — a different connection, since
	// the fixture's SQLite DSN uses shared-cache mode, which enforces
	// table-level locking across connections — cannot observe the "orders"
	// table at all while tx's write lock is held: it blocks on SQLite's
	// unlock-notify mechanism rather than racily returning a stale count.
	// That block is itself proof the uncommitted row is not visible outside
	// the transaction: there is no window in which the outside connection
	// could read anything, committed or not, until the transaction
	// resolves.
	type outsideResult struct {
		count int
		err   error
	}
	outsideDone := make(chan outsideResult, 1)
	go func() {
		rows, err := client.QueryContext(context.Background(), "SELECT COUNT(*) FROM orders")
		if err != nil {
			outsideDone <- outsideResult{err: err}
			return
		}
		count, err := readCount(rows)
		outsideDone <- outsideResult{count: count, err: err}
	}()

	select {
	case <-outsideDone:
		t.Fatal("outside QueryContext returned before the transaction resolved; expected it to block on SQLite's shared-cache lock, proving isolation from the uncommitted row")
	case <-time.After(200 * time.Millisecond):
		// Still blocked, as expected: the outside connection cannot see
		// into the open transaction.
	}

	require.NoError(t, tx.Rollback())

	select {
	case res := <-outsideDone:
		require.NoError(t, res.err)
		require.Equal(t, 0, res.count)
	case <-time.After(5 * time.Second):
		t.Fatal("outside QueryContext did not unblock after the transaction resolved")
	}

	// A fresh read confirms both sides now agree: the rolled-back row never
	// committed anywhere.
	afterRows, err := client.QueryContext(ctx, "SELECT COUNT(*) FROM orders")
	require.NoError(t, err)
	afterCount, err := readCount(afterRows)
	require.NoError(t, err)
	require.Equal(t, 0, afterCount)
}

// readCount consumes a single-row, single-column COUNT(*) result set and
// closes it. It intentionally takes no *testing.T so it is safe to call from
// a goroutine other than the test's own — testify's FailNow-based assertions
// must never be invoked off the test goroutine.
func readCount(rows *sql.Rows) (int, error) {
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, sql.ErrNoRows
	}
	var count int
	if err := rows.Scan(&count); err != nil {
		return 0, err
	}
	return count, rows.Err()
}
