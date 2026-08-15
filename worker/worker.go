package worker

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/smintz/entflow"
)

// errNilEngine is returned by New when eng is nil.
var errNilEngine = errors.New("entflow/worker: eng must not be nil")

// Options configures a Worker at construction time. See worker.New.
//
// The worker is a pool, not a singleton: the unit of ownership is the run
// claim, not the worker. A run belongs to whichever transaction currently
// holds its row lock, for the duration of exactly one step, and to nothing
// else in between — no code path may key behavior on worker identity. See
// docs/dialects.md.
type Options struct {
	// Dialect names the database dialect the worker's run stores are
	// backed by — "postgres", "mysql", or "sqlite". Resolved into a
	// ClaimStrategy at construction (D-37), never lazily at first claim.
	Dialect string
	// Concurrency is the number of interchangeable claim goroutines this
	// worker pool runs. Forced to at most 1 on SQLite (D-36) — New refuses
	// a higher value rather than silently clamping it.
	Concurrency int
	// PollInterval is the base interval between poll ticks (D-33).
	PollInterval time.Duration
	// Jitter is the fractional jitter applied to PollInterval, in [0,1).
	Jitter float64
	// ClaimStrategy overrides the strategy StrategyForDialect(Dialect)
	// would otherwise select. Nil selects the dialect's ratified default.
	ClaimStrategy ClaimStrategy
}

// withDefaults fills unset fields with this tracer's minimal defaults.
// Task 2 (worker/options.go) replaces this with the full Default* constant
// set (PollInterval, Jitter, Concurrency, DrainTimeout, MaxAttempts) once
// Options gains its remaining fields.
func (o Options) withDefaults() Options {
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	if o.Jitter <= 0 {
		o.Jitter = 0.25
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 1
	}
	return o
}

// Worker claims and advances durable runs registered on an *entflow.Engine.
// Both DUR-09 topologies — in-process alongside an API server, or a
// dedicated worker binary — are the same object: go w.Run(ctx) is the whole
// difference.
type Worker struct {
	eng      *entflow.Engine
	opts     Options
	strategy ClaimStrategy
}

// New constructs a Worker, validating eagerly: an unrecognized dialect, or
// a SQLite dialect with Concurrency above one, is refused here rather than
// lazily at the first claim (D-37).
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

	return &Worker{eng: eng, opts: opts, strategy: strategy}, nil
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

// Run drives a poll loop: on every tick (and on every Engine.Notify wakeup)
// it drains ClaimOnce until nothing more is claimable, then waits out a
// jittered PollInterval. Returns nil cleanly when ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		for {
			claimed, err := w.ClaimOnce(ctx)
			if err != nil || !claimed {
				break
			}
		}

		wait := jitteredInterval(w.opts.PollInterval, w.opts.Jitter)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		case <-w.eng.Nudges():
		}
	}
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
