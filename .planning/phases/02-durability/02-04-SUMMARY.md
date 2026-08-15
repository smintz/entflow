---
phase: 02-durability
plan: 04
subsystem: database
tags: [ent, worker, sqlite, results, self-loader, retry, json]

# Dependency graph
requires:
  - phase: 02-durability
    provides: "plan 02-02's Runner/StepCall/StepOutcome/RunStore/Advance/Fail seams and worker/dbstep.go's claim-execute-advance transaction, and plan 02-03's certified SkipLockedStrategy/N-goroutine claim pool this plan's retry classification threads Options through"
provides:
  - "entflow.Result[T] re-backed by a JSON-only resultStore (map[string]json.RawMessage), with putResult/withResultsFrom marshaling on every path (D-40) — no live-value shortcut for an uninterrupted worker pass"
  - "entflow.WithSelfLoader[In,TX,Self] / entflow.Self[T] — a live per-step entity re-read inside the current claim's own transaction (D-41), the third instance of the WithSelfStatus/WithOwnerRef erasure pattern"
  - "entflow.ErrNoSelfLoader, entflow.Runner.LoadSelf, StepCall.Self — the tx-erased seam worker/dbstep.go calls once per claim, before the step loop"
  - "worker/retry.go: classifyRetryable's three-layer driver-agnostic policy (sqlStater interface assertion, driver.ErrBadConn, Options.RetryableError), worker.DefaultMaxAttempts-backed exponential-with-jitter backoff (nextRetryAfter/backoffDelay)"
  - "worker/dbstep.go's failStep: a step-closure error now schedules a retry (attempt++, retry_after set, state stays running) or fails the run to failed:<step> (last_error, finished_at) through RunStore.Fail, guarded exactly like Advance"
affects: [02-05, 02-06, 02-07, 02-08]

# Actuals (#2632)
actuals:
  tokens: 14862
  tasks: 3
  commits: 3

tech-stack:
  added: []
  patterns:
    - "resultStore holds map[string]json.RawMessage only; putResult/marshalResult marshal on every write (D-40), withResultsFrom hydrates a fresh store directly from a run row's persisted results map with no re-marshal — one JSON-backed store shape for both Exec's Phase-1 in-tx path and Runner.ExecStep's durable path"
    - "WithSelfLoader/Self[T]/selfKey/setSelf: the third instance of the WithSelfStatus/WithOwnerRef type-erasure pattern (declared-type recorded on flowConfig, New[In] eager mismatch panic, comma-ok resolution). Self is attached to a step's context by ExecStep itself (via StepCall.Self), never by the worker package directly, keeping the cross-package boundary at the same Runner seam every other per-step fact already crosses through"
    - "Three deliberately asymmetric per-step data guarantees, now all documented on each other: Result[T] is inert JSON-round-tripped data (every path, D-40); Self[T] is a live re-read inside the current claim's own transaction, never cached (D-41); a SelfWas condition is a snapshot frozen once at flow entry and persisted on the run row (D-10/D-42)"
    - "worker/dbstep.go's failStep: a step error is now always a claimed outcome (claimed=true, err=nil) when the guard matches — the same treatment as a successful Advance — since a run reaching a failed:<step> terminal state is a normal domain outcome, not a worker-level Go error. Only a guard miss (ErrRunNotAdvanced) or a genuine write failure surfaces as an error from ClaimOnce"
    - "classifyRetryable declares its own single-method sqlStater interface (SQLState() string) rather than importing github.com/jackc/pgx/v5/pgconn — *pgconn.PgError satisfies it structurally, so TestNoTransportDeps stays green with zero new modules"

key-files:
  created:
    - selfloader.go
    - selfloader_test.go
    - worker/retry.go
    - worker/retry_test.go
  modified:
    - result.go
    - result_test.go
    - exec.go
    - runner.go
    - errors.go
    - flow.go
    - worker/dbstep.go
    - worker/worker.go
    - internal/testdata/ent/schema/order_flows.go

