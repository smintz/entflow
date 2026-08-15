package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/smintz/entflow"
)

// errNilEngine is returned by New when eng is nil.
var errNilEngine = errors.New("entflow/worker: eng must not be nil")

// ErrSQLiteConcurrency is returned by New when Options.Dialect names SQLite
// and Options.Concurrency is greater than 1 — a refusal, never a silent
// clamp to one (D-36). SQLite has no row-level locking; a second concurrent
// claim goroutine would not get SKIP LOCKED's mutual exclusion from
// SQLiteStrategy, which relies entirely on the whole-database write
// serialization a single claiming goroutine gets for free.
var ErrSQLiteConcurrency = errors.New("entflow/worker: sqlite supports at most one claim goroutine")

// Worker claims and advances durable runs registered on an *entflow.Engine.
// Both DUR-09 topologies — in-process alongside an API server, or a
// dedicated worker binary — are the same object: go w.Run(ctx) is the whole
// difference.
//
// The worker is a pool, not a singleton: the unit of ownership is the run
// claim, not the worker. A run belongs to whichever transaction currently
// holds its row lock, for the duration of exactly one step, and to nothing
// else in between — no code path may key behavior on worker identity. See
// "The worker is a pool, not a singleton" in docs/dialects.md.
type Worker struct {
	eng      *entflow.Engine
	opts     Options
	strategy ClaimStrategy
}

// New constructs a Worker, validating eagerly: an unrecognized dialect, or
// a SQLite dialect with Concurrency above one, is refused here rather than
// lazily at the first claim (D-37). Neither refusal makes any database
// round trip — both are pure checks against opts.
func New(eng *entflow.Engine, opts Options) (*Worker, error) {
	if eng == nil {
		return nil, errNilEngine
	}
	opts = opts.withDefaults()

	strategy := opts.ClaimStrategy
	if strategy == nil {
		s, err := StrategyForDialect(opts.Dialect)
		if err != nil {
			return nil, err
		}
		strategy = s
	}

	if isSQLiteDialect(opts.Dialect) && opts.Concurrency > 1 {
		return nil, fmt.Errorf("entflow/worker: dialect %q: concurrency %d: %w (see docs/dialects.md)", opts.Dialect, opts.Concurrency, ErrSQLiteConcurrency)
	}

	return &Worker{eng: eng, opts: opts, strategy: strategy}, nil
}

// isSQLiteDialect reports whether dialect names the SQLite entry in the
// dialect matrix (D-35), under either of its accepted spellings.
func isSQLiteDialect(dialect string) bool {
	switch strings.ToLower(dialect) {
	case "sqlite", "sqlite3":
		return true
	default:
		return false
	}
}

// ClaimOnce attempts exactly one claim-execute-advance cycle (D-31: one run
// per claim transaction, never a batch), trying each registered flow in
// turn and returning as soon as one succeeds. claimed is false with a nil
// error when nothing was claimable across every registered flow.
func (w *Worker) ClaimOnce(ctx context.Context) (claimed bool, err error) {
	for _, f := range w.eng.Flows() {
		name := f.Name()
		store, ok := w.eng.StoreFor(name)
		if !ok {
			continue
		}
		runner, ok := w.eng.RunnerFor(name)
		if !ok {
			continue
		}
		claimedThis, claimErr := claimOnce(ctx, name, store, runner, w.strategy, w.opts)
		if claimErr != nil {
			return false, claimErr
		}
		if claimedThis {
			return true, nil
		}
	}
	return false, nil
}

// Shutdown is a placeholder in this plan's Task 1 (the poll loop). Real
// drain semantics (D-49: stop claiming, let in-flight step transactions
// commit, then exit) land in Task 2. It returns nil immediately.
func (w *Worker) Shutdown(ctx context.Context) error {
	return nil
}
