package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/crashpoint"
	"github.com/smintz/entflow/internal/wfmarker"
)

// tracerName is the instrumentation name every span this package starts is
// recorded under (D-58) — the single call site that names the tracer, so
// every span in a trace attributes back to entflow/worker consistently.
const tracerName = "github.com/smintz/entflow/worker"

// claimTelemetry bundles the span-related values one claim's success and
// failure write paths both need to finish D-58/D-59's span recording —
// resolved once per claim (claimOnce), then passed into failStep so its own
// doc comment about sharing claimOnce's transaction/committed pointer stays
// accurate.
//
// firstClaim is keyed off run.TraceContext being empty, not
// run.CurrentStep == "" — deliberately: a retryable driver error on the
// run's very first step (D-51) leaves CurrentStep empty on every retried
// attempt, but the root span must be created exactly once, on the attempt
// that actually seeds trace_context. Keying on TraceContext instead means a
// retried first attempt correctly restores the same root rather than
// forking a second, unrelated trace.
type claimTelemetry struct {
	tracer     trace.Tracer
	spanCtx    context.Context
	rootSpan   trace.Span
	flow       string
	firstClaim bool
}

// startClaimTelemetry resolves opts.TracerProvider (defaulting to the
// no-op provider, D-58) and either starts the run's root span — on the
// claim that first seeds trace_context — or restores the persisted trace
// context as a remote parent for this claim's own step span (D-59).
//
// The root span is started and ended within the SAME claim that creates it;
// it is never held open across a claim boundary, because a span cannot
// survive a process boundary. A reader expecting a long-lived root span
// spanning the whole run's lifetime will not find one here — later claims
// restore its trace/span identifiers as a remote parent instead (see
// WithRestoredParent), which is what lets a step span in a different
// process still land in the same trace.
func startClaimTelemetry(ctx context.Context, opts Options, runID any, flow, storedTraceContext string) claimTelemetry {
	tracer := ResolveTracerProvider(opts.TracerProvider).Tracer(tracerName)
	firstClaim := storedTraceContext == ""

	spanCtx := ctx
	var rootSpan trace.Span
	if firstClaim {
		spanCtx, rootSpan = tracer.Start(ctx, RootSpanName(flow), trace.WithAttributes(RunAttributes(runID, flow)...))
	} else {
		spanCtx = WithRestoredParent(ctx, storedTraceContext)
	}

	return claimTelemetry{
		tracer:     tracer,
		spanCtx:    spanCtx,
		rootSpan:   rootSpan,
		flow:       flow,
		firstClaim: firstClaim,
	}
}

// finishSpans records this claim's step span (only when a step actually ran
// or errored — step == "" means every remaining step was skipped, and a
// skipped step never gets a span) and, on the claim that owns the root
// span, sets its final state attribute and ends it (D-58: "the run's
// terminal state appears as an attribute on the root span" — literally true
// whenever the run's first claim is also its last, e.g. a single-DB-step
// flow or a run whose every step is skipped; on a later claim the root span
// no longer exists to update, so only the step span carries state).
//
// stepErr, when non-nil, is recorded on the step span through the
// error-recording API with the span status set to error, rendered via
// stepErr.Error() — StepError's own message, which names the step and kind
// and never embeds the run's input bytes (T-02-03).
func (tel claimTelemetry) finishSpans(runID any, step string, attempt int, state string, stepErr error) {
	if step != "" {
		_, span := tel.tracer.Start(tel.spanCtx, SpanName(tel.flow, step), trace.WithAttributes(StepAttributes(runID, tel.flow, step, attempt, state)...))
		if stepErr != nil {
			span.RecordError(stepErr)
			span.SetStatus(codes.Error, stepErr.Error())
		}
		span.End()
	}
	if tel.firstClaim {
		tel.rootSpan.SetAttributes(AttrState.String(state))
		tel.rootSpan.End()
	}
}

