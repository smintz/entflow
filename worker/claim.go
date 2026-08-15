// Package worker executes durable runs: it claims a run row via a
// dialect-specific ClaimStrategy, executes exactly one DB step's closure
// against the same transaction, writes the step's effect and the run's
// progress pointer, and commits — all in one transaction (D-30).
package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/smintz/entflow"
)

// ErrUnsupportedDialect is returned by StrategyForDialect (and, at
// construction, by New) when a dialect string names no ratified entry in
// the dialect matrix (D-35). The error names the offending dialect and
// points at docs/dialects.md.
var ErrUnsupportedDialect = errors.New("entflow/worker: unsupported dialect")

// ClaimStrategy isolates the one query that cannot be written portably
// across dialects (D-36): the claim query itself. Every value in the
// statement a ClaimStrategy builds goes through placeholder binding — the
// predicate is a fixed literal derived from t's identifiers, with no
// caller-supplied SQL fragment anywhere (T-02-02's mitigation).
type ClaimStrategy interface {
	// Claim attempts to claim exactly one row from t whose state is one of
	// states and whose retry-after column is null or already due, using
	// the database's own clock for that comparison — never the worker
	// process clock, so worker/database clock skew cannot make a run
	// permanently unclaimable or prematurely claimable. ok is false, err is
	// nil when nothing is claimable.
	Claim(ctx context.Context, q entflow.RawQuerier, t entflow.RunTable, states []string) (id int64, ok bool, err error)
}

// SQLiteStrategy returns the ClaimStrategy correct for SQLite: a plain
// SELECT of the ID column, filtered to the claimable states and to rows
// whose retry_after is null or already due, ordered by ID, limited to one
// row. This is correct on SQLite ONLY because the surrounding transaction
// is opened in immediate mode ("_txlock=immediate" on the DSN, or an
// equivalent BEGIN IMMEDIATE) — SQLite's whole-database write serialization
// under an immediate transaction supplies exactly the mutual exclusion
// SKIP LOCKED supplies elsewhere. A SQLite connection opened without
// immediate-mode transactions does NOT get this guarantee from this
// strategy alone.
func SQLiteStrategy() ClaimStrategy {
	return sqliteStrategy{}
}

type sqliteStrategy struct{}

func (sqliteStrategy) Claim(ctx context.Context, q entflow.RawQuerier, t entflow.RunTable, states []string) (int64, bool, error) {
	return claimWithPlaceholders(ctx, q, t, states, "?", "", "CURRENT_TIMESTAMP")
}

// SkipLockedStrategy returns the ClaimStrategy for dialects that support
// SELECT ... FOR UPDATE SKIP LOCKED (Postgres and MySQL, D-35/D-36) using
// Postgres's own placeholder and clock-function syntax. Certified against a
// real Postgres by worker/claim_test.go (plan 02-03, via
// internal/testdata/pgtest) and by worker/conformance_test.go's D-38 suite;
// StrategyForDialect is the entry point that also selects MySQL's
// placeholder syntax. MySQL shares this exact statement (D-36) but is
// compatible/uncertified, not certified by the release gate (D-35) — no
// MySQL container or driver runs against it in this phase.
func SkipLockedStrategy() ClaimStrategy {
	return skipLockedStrategy{dialect: "postgres"}
}

type skipLockedStrategy struct {
	dialect string // "postgres" or "mysql"
}

func (s skipLockedStrategy) Claim(ctx context.Context, q entflow.RawQuerier, t entflow.RunTable, states []string) (int64, bool, error) {
	placeholder := "?"
	if s.dialect == "postgres" {
		placeholder = "$"
	}
	return claimWithPlaceholders(ctx, q, t, states, placeholder, "FOR UPDATE SKIP LOCKED", "now()")
}

// claimWithPlaceholders builds and runs the shared claim query shape for
// every strategy: only the placeholder style, the trailing locking clause,
// and the database-clock function name vary by dialect. placeholderStyle is
// either the literal "?" (SQLite/MySQL) or "$" (Postgres, numbered
// $1..$N). now is the database-side clock expression the retry_after
// comparison uses — never the worker process clock.
func claimWithPlaceholders(ctx context.Context, q entflow.RawQuerier, t entflow.RunTable, states []string, placeholderStyle, lockingClause, now string) (int64, bool, error) {
	if len(states) == 0 {
		return 0, false, fmt.Errorf("entflow/worker: claim: no claimable states supplied")
	}

	placeholders := make([]string, len(states))
	args := make([]any, len(states))
	for i, s := range states {
		placeholders[i] = placeholderFor(placeholderStyle, i)
		args[i] = s
	}

	stmt := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s IN (%s) AND (%s IS NULL OR %s <= %s) ORDER BY %s LIMIT 1",
		t.IDColumn, t.Name, t.StateColumn, strings.Join(placeholders, ", "),
		t.RetryAfterColumn, t.RetryAfterColumn, now, t.IDColumn,
	)
	if lockingClause != "" {
		stmt += " " + lockingClause
	}

	rows, err := q.QueryContext(ctx, stmt, args...)
	if err != nil {
		return 0, false, fmt.Errorf("entflow/worker: claim query: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, false, rows.Err()
	}
	var id int64
	if err := rows.Scan(&id); err != nil {
		return 0, false, fmt.Errorf("entflow/worker: claim scan: %w", err)
	}
	return id, true, rows.Err()
}

// placeholderFor renders the i'th (zero-based) bound-parameter placeholder
// for style, which is either the literal "?" or the Postgres numbered-dollar
// prefix "$".
func placeholderFor(style string, i int) string {
	if style == "$" {
		return fmt.Sprintf("$%d", i+1)
	}
	return style
}

// StrategyForDialect selects the ClaimStrategy for dialect (D-35's ratified
// matrix): "postgres"/"postgresql" and "mysql" get the SKIP LOCKED
// strategy with their own placeholder syntax; "sqlite"/"sqlite3" get
// SQLiteStrategy. Every other dialect is refused with an error wrapping
// ErrUnsupportedDialect, naming dialect and pointing at docs/dialects.md.
func StrategyForDialect(dialect string) (ClaimStrategy, error) {
	switch strings.ToLower(dialect) {
	case "postgres", "postgresql":
		return skipLockedStrategy{dialect: "postgres"}, nil
	case "mysql":
		return skipLockedStrategy{dialect: "mysql"}, nil
	case "sqlite", "sqlite3":
		return SQLiteStrategy(), nil
	default:
		return nil, fmt.Errorf("entflow/worker: dialect %q: %w (see docs/dialects.md)", dialect, ErrUnsupportedDialect)
	}
}