key-decisions:
  - "D-40 ratified as a real behavior change from Phase 1, not just a doc comment: TestResultTypedHit moved from require.Same to require.Equal + require.NotSame, since every path (including a single uninterrupted worker pass) now round-trips a step's result through JSON — no live-value shortcut."
  - "ErrResultTypeMismatch's rendered message can no longer name the original Go type a value was recorded under (only JSON bytes are stored now) — result_test.go's mismatch assertion was narrowed to check the step name and wanted type only, dropping the now-unavailable source-type substring check."
  - "Self[T]'s value crosses the entflow/worker package boundary via StepCall.Self (a new field on the existing tx-erased seam), not via an exported context-setter function — ExecStep (package entflow) attaches it to the step closure's context internally, exactly how results/self_was already cross that boundary. No new cross-package API surface was needed."
  - "worker/retry.go reuses worker.DefaultMaxAttempts (already declared in worker/options.go by plan 02-02) rather than redeclaring it, per the plan's own read_first pointer to that file."
  - "A step-closure error is now always a claimed=true outcome from ClaimOnce once the guard matches (whether it schedules a retry or fails the run to failed:<step>) — only a guard miss or a genuine write error is a Go error. This is a deliberate DUR-07 semantics change from plan 02-02/02-03, where any execErr from ExecStep silently rolled back and returned an unhandled Go error the poll loop's own error-swallowing made invisible."

requirements-completed: [DUR-07]

coverage:
  - id: D1
    description: "Result[T] keeps its exact Phase 1 signature and now reads from the run row's persisted results, with every path (including a single uninterrupted worker pass) round-tripping through JSON, proven inert (unexported fields do not survive) and readable-and-equal after a simulated reload"
    requirement: DUR-07
    verification:
      - kind: unit
        ref: "result_test.go#TestResultTypedHit"
        status: pass
      - kind: unit
        ref: "result_test.go#TestResultUnexportedFieldDoesNotSurviveRoundTrip"
        status: pass
      - kind: unit
        ref: "result_test.go#TestResultRoundTripsAfterReload"
        status: pass
      - kind: unit
        ref: "result_test.go#TestResultUnknownForSkippedStep"
        status: pass
    human_judgment: false
  - id: D2
    description: "Self[T] returns the owning entity re-read live inside the current step's own transaction, observing an earlier step's mutation from an earlier claim; a flow with no declared WithSelfLoader gets ErrNoSelfLoader; a mismatched WithSelfLoader input type panics from New"
    requirement: DUR-07
    verification:
      - kind: unit
        ref: "selfloader_test.go#TestSelfIsLiveAcrossClaims"
        status: pass
      - kind: unit
        ref: "selfloader_test.go#TestSelfNoLoaderReturnsErrNoSelfLoader"
        status: pass
      - kind: unit
        ref: "selfloader_test.go#TestFlowLoadSelfWithoutDeclaredLoaderReturnsErrNoSelfLoader"
        status: pass
      - kind: unit
        ref: "selfloader_test.go#TestNewPanicsOnSelfLoaderTypeMismatch"
        status: pass
    human_judgment: false
  - id: D3
    description: "A SelfWas-gated step still evaluates against the entry-time snapshot (D-10/D-42) after the entity's live status has changed and the run row has been reloaded across two separate claims — the snapshot is read from the run row's self_was column, never recomputed"
    requirement: DUR-07
    verification:
      - kind: unit
        ref: "selfloader_test.go#TestSelfWasSnapshotSurvivesEntityMutationAndReload"
        status: pass
    human_judgment: false
  - id: D4
    description: "A run whose steps all succeed reaches done with results recorded and an empty last_error; a step failing with a retryable driver error schedules a retry (attempt++, retry_after set, state running, current_step unchanged) and fails to failed:<step> (non-empty last_error, finished_at set) only once DefaultMaxAttempts is reached; a non-retryable error fails immediately without incrementing attempt; Options.RetryableError classifies an otherwise-unrecognized error"
    requirement: DUR-07
    verification:
      - kind: unit
        ref: "worker/retry_test.go#TestRetryScheduledOnRetryableError"
        status: pass
      - kind: unit
        ref: "worker/retry_test.go#TestRetryFailsAtCeilingAfterExhaustingAttempts"
        status: pass
      - kind: unit
        ref: "worker/retry_test.go#TestNonRetryableErrorFailsImmediately"
        status: pass
      - kind: unit
        ref: "worker/retry_test.go#TestDoneRunHasEmptyLastError"
        status: pass
      - kind: unit
        ref: "worker/retry_test.go#TestRetryableErrorHookClassifiesUnrecognizedError"
        status: pass
    human_judgment: false
  - id: D5
    description: "A guarded failure write against a run that was concurrently cancelled reports the guard miss and does not overwrite the cancellation; the retry classification imports no database driver (TestNoTransportDeps stays green, zero jackc packages in worker's non-test dependency graph)"
    requirement: DUR-07
    verification:
      - kind: unit
        ref: "worker/retry_test.go#TestGuardMissDoesNotOverwriteRun"
        status: pass
      - kind: other
        ref: "go test ./... -run TestNoTransportDeps -count=1; go list -deps ./worker | grep -c jackc"
        status: pass
    human_judgment: false