// traceContextToPersist returns the value this claim's write should persist
// to trace_context: the newly-seeded root span's own context on the claim
// that created it, or the value already on the row, unchanged, on every
// later claim — trace_context is written exactly once per run, at whichever
// claim's write actually first commits it.
func (tel claimTelemetry) traceContextToPersist(existing string) string {
	if tel.firstClaim {
		return EncodeTraceContext(tel.rootSpan.SpanContext())
	}
	return existing
}

// claimStates are the two run states a claim query considers (D-32). done,
// cancelled, and every failed:<step> value are terminal and structurally
// unclaimable.
var claimStates = []string{entflow.RunStatePending, entflow.RunStateRunning}

// claimOnce is the phase's whole safety argument (D-30): it opens exactly
// one transaction, claims at most one run via strategy, executes exactly
// one DB step's closure against that transaction, writes the step's effect
// and the run's progress pointer, and commits — all before returning. A
// crash or panic anywhere in this window rolls the whole transaction back,
// leaving the row exactly as claimable as it was.
//
// claimed is false with a nil error when nothing was claimable — not an
// error condition. A claim cycle that finds nothing claimable starts no
// span at all: an empty poll is not a run event (see the early return right
// after strategy.Claim below, well before any telemetry is resolved).
func claimOnce(ctx context.Context, flowName string, store entflow.RunStore, runner entflow.Runner, strategy ClaimStrategy, opts Options) (claimed bool, err error) {
	txAny, err := store.BeginTx(ctx)
	if err != nil {
		return false, fmt.Errorf("entflow/worker: opening claim transaction: %w", err)
	}
	tx, ok := txAny.(entflow.Tx)
	if !ok {
		return false, fmt.Errorf("entflow/worker: claim transaction has type %T, want entflow.Tx", txAny)
	}

	committed := false
	defer func() {
		if !committed {
			// A no-op rollback after a successful commit is impossible here
			// because every return path below either commits and sets
			// committed=true, or returns before committing — so this defer
			// always rolls back an uncommitted transaction, never a
			// committed one.
			_ = tx.Rollback()
		}
	}()

	// The claim transaction's context derivation (D-44/D-45), immediately
	// after the transaction opens and before the run is hydrated — so
	// every hook, privacy rule and step closure that runs inside this
	// claim sees the result. Order is load-bearing and deliberate: the
	// application's Options.Context hook runs FIRST, deriving whatever
	// privileged viewer-bearing context the worker needs to touch a
	// privacy-governed entity at all; the workflow marker is applied to
	// THAT result, last, so the hook itself can never observe or forge the
	// marker it has not yet been given. A hook that could see the marker
	// could branch on it, which is the beginning of the escalation path
	// D-44 closes. When Options.Context is nil, ctx passes through
	// unchanged — a worker running against a privacy-governed entity with
	// no hook configured is honestly denied by that entity's own Policy(),
	// not silently granted access it was never given (see Options.Context's
	// own doc comment).
	if opts.Context != nil {
		ctx = opts.Context(ctx)
	}
	// This is the ONLY call to the internal package's setter anywhere in
	// the module, enforced by marker_test.go's AST scan
	// (TestWorkflowMarkerHasExactlyOneSetter).
	ctx = wfmarker.Set(ctx)

	q, ok := txAny.(entflow.RawQuerier)
	if !ok {
		return false, fmt.Errorf("entflow/worker: claim transaction has type %T, does not satisfy entflow.RawQuerier", txAny)
	}

	// The flow-scoped pre-claim boundary (D-56, crashpoint.PreClaimName):
	// fired once, before the claim query runs at all and therefore before
	// any row — hence any step — is even known. Nothing has been read or
	// written yet, so a hook error here rolls back a transaction that
	// never touched anything.
	if err := crashpoint.At(ctx, crashpoint.PreClaimName); err != nil {
		return false, fmt.Errorf("entflow/worker: crash point %q: %w", crashpoint.PreClaimName, err)
	}

	id, ok, err := strategy.Claim(ctx, q, store.Table(), claimStates)
	if err != nil {
		return false, fmt.Errorf("entflow/worker: claim query: %w", err)
	}
	if !ok {
		return false, nil
	}

	run, err := store.Load(ctx, txAny, id)
	if err != nil {
		return false, fmt.Errorf("entflow/worker: loading claimed run %v: %w", id, err)
	}

	claimable := false
	for _, s := range claimStates {
		if run.State == s {
			claimable = true
			break
		}
	}
	if !claimable {
		return false, fmt.Errorf("entflow/worker: run %v: %w: state is %q", id, entflow.ErrRunNotClaimable, run.State)
	}

	stepOrder, err := runner.StepOrder()
	if err != nil {
		return false, fmt.Errorf("entflow/worker: resolving step order: %w", err)
	}

	// tel resolves Options.TracerProvider (defaulting to the no-op provider)
	// and either starts this run's root span (on the claim that first seeds
	// trace_context, D-59) or restores the persisted trace context as this
	// claim's remote parent — see startClaimTelemetry's doc comment for why
	// the root span never survives past the claim that created it.
	tel := startClaimTelemetry(ctx, opts, id, flowName, run.TraceContext)

	// selfWas is the D-42 entry-time self-status snapshot, persisted on the
	// run row's self_was column: taken once, on the first claim of a run
	// (run.CurrentStep == ""), and read back from run.SelfWas (the value
	// RunStore.Load hydrated from that same self_was column) on every later
	// claim — never recomputed against the entity's live status, which is
	// exactly what keeps a SelfWas condition's D-10 entry semantics stable
	// across a crash and across processes.
	selfWas := run.SelfWas
	nextIdx := 0
	if run.CurrentStep == "" {
		selfWas, err = runner.EntrySelfStatus(ctx, txAny, run.Input)
		if err != nil {
			return false, fmt.Errorf("entflow/worker: reading entry self status: %w", err)
		}
	} else {
		found := false
		for i, name := range stepOrder {
			if name == run.CurrentStep {
				nextIdx = i + 1
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Errorf("entflow/worker: run %v: current_step %q not found in flow's step order", id, run.CurrentStep)
		}
	}
	if nextIdx >= len(stepOrder) {
		return false, fmt.Errorf("entflow/worker: run %v: current_step %q already the last step in the flow's order — should be unclaimable", id, run.CurrentStep)
	}
	targetStep := stepOrder[nextIdx]

	// AfterClaim and AfterHydrate (D-56) both fire here, immediately after
	// the claimed row has been read (store.Load, right above) and the step
	// this claim is about to work on has been identified (targetStep) —
	// deliberately positioned together rather than strictly at their
	// literal, separate code locations (AfterClaim technically precedes
	// store.Load; AfterHydrate follows it). Naming a crash point requires
	// knowing WHICH step it belongs to, and that is only knowable once
	// run.CurrentStep has been read and resolved into nextIdx — but
	// store.Load and the CurrentStep resolution above are both reads with
	// no durability consequence of their own, so firing both checks at
	// this single point produces an identical observable outcome (a
	// rollback with zero effect and zero progress-pointer change) as
	// firing AfterClaim strictly before store.Load would.
	if err := crashpoint.At(ctx, crashpoint.Name(targetStep, crashpoint.AfterClaim)); err != nil {
		return false, fmt.Errorf("entflow/worker: crash point %q: %w", crashpoint.Name(targetStep, crashpoint.AfterClaim), err)
	}
	if err := crashpoint.At(ctx, crashpoint.Name(targetStep, crashpoint.AfterHydrate)); err != nil {
		return false, fmt.Errorf("entflow/worker: crash point %q: %w", crashpoint.Name(targetStep, crashpoint.AfterHydrate), err)
	}

	adv := entflow.Advance{
		FromState:    run.State,
		FromStep:     run.CurrentStep,
		CurrentStep:  run.CurrentStep,
		Attempt:      run.Attempt,
		Results:      run.Results,
		TraceContext: run.TraceContext,
		SelfWas:      selfWas,
	}

	// Load the current claim's live self value once, before executing any
	// step — a fresh read on every claim, inside this claim's own
	// transaction, never cached across claims and never rehydrated from
	// persisted JSON (D-41). A flow that declares no WithSelfLoader gets
	// ErrNoSelfLoader here, which is not a claim failure: self simply stays
	// nil, and Self[T] inside a step reports that same sentinel.
	self, err := runner.LoadSelf(ctx, txAny, run.Input)
	if err != nil && !errors.Is(err, entflow.ErrNoSelfLoader) {
		return false, fmt.Errorf("entflow/worker: loading self for run %v: %w", id, err)
	}

	ranStep := ""
	failingStep := ""
	var stepErr error
	for i := nextIdx; i < len(stepOrder); i++ {
		name := stepOrder[i]
		if err := crashpoint.At(ctx, crashpoint.Name(name, crashpoint.BeforeStep)); err != nil {
			return false, fmt.Errorf("entflow/worker: crash point %q: %w", crashpoint.Name(name, crashpoint.BeforeStep), err)
		}
		outcome, execErr := runner.ExecStep(ctx, txAny, entflow.StepCall{
			Step:    name,
			Input:   run.Input,
			SelfWas: selfWas,
			Results: run.Results,
			Self:    self,
		})
		if execErr != nil {
			failingStep = name
			stepErr = execErr
			break
		}
		if !outcome.Ran {
			continue
		}
		if err := crashpoint.At(ctx, crashpoint.Name(name, crashpoint.AfterStep)); err != nil {
			return false, fmt.Errorf("entflow/worker: crash point %q: %w", crashpoint.Name(name, crashpoint.AfterStep), err)
		}
		ranStep = name
		if adv.Results == nil {
			adv.Results = map[string]json.RawMessage{}
		}
		if outcome.Result != nil {
			adv.Results[name] = outcome.Result
		}
		break
	}

	// DUR-07: make "after retries exhaust" literally true. A retryable
	// driver error (D-51) schedules a retry by incrementing attempt and
	// setting retry_after, leaving state running and current_step
	// unchanged, so the same step is retried on a later claim — until the
	// attempt ceiling is reached, at which point (or immediately, for a
	// non-retryable error, which never burns the counter) the run fails to
	// failed:<step> with the error recorded. Both writes go through
	// RunStore.Fail, guarded exactly like Advance: a guard miss means
	// another transaction already touched this run (most importantly,
	// cancelled it, D-43) between this claim's load and this write, and
	// must be handled — not ignored — by abandoning the claim rather than
	// resurrecting a run that is no longer ours.
	if stepErr != nil {
		return failStep(ctx, store, txAny, tx, id, run, failingStep, stepErr, opts, &committed, tel)
	}

	if ranStep != "" {
		adv.CurrentStep = ranStep
		if len(stepOrder) > 0 && stepOrder[len(stepOrder)-1] == ranStep {
			adv.ToState = entflow.RunStateDone
			adv.Finished = true
		} else {
			adv.ToState = entflow.RunStateRunning
		}
	} else {
		// Every remaining step's conditions evaluated false: walk straight
		// to done without having invoked a closure or recorded a result.
		adv.ToState = entflow.RunStateDone
		adv.Finished = true
	}

	// ranStep is "" exactly when every remaining step was skipped — the
	// D-58 empty edge: one root span (already started above), zero child
	// spans, finishSpans's step=="" branch a no-op.
	tel.finishSpans(id, ranStep, adv.Attempt, adv.ToState, nil)
	adv.TraceContext = tel.traceContextToPersist(run.TraceContext)

	advanced, err := store.Advance(ctx, txAny, id, adv)
	if err != nil {
		return false, fmt.Errorf("entflow/worker: advancing run %v: %w", id, err)
	}
	if !advanced {
		return false, fmt.Errorf("entflow/worker: advancing run %v: %w", id, entflow.ErrRunNotAdvanced)
	}

	// AfterAdvance and BeforeCommit (D-56) — the progress-pointer write has
	// returned successfully, and the transaction has not yet committed.
	// targetStep is used, not ranStep, so both boundaries stay named even
	// on the empty edge where every remaining step's conditions evaluated
	// false and ranStep is "" (adv.ToState walked straight to done with no
	// step closure invoked).
	if err := crashpoint.At(ctx, crashpoint.Name(targetStep, crashpoint.AfterAdvance)); err != nil {
		return false, fmt.Errorf("entflow/worker: crash point %q: %w", crashpoint.Name(targetStep, crashpoint.AfterAdvance), err)
	}
	if err := crashpoint.At(ctx, crashpoint.Name(targetStep, crashpoint.BeforeCommit)); err != nil {
		return false, fmt.Errorf("entflow/worker: crash point %q: %w", crashpoint.Name(targetStep, crashpoint.BeforeCommit), err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("entflow/worker: committing claim transaction: %w", err)
	}
	committed = true
	return true, nil
}

// failStep is claimOnce's DUR-07 terminal-states/retry branch, split out
// only for readability — it shares claimOnce's transaction (txAny/tx) and
// its committed flag (via pointer, so the shared defer in claimOnce still
// rolls back exactly once on any early return).
//
// classifyRetryable (retry.go) decides retryable vs. not. A retryable error
// below the attempt ceiling (DefaultMaxAttempts) schedules a retry: attempt
// increments, retry_after is set from the database-independent process
// clock via nextRetryAfter's exponential-with-jitter schedule, state stays
// running, and current_step is left untouched by RunStore.Fail (it has no
// CurrentStep field to set) — so the SAME step is retried on a later claim.
// Everything else — a retryable error at the ceiling, or any non-retryable
// error — fails the run to failed:<failingStep> immediately, recording
// stepErr's rendered message (StepError.Error(), which names the step and
// kind and never embeds the run's input bytes, T-02-03) in last_error. A
// non-retryable error never increments attempt at all — retrying a
// business-logic error would just burn the ceiling for no reason.
func failStep(ctx context.Context, store entflow.RunStore, txAny any, tx entflow.Tx, id any, run *entflow.Run, failingStep string, stepErr error, opts Options, committed *bool, tel claimTelemetry) (bool, error) {
	f := entflow.Fail{
		FromState: run.State,
		FromStep:  run.CurrentStep,
	}

	retryable := classifyRetryable(stepErr, opts)
	newAttempt := run.Attempt
	if retryable {
		newAttempt = run.Attempt + 1
	}

	if retryable && newAttempt < DefaultMaxAttempts {
		retryAt := nextRetryAfter(time.Now(), newAttempt)
		f.ToState = entflow.RunStateRunning
		f.Attempt = newAttempt
		f.RetryAfter = &retryAt
	} else {
		f.ToState = entflow.RunStateFailed(failingStep)
		f.Attempt = newAttempt
		f.LastError = stepErr.Error()
	}

	// The failing step's span records stepErr through the error-recording
	// API with an error status (D-58); the root span (first claim only)
	// gets f.ToState as its final state attribute — see finishSpans' doc
	// comment for the "root span only outlives the claim that created it"
	// caveat this shares with the success path.
	tel.finishSpans(id, failingStep, f.Attempt, f.ToState, stepErr)
	f.TraceContext = tel.traceContextToPersist(run.TraceContext)

	failed, ferr := store.Fail(ctx, txAny, id, f)
	if ferr != nil {
		return false, fmt.Errorf("entflow/worker: failing run %v: %w", id, ferr)
	}
	if !failed {
		// The guard (FromState/FromStep) did not match — another
		// transaction already touched this run (e.g. cancelled it, D-43)
		// since this claim's load. Report it and let the caller's deferred
		// rollback abandon the claim; never overwrite.
		return false, fmt.Errorf("entflow/worker: failing run %v: %w", id, entflow.ErrRunNotAdvanced)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("entflow/worker: committing claim transaction: %w", err)
	}
	*committed = true
	return true, nil
}
