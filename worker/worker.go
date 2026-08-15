package worker

import (
	"context"
	"errors"
	"fmt"
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

	// stopCh is closed exactly once, by Shutdown, as the "stop starting new
	// claims" signal every pollLoop goroutine selects on (loop.go) —
	// deliberately separate from the ctx a claim's own transaction runs
	// under, so an in-flight claim keeps running (and can still commit)
	// while no new one begins. Created once in New so it is never nil,
	// including on a Worker whose Run is never called (Shutdown on an
	// unstarted worker must not panic closing a nil channel). stopCh exists
	// purely to wake up a pollLoop goroutine's select PROMPTLY, even mid-wait
	// — the actual "may a new claim begin" decision is w.stopping below,
	// checked atomically alongside w.inFlight under mu.
	stopCh chan struct{}

	// mu guards started, cancelWork, stopping, and inFlight — one lock for
	// every piece of Shutdown/claim-lifecycle bookkeeping, deliberately NOT
	// a sync.WaitGroup: a WaitGroup's contract forbids a positive-delta Add
	// racing a Wait once the counter has touched zero (a real, intermittent
	// race this file hit during development — see zeroCond below), which is
	// exactly the pattern "N independent claim goroutines starting and
	// finishing claims while Shutdown waits for zero" produces. A plain
	// mutex-guarded counter plus a condition variable has no such
	// restriction: beginClaim/endClaim/Shutdown's drain wait are all fully
	// serialized through mu.
	mu         sync.Mutex
	started    bool
	cancelWork context.CancelFunc
	stopping   bool
	inFlight   int
	zeroCond   *sync.Cond

	// pollWG tracks the pollLoop goroutines Run starts; Shutdown waits on it
	// so it never returns while a goroutine is still unwinding. A
	// sync.WaitGroup is safe here specifically because Run — the only
	// caller that Adds to it — Adds all N deltas up front, once, before any
	// goroutine can Done; nothing Adds to pollWG concurrently with a Wait,
	// which is the exact pattern the mu/zeroCond pair above exists to avoid
	// for claim counting.
	pollWG sync.WaitGroup

	// shutdownMu serializes Shutdown itself: a second, concurrent or
	// sequential call blocks until the first finishes, then observes
	// shutdownDone and returns nil immediately — D-49's "safe to call
	// twice" requirement, without redoing (or racing) the drain.
	shutdownMu   sync.Mutex
	shutdownDone bool
}

// beginClaim registers one in-flight claim attempt, under the same lock
// Shutdown uses to set stopping — so "is a new claim allowed to start" and
// "count it if so" are one atomic decision, with no window where Shutdown
// could set stopping between a caller's check and its increment. ok is
// false once Shutdown has been called: the caller (claimOnceRecovered,
// loop.go) must not proceed to claim, and returns claimed=false, nil — the
// same "nothing claimable" shape as an empty poll, which is what makes
// pollLoop's own inner claiming loop stop on its own with no separate
// stopCh check needed there.
func (w *Worker) beginClaim() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopping {
		return false
	}
	w.inFlight++
	return true
}

// endClaim un-registers one in-flight claim (always paired with a
// successful beginClaim, via defer in claimOnceRecovered) and, if that was
// the last one, wakes every goroutine blocked in Shutdown's zeroCond.Wait.
func (w *Worker) endClaim() {
	w.mu.Lock()
	w.inFlight--
	if w.inFlight == 0 {
		w.zeroCond.Broadcast()
	}
	w.mu.Unlock()
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

	w := &Worker{eng: eng, opts: opts, strategy: strategy, stopCh: make(chan struct{})}
	w.zeroCond = sync.NewCond(&w.mu)
	return w, nil
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

// Shutdown is graceful termination framed exactly as D-49 requires:
// shutdown is a crash we happened to be polite about. It stops claiming,
// waits for in-flight step transactions to commit — bounded by
// Options.DrainTimeout or ctx's own deadline, whichever expires first — and
// past that bound cancels the step contexts so their transactions roll
// back, which leaves each affected run exactly as claimable as a real crash
// would have (D-30). Nothing here needed a mechanism crash-resume did not
// already provide: no drain table, no in-memory claimed-run registry, no
// handoff protocol — only a signal to stop claiming and a deadline-bounded
// wait, because "rolled back" was already a safe, ordinary outcome before
// this method existed.
//
// Shutdown is idempotent: calling it a second time (concurrently or
// sequentially) blocks until any first call finishes, then returns nil
// without redoing the drain. Calling it on a Worker whose Run was never
// invoked returns nil immediately — there is nothing to stop and nothing to
// drain. Shutdown does not assume a single goroutine or a single worker
// process: it drains only THIS process's pool (Options.Concurrency claim
// goroutines) and makes no claim about, and has no visibility into, any
// other worker process claiming from the same table — consistent with "the
// worker is a pool, not a singleton" (docs/dialects.md).
//
// A non-nil return names how many claims were still in flight when the
// drain deadline (or ctx) expired — reporting that honestly, rather than a
// silent truncation, is the entire point of this method existing as
// something more than "cancel the context and hope."
func (w *Worker) Shutdown(ctx context.Context) error {
	w.shutdownMu.Lock()
	defer w.shutdownMu.Unlock()
	if w.shutdownDone {
		return nil
	}

	// Closing stopCh is safe even if Run was never called: every pollLoop
	// goroutine (none exist yet, in that case) selects on it, and New
	// guarantees stopCh is never nil.
	close(w.stopCh)

	w.mu.Lock()
	w.stopping = true // beginClaim (worker.go) checks this — no new claim starts from here on
	started := w.started
	cancelWork := w.cancelWork
	w.mu.Unlock()

	if !started {
		w.shutdownDone = true
		return nil
	}

	// drained closes once w.inFlight reaches zero. Checking the condition
	// under mu BEFORE ever calling zeroCond.Wait means there is no missed-
	// wakeup window: either endClaim's decrement-to-zero-and-Broadcast
	// already happened (in which case this goroutine sees inFlight==0
	// immediately and never waits), or it happens later while this
	// goroutine is inside Wait (which atomically releases mu while
	// blocked, so the Broadcast is not missed).
	drained := make(chan struct{})
	go func() {
		w.mu.Lock()
		for w.inFlight > 0 {
			w.zeroCond.Wait()
		}
		w.mu.Unlock()
		close(drained)
	}()

	var err error
	select {
	case <-drained:
		// Every in-flight claim committed (or rolled back on its own)
		// before the drain deadline — the ordinary, polite path.
	case <-time.After(w.opts.DrainTimeout):
		w.mu.Lock()
		n := w.inFlight
		w.mu.Unlock()
		if cancelWork != nil {
			cancelWork()
		}
		<-drained
		err = fmt.Errorf("entflow/worker: shutdown: drain timeout (%s) exceeded with %d claim(s) still in flight; their contexts were cancelled", w.opts.DrainTimeout, n)
	case <-ctx.Done():
		w.mu.Lock()
		n := w.inFlight
		w.mu.Unlock()
		if cancelWork != nil {
			cancelWork()
		}
		<-drained
		err = fmt.Errorf("entflow/worker: shutdown: caller context done (%w) with %d claim(s) still in flight; their contexts were cancelled", ctx.Err(), n)
	}

	// Every pollLoop goroutine observes stopCh (and, once cancelWork ran
	// above, its own cancelled ctx too) and returns promptly; wait for them
	// so Shutdown never returns while one is still unwinding.
	w.pollWG.Wait()

	w.shutdownDone = true
	return err
}