duration: ~22min
completed: 2026-08-15
status: complete
---

# Phase 02 Plan 04: Results, live self, and honest terminal states Summary

**`entflow.Result[T]` is re-backed by a JSON-only store so every execution path — including a single uninterrupted worker pass — produces the same inert value; `entflow.WithSelfLoader`/`Self[T]` give a step a live, per-claim re-read of its owning entity; and `worker/dbstep.go`'s new retry/fail branch makes DUR-07's "after retries exhaust" a tested behavior, with a driver-agnostic SQLSTATE classifier that imports no database driver.**

## Performance

- **Duration:** ~22 min
- **Started:** 2026-08-15T12:33:00Z (approx.)
- **Completed:** 2026-08-15T12:55:00Z (approx.)
- **Tasks:** 3 (all `type="auto"`)
- **Files modified:** 13 (4 created, 9 modified)

## Accomplishments

- `result.go`'s `resultStore` now holds `map[string]json.RawMessage` exclusively; `putResult`/`marshalResult` marshal on the way in and `withResultsFrom` hydrates a fresh store directly from a run row's persisted results with no re-marshal, so a step calling `Result[T]` for a prior step behaves identically whether or not the run crashed between claims (D-40) — proven by `TestResultRoundTripsAfterReload` and `TestResultUnexportedFieldDoesNotSurviveRoundTrip`, and by `TestResultTypedHit`'s deliberate move from `require.Same` to `require.Equal`+`require.NotSame`.
- `selfloader.go` adds `entflow.WithSelfLoader[In,TX,Self]` and `entflow.Self[T]` — the third instance of the `WithSelfStatus`/`WithOwnerRef` type-erasure pattern. `worker/dbstep.go` calls the new `Runner.LoadSelf` once per claim, before the step loop, and `Runner.ExecStep` (via the new `StepCall.Self` field) attaches the live value to the step closure's context — a fresh read inside the claim's own transaction, never cached and never rehydrated from JSON (D-41). `TestSelfIsLiveAcrossClaims` proves step two observes step one's mutation from an *earlier claim's* committed transaction; `TestSelfWasSnapshotSurvivesEntityMutationAndReload` proves the deliberately opposite guarantee — a `SelfWas` condition still evaluates against the entry-time snapshot persisted on the run row (D-10/D-42), even after the live entity has since changed and the run row was reloaded in a separate claim.
- `worker/retry.go`'s `classifyRetryable` implements D-51's three-layer policy — a locally-declared `sqlStater` interface (`SQLState() string`, which `*pgconn.PgError` satisfies structurally with zero pgx import), `errors.Is(err, driver.ErrBadConn)`, then `Options.RetryableError` — backing a full-jitter exponential schedule (100ms→2s) via `nextRetryAfter`/`backoffDelay`, reusing `worker.DefaultMaxAttempts` (already declared in plan 02-02) rather than redeclaring it.
- `worker/dbstep.go`'s new `failStep` makes a step-closure error a real, tested outcome: a retryable error below the attempt ceiling schedules a retry (`attempt++`, `retry_after` set, state stays running, `current_step` untouched); everything else fails the run to `failed:<step>` immediately with `StepError`'s rendered message in `last_error` and `finished_at` set. Every failure write is guarded exactly like `Advance` — `TestGuardMissDoesNotOverwriteRun` proves a stale write against a concurrently-cancelled run reports the guard miss rather than overwriting.
- `go test ./... -run TestNoTransportDeps` and `go list -deps ./worker | grep -c jackc` (0) both stay green — the retry classification adds no module to entflow's non-test dependency graph.

## Task Commits

