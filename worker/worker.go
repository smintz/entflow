package worker

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

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
		claimedThis, claimErr := claimOnce(ctx, store, runner, w.strategy)
		if claimErr != nil {
			return false, claimErr
		}
		if claimedThis {
			return true, nil
		}
	}
	return false, nil
}

// Run starts Options.Concurrency independent claim goroutines (D-31: N
// independent claim transactions, never one claim transaction fanning work
// out to N goroutines — every goroutine calls ClaimOnce, which itself opens
// and commits exactly one transaction claiming at most one run per
// iteration). Nothing is shared between the goroutines but w itself — the
// engine, the registered stores, and the underlying database connection
// pool. No goroutine has an identity: no run is associated with a goroutine
// or a process beyond the lifetime of one claim transaction, and no
// in-memory structure here is keyed by run ID (see "The worker is a pool,
// not a singleton" in docs/dialects.md).
//
// Run returns cleanly when ctx is cancelled: every goroutine stops starting
// new claim iterations once it observes cancellation, and Run waits for
// every in-flight iteration to finish before returning. Full drain semantics
// (D-49: bounding that wait, then cancelling in-flight step contexts) and
// poll jitter tuning are plan 02-07's expansion — this keeps plan 02-02's
// ticker/jitter behavior unchanged so the two plans never race on the same
// file.
func (w *Worker) Run(ctx context.Context) error {
	n := w.opts.Concurrency
	if n <= 0 {
		n = 1
	}

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			w.pollLoop(ctx)
		}()
	}
	wg.Wait()
	return nil
}

// pollLoop is the body one claim goroutine runs: on every tick (and on
// every Engine.Notify wakeup) it drains claimOnceRecovered until nothing
// more is claimable, then waits out a jittered PollInterval. Returns when
// ctx is cancelled. Every goroutine started by Run runs its own, entirely
// independent copy of this loop against its own transactions — there is no
// shared mutable state between them beyond w's own read-only fields and the
// database itself.
func (w *Worker) pollLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		for {
			claimed, _ := w.claimOnceRecovered(ctx)
			if !claimed {
				break
			}
		}

		wait := jitteredInterval(w.opts.PollInterval, w.opts.Jitter)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-w.eng.Nudges():
		}
	}
}

// claimOnceRecovered wraps ClaimOnce with its own recover boundary, mirroring
// RunInTx's transaction-level recover (exec.go) but scoped to one claim
// goroutine's iteration rather than one Exec call. This is a SEPARATE site
// from runStep's per-closure recover (exec.go): a step closure's own panic
// is already converted into a returned *entflow.StepError well before it
// would ever reach here, so this recover only ever fires for a bug in the
// claim-execute-advance bookkeeping itself (worker/dbstep.go), never for a
// step author's panic. claimOnce's own deferred Rollback has already run by
// the time this recover executes — defers run during panic unwinding
// regardless of whether anything above them recovers — so this exists
// solely to stop that panic from crashing the whole worker process (a
// long-running worker that dies on one bad step is not durable), not to
// redo the rollback.
func (w *Worker) claimOnceRecovered(ctx context.Context) (claimed bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("entflow/worker: recovered panic in claim-execute-advance: %v", r)
		}
	}()
	return w.ClaimOnce(ctx)
}

// Shutdown is a placeholder in this tracer plan — real drain semantics
// (D-49: stop claiming, let in-flight step transactions commit, then exit)
// land in a later plan. It returns nil immediately.
func (w *Worker) Shutdown(ctx context.Context) error {
	return nil
}

// jitteredInterval returns base scaled by a random factor in
// [1-jitter, 1+jitter]. jitter <= 0 returns base unchanged.
func jitteredInterval(base time.Duration, jitter float64) time.Duration {
	if jitter <= 0 || base <= 0 {
		return base
	}
	factor := 1 - jitter + rand.Float64()*2*jitter
	return time.Duration(float64(base) * factor)
}
