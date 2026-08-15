package worker

import (
	"database/sql/driver"
	"errors"
	"math/rand"
	"time"
)

// sqlStater is the single-method interface a Postgres driver's error type
// exposes its SQLSTATE code through. entflow declares this locally, rather
// than importing github.com/jackc/pgx/v5/pgconn, so classifyRetryable can
// reach it via errors.As with no driver import entering entflow's
// non-test dependency graph — TestNoTransportDeps is the standing gate this
// file must never trip. *pgconn.PgError satisfies this interface
// structurally (it declares `func (pe *PgError) SQLState() string`)
// without entflow ever naming the pgx module.
type sqlStater interface {
	SQLState() string
}

// Retryable Postgres SQLSTATE codes (D-51; RESEARCH.md's Assumptions Log
// entry A4): serialization_failure and deadlock_detected. This set is
// deliberately conservative, not exhaustive — an unclassified transient
// error fails the run immediately, which is safe (inspectable, and from
// Phase 3 manually retryable) rather than silently wrong. Extending the set
// is additive: Options.RetryableError is how an application, or a MySQL
// deployment (which entflow has no typed error for at all), classifies more
// without entflow importing another driver.
const (
	sqlstateSerializationFailure = "40001"
	sqlstateDeadlockDetected     = "40P01"
)

// classifyRetryable reports whether err should schedule a retry rather than
// fail the run immediately, per D-51's three-layer, driver-agnostic policy,
// consulted in order:
//
//  1. A locally-declared sqlStater interface assertion (errors.As), treating
//     Postgres's serialization-failure and deadlock-detected SQLSTATE codes
//     as retryable.
//  2. The standard library's bad-connection sentinel (database/sql/driver),
//     covering connection loss on all three dialects with zero driver
//     import — errors.Is, not a type assertion, since this is a sentinel
//     value.
//  3. opts.RetryableError, the application-supplied hook, consulted only
//     when neither built-in matches — the seam a MySQL deployment uses to
//     add its own deadlock error numbers without entflow ever importing a
//     MySQL driver.
//
// Every other error is not retryable. Phase 1's declared Retry(Backoff(...))
// policy governs Activities only and takes effect in Phase 3; it is
// deliberately not consulted here — a DB step's retry is this conservative
// built-in policy, never a per-step declared one.
func classifyRetryable(err error, opts Options) bool {
	if err == nil {
		return false
	}

	var sqlErr sqlStater
	if errors.As(err, &sqlErr) {
		switch sqlErr.SQLState() {
		case sqlstateSerializationFailure, sqlstateDeadlockDetected:
			return true
		}
	}

	if errors.Is(err, driver.ErrBadConn) {
		return true
	}

	if opts.RetryableError != nil {
		return opts.RetryableError(err)
	}
	return false
}

// The conservative built-in DB-step retry schedule (D-51): exponential from
// backoffInitial, capped at backoffMax. DefaultMaxAttempts (worker/
// options.go, declared in plan 02-02 alongside the rest of Options' field
// surface) is the attempt ceiling this schedule backs.
const (
	backoffInitial = 100 * time.Millisecond
	backoffMax     = 2 * time.Second
)

// nextRetryAfter returns the wall-clock time a run should next become
// claimable after its attempt'th failed attempt (attempt is the new,
// post-increment attempt count — the first retry after a run's first
// failure is attempt 1). The delay follows AWS's "full jitter" algorithm
// (sleep = random_between(0, min(cap, base*2^attempt))) so a backing-off
// run neither spins the claim loop nor synchronizes with its siblings
// retrying the same failure at the same instant.
func nextRetryAfter(now time.Time, attempt int) time.Time {
	return now.Add(backoffDelay(attempt))
}

// backoffDelay computes the full-jitter delay for attempt (>= 1).
func backoffDelay(attempt int) time.Duration {
	ceiling := backoffMax
	d := backoffInitial
	for i := 1; i < attempt; i++ {
		if d >= ceiling {
			d = ceiling
			break
		}
		d *= 2
	}
	if d > ceiling {
		d = ceiling
	}
	if d <= 0 {
		d = ceiling
	}
	return time.Duration(rand.Int63n(int64(d)) + 1)
}
