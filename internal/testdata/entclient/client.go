// Package entclient provides an in-memory SQLite ent client helper for
// Phase 1's tests. It is a test-only dependency surface: modernc.org/sqlite
// is imported only from this package (never from ordinary, non-test source),
// which is what keeps it out of `go list -deps` for the module's non-test
// build graph (D-04/D-21).
//
// Per D-22, using SQLite here says nothing about entflow's production
// dialect support — Postgres and testcontainers arrive in Phase 2 with the
// worker.
package entclient

import (
	"context"
	"database/sql"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"github.com/smintz/entflow/internal/testdata/ent"
	// Blank-imported so its init() wires the schema-stitching (here, the
	// no-op passthrough Hook that exists to force this cyclic-import
	// avoidance split — see schema/order.go's Hooks() doc comment) into the
	// generated package's exported Hooks array. Without this import, the
	// hook array is never populated and order_update.go's withHooks call
	// invokes a nil function.
	_ "github.com/smintz/entflow/internal/testdata/ent/runtime"
)

// New returns a fresh in-memory SQLite ent client with the schema
// auto-migrated. The driver name registered by modernc.org/sqlite is
// "sqlite" — NOT "sqlite3", which is the CGo mattn/go-sqlite3 driver's name
// and the single most likely copy-paste mistake here.
//
// The DSN's _txlock=immediate is Phase 2's requirement, not Phase 1's: it
// makes every transaction opened against this client BEGIN IMMEDIATE,
// reserving SQLite's write lock at BEGIN time rather than at the first
// write. worker.SQLiteStrategy's claim query is a bare SELECT — without
// immediate mode, two concurrent claim transactions could both observe the
// same row as claimable before either issues a write, since a plain read
// takes no lock under SQLite's default deferred mode. Harmless for Phase
// 1's single-goroutine tests.
func New(t *testing.T) *ent.Client {
	t.Helper()

	db, err := sql.Open("sqlite", "file:entflow?mode=memory&cache=shared&_fk=1&_txlock=immediate")
	if err != nil {
		t.Fatalf("entclient: opening sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(drv))
	t.Cleanup(func() {
		_ = client.Close()
	})

	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("entclient: running schema migration: %v", err)
	}

	return client
}