1. **Task 1: Re-back Result[T] with the run row's persisted results, always through JSON** - `a64136d` (feat)
2. **Task 2: WithSelfLoader and Self[T] — a live entity per step, a persisted snapshot for conditions** - `e0a8db0` (feat)
3. **Task 3: Terminal states and DB-step retry — done, failed:<step>, and a conservative driver-error policy** - `c9144b9` (feat)

## Files Created/Modified

- `result.go` - `resultStore` now `map[string]json.RawMessage`; `marshalResult`/`putResult`/`withResultsFrom`; `Result[T]` decodes via `encoding/json`
- `exec.go` - `Exec`'s loop propagates `putResult`'s (now fallible) marshal error
- `runner.go` - `Runner.LoadSelf`, `StepCall.Self`; `ExecStep` hydrates via `withResultsFrom` and attaches `Self` to the step closure's context
- `result_test.go` - D-40 round-trip/inertness/skipped-step coverage; `TestResultTypedHit`/`TestResultTypeMismatch` updated for the new JSON-only contract
- `selfloader.go` - `WithSelfLoader[In,TX,Self]`, `Self[T]`, `selfKey`/`setSelf`
- `selfloader_test.go` - live-across-claims, no-loader, mismatched-input-panic, and SelfWas-snapshot-survives-reload coverage
- `errors.go` - `ErrNoSelfLoader`
- `flow.go` - `flowConfig`/`FlowOf` carry `selfLoader`/`selfLoaderInType`; `New[In]` eager mismatch panic
- `internal/testdata/ent/schema/order_flows.go` - `CancelOrder` now declares `WithSelfLoader`
- `worker/dbstep.go` - `LoadSelf` invoked once per claim; `failStep` (retry-schedule / terminal-fail branch), guarded like `Advance`
- `worker/worker.go` - `ClaimOnce` threads `w.opts` through to `claimOnce`
- `worker/retry.go` - `classifyRetryable`, `sqlStater`, `nextRetryAfter`/`backoffDelay`
- `worker/retry_test.go` - retry-scheduled, ceiling-fails, non-retryable-immediate, done-has-empty-last_error, RetryableError-hook, and guard-miss coverage

## Decisions Made

- **D-40 ratified as an executable, deliberate behavior change from Phase 1** — every path (including a single uninterrupted worker pass) now round-trips a step's result through JSON, proven by moving `TestResultTypedHit` from pointer-identity to value-equality assertions and adding an explicit unexported-field inertness test.
- **`ErrResultTypeMismatch`'s message can no longer name the original Go type** a value was recorded under, since the store now holds only JSON bytes — `result_test.go`'s mismatch test was narrowed to check the step name and wanted type, dropping the source-type substring check the Phase 1 test relied on.
- **`Self[T]`'s live value crosses the `entflow`/`entflow/worker` package boundary via `StepCall.Self`**, a new field on the existing tx-erased `Runner.ExecStep` seam — not via a new exported context-setter function. `ExecStep` (package `entflow`) attaches it internally, exactly how results and `self_was` already cross that boundary; no new cross-package API surface was needed.
- **`worker/retry.go` reuses `worker.DefaultMaxAttempts`** (declared in `worker/options.go` by plan 02-02) rather than redeclaring it, per the plan's own read_first pointer.
- **A step-closure error is now always a `claimed=true` outcome from `ClaimOnce`** once the guard matches, whether it schedules a retry or fails the run to `failed:<step>` — only a guard miss or a genuine write error surfaces as a Go error. This is a deliberate DUR-07 semantics change: previously (plans 02-02/02-03) any `execErr` from `ExecStep` rolled the transaction back and returned an unhandled Go error the poll loop's own error-swallowing made invisible; now a step failure is a durable, inspectable domain outcome recorded on the run row.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `selfloader_test.go`'s `newNoSelfLoaderFlow` step name collided with `CancelOrderFlowRun`'s enum once Task 3 landed**
- **Found during:** Task 3, full-suite regression run after `worker/dbstep.go`'s retry/fail branch landed
- **Issue:** `newNoSelfLoaderFlow`'s one step was named `"observe"` and shared the `CancelOrderFlowRun` table (`entflowfixture.New`). Task 3 made a step-closure error write a `failed:<step>` state through `RunStore.Fail`, but `CancelOrderFlowRun`'s `state` enum (derived from `RunMixin`, D-27) only contains `failed:<step>` values for the real `CancelOrder` flow's own step names (`cancel`, `refund`, `order.cancelled`). Writing `failed:observe` failed ent's own field validator.
- **Fix:** Renamed the fixture's step to `"cancel"`, the one name every fixture flow sharing this table can safely fail under, and documented why in the function's doc comment.
- **Files modified:** selfloader_test.go
- **Verification:** full suite green.
- **Committed in:** c9144b9 (Task 3 commit)

