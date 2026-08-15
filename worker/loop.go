package worker

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

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
// Run derives its own cancellable work context from ctx (workCtx below) —
// the context every claim transaction actually runs under — so a later
// Shutdown call can cancel in-flight claims independently of ctx itself
// (D-49's drain-then-cancel deadline). Cancelling ctx directly propagates
// into workCtx immediately, with no drain window at all: that is exactly
// what "Run returns cleanly when its own context is cancelled, taking the
// same path as Shutdown with a zero drain window" means literally, not just
// by convention — there is no separate code path for it.
//
// Run always returns nil: a run cancelled by ctx, or drained and cancelled
// by Shutdown, both leave every in-flight claim's transaction rolled back
// (D-30's ordinary crash-shaped outcome) rather than surfacing as a Run
// error. Shutdown, not Run's return value, is where a caller learns whether
// the drain deadline was exceeded.
func (w *Worker) Run(ctx context.Context) error {
	n := w.opts.Concurrency
	if n <= 0 {
		n = 1
	}

	workCtx, cancel := context.WithCancel(ctx)

	w.mu.Lock()
	w.started = true
	w.cancelWork = cancel
	w.mu.Unlock()

	w.pollWG.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer w.pollWG.Done()
			w.pollLoop(workCtx)
		}()
	}
	w.pollWG.Wait()
	cancel()
	return nil
}

// pollLoop is the body one claim goroutine runs: on every tick (and on
// every Engine.Notify wakeup) it drains claimOnceRecovered until nothing
// more is claimable, then waits out a jittered PollInterval. Returns when
// ctx is cancelled or when w.stopCh is closed (Shutdown's "stop claiming"
// signal — deliberately a SEPARATE channel from ctx, so Shutdown can stop
// new claims from starting while letting an in-flight claim's transaction,
// still running under ctx, finish committing during the drain window).
// stopCh is what makes this select wake up PROMPTLY on Shutdown even mid-wait;
// claimOnceRecovered's own beginClaim gate (worker.go) is what makes the
// inner claiming loop below stop starting new claims, so no separate stopCh
// check is needed there. Every goroutine started by Run runs its own,
// entirely independent copy of this loop against its own transactions —
// there is no shared mutable state between them beyond w's own read-only
// fields and the database itself.
//
// A goroutine that just claimed a run attempts its next claim immediately,
// without waiting out a poll interval first: work found means more work may
// be waiting, and a durable run needs one claim per step, so without this a
// multi-step run would cost one poll interval per step — the single most
// impactful latency property this loop has.
func (w *Worker) pollLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		default:
		}

		for {
			claimed, _ := w.claimOnceRecovered(ctx)
			if !claimed {
				break
			}
		}

		// Jitter is applied on EVERY wait, never once at construction time
		// (D-33): N workers started by the same deployment at the same
		// second converge on the same poll tick without it, and — absent
		// per-wait re-randomization — stay converged forever once they do,
		// turning every tick into a claim-query stampede (the documented
		// poll-storm performance trap, .planning/research/PITFALLS.md). A
		// nudge from Engine.Notify (below) short-circuits this wait
		// entirely; polling remains the correctness backstop regardless —
		// a nudge that is missed, dropped, or (Phase 2's deliberate scope
		// limit) cannot cross a process boundary at all costs latency and
		// nothing else, because the very next tick still fires.
		wait := jitteredInterval(w.opts.PollInterval, w.opts.Jitter)
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-time.After(wait):
		case <-w.eng.Nudges():
			// The nudge channel is buffered (size 1) and Engine.Notify's
			// send is non-blocking (engine.go): a nudge can never become a
			// backpressure path, and a slow or stuck worker can never stall
			// entflow.Start. Cross-process wakeup — a worker in a different
			// process learning about a run started elsewhere — is
			// deliberately out of scope until the transactional outbox
			// work; until then, every process's own poll ticker is what
			// eventually claims a run no co-located nudge reached.
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
//
// w.beginClaim (worker.go) gates entry: once Shutdown has set w.stopping,
// beginClaim returns false and this returns (false, nil) without ever
// calling ClaimOnce — which is also what makes pollLoop's own inner
// claiming loop above stop on its own, with no separate stopCh check
// needed there. A successful beginClaim is always paired with exactly one
// w.endClaim, via defer, so Shutdown's drain wait (a condition variable, not
// a sync.WaitGroup — see worker.go's doc comment on why) always eventually
// observes zero in-flight claims.
func (w *Worker) claimOnceRecovered(ctx context.Context) (claimed bool, err error) {
	if !w.beginClaim() {
		return false, nil
	}
	defer func() {
		w.endClaim()
		if r := recover(); r != nil {
			err = fmt.Errorf("entflow/worker: recovered panic in claim-execute-advance: %v", r)
		}
	}()
	return w.ClaimOnce(ctx)
}

// jitteredInterval returns base scaled by a random factor in
// [1-jitter, 1+jitter]. jitter <= 0 returns base unchanged. Called fresh on
// every wait (pollLoop above), never memoized — a per-goroutine, per-wait
// random factor is what actually prevents the poll-storm convergence D-33
// exists to avoid; a jitter computed once at construction and reused for
// every subsequent wait would let goroutines started together drift back
// into lockstep the moment their (identical) jittered intervals happened to
// realign.
func jitteredInterval(base time.Duration, jitter float64) time.Duration {
	if jitter <= 0 || base <= 0 {
		return base
	}
	factor := 1 - jitter + rand.Float64()*2*jitter
	return time.Duration(float64(base) * factor)
}
