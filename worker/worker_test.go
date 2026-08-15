package worker_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/worker"
)

// TestNewRefusesUnsupportedDialect proves DUR-08/D-37: an unrecognized
// dialect string is refused at construction, naming both the offending
// dialect and the documentation path — never lazily deferred to the first
// claim.
func TestNewRefusesUnsupportedDialect(t *testing.T) {
	eng := entflow.NewEngine()
	w, err := worker.New(eng, worker.Options{Dialect: "cockroachdb"})
	require.Nil(t, w)
	require.ErrorIs(t, err, worker.ErrUnsupportedDialect)
	require.Contains(t, err.Error(), "cockroachdb")
	require.Contains(t, err.Error(), "docs/dialects.md")
}

// TestNewRefusesSQLiteConcurrency proves D-36: SQLite with Concurrency
// above one is refused at construction rather than silently serialized.
func TestNewRefusesSQLiteConcurrency(t *testing.T) {
	eng := entflow.NewEngine()
	w, err := worker.New(eng, worker.Options{Dialect: "sqlite", Concurrency: 2})
	require.Nil(t, w)
	require.ErrorIs(t, err, worker.ErrSQLiteConcurrency)
}

// TestNewAcceptsSQLiteSingleWorker and TestNewAcceptsPostgresConcurrency
// prove the boundary: SQLite at concurrency 1, and any other ratified
// dialect at higher concurrency, both construct successfully.
func TestNewAcceptsSQLiteSingleWorker(t *testing.T) {
	eng := entflow.NewEngine()
	w, err := worker.New(eng, worker.Options{Dialect: "sqlite", Concurrency: 1})
	require.NoError(t, err)
	require.NotNil(t, w)
}

func TestNewAcceptsPostgresConcurrency(t *testing.T) {
	eng := entflow.NewEngine()
	w, err := worker.New(eng, worker.Options{Dialect: "postgres", Concurrency: 4})
	require.NoError(t, err)
	require.NotNil(t, w)
}

// TestNewValidatesBeforeAnyDatabaseRoundTrip proves New's validation is
// synchronous and side-effect-free: constructing against a bogus dialect (or
// an over-concurrent SQLite configuration) never touches a database — there
// is no database configured at all in this test, so a database round trip
// would surface as a nil-pointer panic or a network-dial error, neither of
// which occurs.
func TestNewValidatesBeforeAnyDatabaseRoundTrip(t *testing.T) {
	eng := entflow.NewEngine() // no flows, no stores, no client anywhere

	_, err := worker.New(eng, worker.Options{Dialect: "not-a-real-dialect"})
	require.Error(t, err)

	_, err = worker.New(eng, worker.Options{Dialect: "sqlite", Concurrency: 8})
	require.Error(t, err)
}

// TestStrategyForDialectMatrix exercises the full D-35 matrix end to end
// through the public entry point.
func TestStrategyForDialectMatrix(t *testing.T) {
	for _, dialect := range []string{"postgres", "postgresql", "mysql", "sqlite", "sqlite3"} {
		t.Run(dialect, func(t *testing.T) {
			strategy, err := worker.StrategyForDialect(dialect)
			require.NoError(t, err)
			require.NotNil(t, strategy)
		})
	}

	_, err := worker.StrategyForDialect("gremlin")
	require.Error(t, err)
	var wrapped error = err
	require.True(t, errors.Is(wrapped, worker.ErrUnsupportedDialect))
	require.True(t, strings.Contains(err.Error(), "gremlin"))
}
