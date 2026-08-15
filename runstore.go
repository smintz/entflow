package entflow

import (
	"context"
	"encoding/json"
	"time"
)

// RunStore is the tx-erased port (D-24) the worker reaches run rows
// through. entflow core can never name the application's generated ent
// types, so every method here takes the transaction as any, matching the
// erasure Exec(ctx, tx any, in In) and WithSelfStatus already established
// (exec.go) — no new erasure mechanism is invented. BeginTx lives on the
// store because the store is the one component that legitimately knows the
// application's generated client; the value it returns must satisfy the
// existing Tx interface, asserted comma-ok with a descriptive error by every
// caller.
//
// A concrete RunStore is hand-written per flow in Phase 2 (Phase 6 generates
// it). See internal/testdata/entflowfixture for the reference
// implementation this plan's tests exercise against a real generated ent
// client.
type RunStore interface {
	// BeginTx opens a new transaction, returned boxed as any so RunStore
	// stays generic over the application's generated transaction type. The
	// returned value must satisfy Tx (Commit/Rollback) and, for any dialect
	// the worker's ClaimStrategy runs the claim query against, RawQuerier.
	BeginTx(ctx context.Context) (any, error)

	// Table names the run table's storage identity — the identifiers a
	// dialect-specific ClaimStrategy builds its claim query from, without
	// ever naming a generated ent type.
	Table() RunTable

	// Insert persists one new run row in the pending state and returns its
	// ID, boxed as any because the owning application's generated ID type
	// (int, uuid.UUID, etc.) is never known to entflow core.
	Insert(ctx context.Context, tx any, in InsertRun) (runID any, err error)

	// Claim is a convenience, non-concurrency-safe resolution from a
	// ClaimQuery to a candidate run ID for callers that are not the
	// worker's own claim loop (e.g. a single-process dev tool). The
	// worker's real mutual-exclusion guarantee comes exclusively from
	// worker.ClaimStrategy running its dialect-specific SQL directly
	// against the transaction's RawQuerier (D-36) — Claim here never
	// duplicates that SQL and must not be relied on for concurrent,
	// multi-worker correctness.
	Claim(ctx context.Context, tx any, q ClaimQuery) (runID any, ok bool, err error)

	// Load hydrates the full run row named by runID on the given
	// transaction.
	Load(ctx context.Context, tx any, runID any) (*Run, error)

	// Advance conditionally updates a claimed run's progress pointer,
	// state, results, and self_was snapshot in the same transaction as the
	// step's own effect (D-39). The update is guarded by adv.FromState and
	// adv.FromStep matching the row's current values; the returned bool
	// reports whether the guard matched — a false result (with a nil error)
	// means another transaction already advanced this run, and the caller
	// must not treat its own step effect as durable.
	//
	// Results is carried on Advance rather than a separate record-result
	// method deliberately: D-39 requires the step's results to commit in
	// the exact same write as the effect and the progress pointer, and a
	// separate method would invite a second write that could land apart
	// from them.
	Advance(ctx context.Context, tx any, runID any, adv Advance) (ok bool, err error)

	// Fail conditionally moves a claimed run to a failed:<step> (or
	// retry-scheduled running) state, recording the last error. Guarded the
	// same way Advance is.
	Fail(ctx context.Context, tx any, runID any, f Fail) (ok bool, err error)

	// Cancel conditionally moves a run from pending/running to cancelled
	// (D-43) — a state transition, not a signal. The returned bool reports
	// whether the run was in a cancellable state.
	Cancel(ctx context.Context, tx any, runID any) (ok bool, err error)
}

// InsertRun carries the fields entflow.Start (engine.go) supplies when
// persisting a new run row.
type InsertRun struct {
	// State is the run's initial stored state — always RunStatePending in
	// Phase 2.
	State string
	// Input is the flow's input, already marshaled through its codec.
	Input []byte
	// OwnerRef is the D-26 lineage edge target, resolved by the flow's
	// declared WithOwnerRef, boxed as any because entflow core never names
	// the owning aggregate's ID type. Nil when the flow declares no
	// WithOwnerRef.
	OwnerRef any
}

// ClaimQuery parameterizes RunStore.Claim's convenience resolution — see
// Claim's doc comment for why this is not the worker's real claim
// mechanism.
type ClaimQuery struct {
	// States lists the run states considered claimable.
	States []string
}

// Advance carries every field a successful claim's progress-pointer write
// touches. The leading From* fields are the conditional-update guard; every
// other field is the new value to write when the guard matches.
type Advance struct {
	// FromState is the run's expected current state — part of the guard.
	FromState string
	// FromStep is the run's expected current_step — part of the guard.
	FromStep string
	// ToState is the state to write: RunStateRunning when more steps
	// remain, RunStateDone when the executed step was the last, or
	// RunStateDone with no step executed when every remaining step's
	// conditions evaluated false.
	ToState string
	// CurrentStep is the step name to write as current_step. Equal to
	// FromStep when no step actually ran (every remaining step skipped).
	CurrentStep string
	// Attempt is the attempt counter to write.
	Attempt int
	// Results is the full results map to persist, including any newly
	// recorded step result.
	Results map[string]json.RawMessage
	// RetryAfter, when non-nil, gates the run's next claimability by time
	// (D-29). Unused by Phase 2's tracer (no retry scheduling yet).
	RetryAfter *time.Time
	// TraceContext is the OTel trace context to persist (D-59). Unused
	// until plan 02-06 wires real spans.
	TraceContext string
	// SelfWas is the entry-time self-status snapshot (D-42) to persist.
	SelfWas string
	// Finished reports whether ToState is a terminal state, so the
	// implementation knows to set finished_at.
	Finished bool
}

// Fail carries the fields a failed claim's write touches.
type Fail struct {
	// FromState is the run's expected current state — part of the guard.
	FromState string
	// FromStep is the run's expected current_step — part of the guard.
	FromStep string
	// ToState is the terminal failed:<step> state, or RunStateRunning when
	// scheduling a retry via RetryAfter.
	ToState string
	// LastError is the error message to persist. Never embeds raw input
	// bytes (T-01-05), mirroring the codec's own error discipline.
	LastError string
	// Attempt is the attempt counter to write.
	Attempt int
	// RetryAfter, when non-nil, schedules the run's next claimable time
	// rather than failing it terminally.
	RetryAfter *time.Time
}
