package worker

import (
	"context"
	"time"
)

// Default* constants are the values New fills unset Options fields with
// (D-33, D-49, D-51).
const (
	// DefaultConcurrency is the number of interchangeable claim goroutines
	// a worker pool runs when Options.Concurrency is unset. Forced to at
	// most 1 on SQLite (D-36) — New refuses a higher value rather than
	// silently clamping it.
	DefaultConcurrency = 4
	// DefaultPollInterval is the base interval between poll ticks (D-33).
	DefaultPollInterval = time.Second
	// DefaultJitter is the fractional jitter applied to PollInterval —
	// documented mitigation for the poll-storm trap. Not optional in
	// spirit: New always applies some jitter unless Jitter is explicitly
	// set to exactly 0.
	DefaultJitter = 0.25
	// DefaultDrainTimeout is how long graceful Shutdown waits for in-flight
	// step transactions to commit before cancelling their contexts (D-49).
	DefaultDrainTimeout = 30 * time.Second
	// DefaultMaxAttempts is the conservative built-in DB-step retry policy's
	// attempt ceiling (D-51). Unused until a later plan wires DB-step retry.
	DefaultMaxAttempts = 3
)

// Options configures a Worker at construction time. See worker.New. Every
// field the phase will need is declared up front, so no later plan has to
// widen the struct and collide with a sibling.
//
// The worker is a pool, not a singleton — see "The worker is a pool, not a
// singleton" in docs/dialects.md and the doc comment on Worker.
type Options struct {
	// Dialect names the database dialect the worker's run stores are
	// backed by — "postgres"/"postgresql", "mysql", or "sqlite"/"sqlite3".
	// Resolved into a ClaimStrategy at construction (D-37), never lazily at
	// first claim.
	Dialect string

	// Flows restricts which registered flow names this worker polls. Empty
	// means every flow registered on the Engine.
	Flows []string

	// Concurrency is the number of interchangeable claim goroutines this
	// worker pool runs (D-31: concurrency comes from N independent claim
	// transactions, never from batching one claim). Forced to at most 1 on
	// SQLite (D-36) — New refuses a higher value rather than silently
	// clamping it. Defaults to DefaultConcurrency.
	Concurrency int

	// PollInterval is the base interval between poll ticks (D-33). Defaults
	// to DefaultPollInterval.
	PollInterval time.Duration

	// Jitter is the fractional jitter applied to PollInterval, in [0,1).
	// Defaults to DefaultJitter.
	Jitter float64

	// DrainTimeout bounds graceful Shutdown (D-49): after this long waiting
	// for in-flight step transactions to commit, Shutdown cancels their
	// contexts instead, which rolls them back — the same claimable-again
	// outcome D-30 gives a crash. Defaults to DefaultDrainTimeout.
	DrainTimeout time.Duration

	// Context, when non-nil, derives the context each claimed step's
	// closure runs under from the worker's own polling context — the seam
	// an application uses to attach its own privileged viewer (D-45: the
	// worker obtains its own viewer this way, since entflow core cannot
	// name the application's viewer type) or other request-scoped values.
	Context func(context.Context) context.Context

	// TracerProvider will supply the OpenTelemetry TracerProvider per-run
	// and per-step spans (D-58) are created against, once D-57's narrow,
	// named exception to the META-02 dependency test (D-04) lands and
	// entflow core takes its one sanctioned dependency beyond ent —
	// go.opentelemetry.io/otel/trace, API only, never the SDK. This plan
	// adds no new module (T-02-SC; TestNoTransportDeps is the standing
	// gate this field must not trip), so the field is typed any here
	// rather than trace.TracerProvider; it narrows to the real type the
	// moment that plan lands, with no call-site change for a caller who
	// passes nil. Defaults to nil (no tracing), so an application that
	// wires nothing pays nothing.
	TracerProvider any

	// RetryableError classifies an error returned from a DB step's closure
	// (or from the database driver) as retryable — consumed by the DB-step
	// retry policy a later plan wires. Nil means no error is treated as
	// retryable yet.
	RetryableError func(error) bool

	// ClaimStrategy overrides the strategy StrategyForDialect(Dialect)
	// would otherwise select. Nil selects the dialect's ratified default.
	ClaimStrategy ClaimStrategy
}

// withDefaults fills every unset Options field with its Default* constant,
// leaving explicitly-set fields (including an explicit zero Jitter)
// untouched.
func (o Options) withDefaults() Options {
	if o.Concurrency <= 0 {
		o.Concurrency = DefaultConcurrency
	}
	if o.PollInterval <= 0 {
		o.PollInterval = DefaultPollInterval
	}
	if o.Jitter == 0 {
		o.Jitter = DefaultJitter
	}
	if o.DrainTimeout <= 0 {
		o.DrainTimeout = DefaultDrainTimeout
	}
	return o
}