**2. [Rule 1 - Bug] `TestSelfNoLoaderReturnsErrNoSelfLoader` asserted the pre-Task-3 error-propagation contract**
- **Found during:** Task 3, same regression run
- **Issue:** The test asserted `w.ClaimOnce` returned `(false, error)` with `errors.Is(err, entflow.ErrNoSelfLoader)` — true before Task 3 (any `execErr` from `ExecStep` was an unhandled Go error), but Task 3 deliberately routes a step-closure error (including `Self[T]`'s `ErrNoSelfLoader`) into `failStep`, which classifies it as non-retryable and fails the run, returning `(true, nil)` from `ClaimOnce` on a matched guard — the error is recorded in `last_error`, not returned as a Go error.
- **Fix:** Narrowed the test to a direct `Self[T]`/`Runner.LoadSelf` unit check (`TestSelfNoLoaderReturnsErrNoSelfLoader` + new `TestFlowLoadSelfWithoutDeclaredLoaderReturnsErrNoSelfLoader`), which is unaffected by the worker-level semantics change and still proves the exact acceptance criterion ("a flow without a declared loader gets ErrNoSelfLoader rather than a panic").
- **Files modified:** selfloader_test.go
- **Verification:** both new tests pass; full suite green.
- **Committed in:** c9144b9 (Task 3 commit)

---

**Total deviations:** 2 auto-fixed (both Rule 1 — bugs in Task-2-authored test fixtures surfaced by Task 3's intended DUR-07 semantics change, not scope creep). No architectural changes.

## Issues Encountered

- `TestPanickingStep` (plan 02-03, `worker/worker_test.go`, untouched by this plan) shares `CancelOrderFlowRun` via its own locally-named `"boom"` step, which is likewise not part of that table's `failed:<step>` enum. After Task 3, its panic still ends up attempted through `failStep`/`store.Fail`, which now fails ent's field validator and returns an error `claimOnce` propagates — but that error was already silently swallowed by `pollLoop` before Task 3 (a pre-existing, unrelated design choice), so the test's observable assertion (`state` stays `pending`, unclaimed) is unaffected and the suite stays green with no change to that file. This is a real, general limitation worth flagging for Phase 6: a hand-written or generated `RunStore` sharing a table across unrelated flows can never legally fail on a step name outside the table-owning flow's own declared step graph — a per-flow generated run table (Phase 6's norm) makes this a non-issue by construction. Not fixed here — out of this plan's file scope (`worker/worker_test.go` is not in Task 3's `<files>`), and the observable behavior of the existing test did not regress.
- `gofmt -l` flags `worker/conformance_test.go` (a pre-existing formatting drift from plan 02-03, unrelated to any file this plan touches) — left untouched per the scope boundary rule; every file this plan created or modified is gofmt-clean.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- `entflow.Result[T]`, `entflow.Self[T]`, and a `SelfWas` condition now have their three-way asymmetry (inert/live/snapshot) both documented on all three accessors and asserted by tests — plan 02-05 (privacy/policy) and later phases can rely on this contract being load-bearing, not aspirational.
- DUR-07 is fully proven: done with results recorded, `failed:<step>` after retries exhaust, retry only on classified driver errors, guarded writes that never resurrect a concurrently-cancelled run.
- A known, documented limitation carries forward to Phase 6: a step name shared across unrelated flows on the same hand-written run table cannot legally reach a `failed:<step>` state outside its own flow's declared step graph. Phase 6's per-flow generated run tables resolve this by construction — no interim fix is needed before then.
- No blockers.

---
*Phase: 02-durability*
*Completed: 2026-08-15*

## Self-Check: PASSED

All created/modified files verified present on disk (result.go, result_test.go, exec.go, runner.go, selfloader.go, selfloader_test.go, errors.go, flow.go, internal/testdata/ent/schema/order_flows.go, worker/dbstep.go, worker/worker.go, worker/retry.go, worker/retry_test.go). All referenced commit hashes (a64136d, e0a8db0, c9144b9) verified present in `git log --oneline --all`.
