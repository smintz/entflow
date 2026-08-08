---
phase: 01-runtime-core
plan: 03
subsystem: runtime
tags: [go, generics, ent, workflow, di]

# Dependency graph
requires:
  - phase: 01-01
    provides: "Flow/FlowOf[In]/New[In]/FlowsOf builder surface, StepKind, the Exec(ctx, tx, in) seam"
  - phase: 01-02
    provides: "Registry/Use[T]/TryUse[T], Codec[In], Result[T], StepError/ErrRequiresDurableRun"
provides:
  - "entflow.Step/CreateSelf/UpdateSelf/Create/Update/Query/Check — the full DB-step constructor set over one shared addDBStep mechanism (CORE-03)"
  - "entflow.Activity[In, Ent, Out] and entflow.Emit[In] — declarable-but-not-executable step kinds whose builder data (retry policy, emit topic, conditions) is fully readable in Phase 1 (D-13)"
  - "internal/testdata/compilefail — a machine-checked proof that an Activity closure cannot declare a transaction parameter (CORE-04)"
  - "entflow.JSON[Out]/NewJSON, entflow.Attempt.IdempotencyKey() — the Activity result wrapper and the ACT-02 idempotency-key derivation, both implemented ahead of Phase 3's executor"
  - "entflow.RetryPolicy/Backoff/Retry — the fixed 3-parameter backoff surface CORE-09 specifies"
  - "entflow.StepOption: After/When/SelfWas/Condition/Transition, consolidated into stepoptions.go"
  - "entflow.WithSelfStatus[In, TX], entflow.Tx, entflow.TxOpener[T], entflow.RunInTx[In, T] — the full Phase 1 executor: topological ordering, entry-status snapshot, per-step panic recovery into *StepError, and RunInTx's commit/rollback/re-panic sugar"
affects: [01-04, phase-02-durability, phase-03-activities, phase-04-outbox, phase-05-codegen-injection, phase-06-codegen-validation]

actuals:
  tokens: 16460
  tasks: 3
  commits: 3

tech-stack:
  added: []
  patterns:
    - "One shared unexported mechanism (addDBStep) behind seven exported DB-step constructors — the constructors differ only in the recorded `constructor` string, never in behavior, so D-12's 'sugar over one mechanism, not seven' claim is enforced by the code shape, not just documentation."
    - "Package-level generic function constructors that need a fourth or fifth type parameter beyond In (Activity's Ent/Out) follow the exact same *FlowOf[In]-first-argument shape Plan 01 established for UpdateSelf — no new pattern, proving the pattern scales."
    - "Declaration-time validation panics with an error VALUE (never a string) so a recovering context can errors.As/errors.Is it — applied uniformly to duplicate step names, illegal Transition claims on read-only constructors, Retry on a DB step, and Backoff's parameter validation."
    - "Type-erased FlowOption adapters (WithSelfStatus, mirroring Plan 02's WithCodec): a generic function captures a typed closure and stores an any-erased adapter on flowConfig, so a non-generic options struct can still compose with a generic constructor."
    - "Two structurally separate recover() sites in the executor: one per-step (runStep, protects user closure panics, converts to *StepError with debug.Stack()), one transaction-level (RunInTx, protects Commit/Rollback bookkeeping against a panic escaping Exec) — never collapsed into one defer."
    - "DFS-based topological sort with declaration index as the recursion order: visiting steps in original declaration order and appending each step to the result only after its dependencies finish is what makes declaration order the deterministic tie-break among independent steps, with no separate stable-sort pass needed."

key-files:
  created:
    - steps.go
    - steps_test.go
    - activity.go
    - activity_test.go
    - json.go
    - retry.go
    - stepoptions.go
    - internal/testdata/compilefail/activity_tx.go
  modified:
    - step.go
    - flow.go
    - exec.go
    - exec_test.go
    - internal/testdata/ent/schema/order_flows.go

key-decisions:
  - "Check's closure returns only an error; its type-erased adapter returns a nil result value alongside it so the executor's result-recording path stays uniform across all seven DB-step constructors (Claude's Discretion per 01-CONTEXT.md, recorded here as directed)."
  - "Emit's sole string argument (topic) doubles as the step's own name — the identifier After edges and Result[T] lookups key on — since Emit's published signature (`Emit[In any](f, topic string, opts ...StepOption)`) takes no separate name parameter."
  - "WithSelfStatus's reader is an explicit declared FlowOption, never inferred from a flow's *Self steps, per ARCHITECTURE.md's 'codegen must never infer facts by inspecting closure bodies' invariant — this is a derived mechanism decision; D-10 specifies SelfWas's semantics but not how the reader is supplied."
  - "Condition is a two-field data-only struct (Kind, Value — both strings, no enum type, no function field) rather than a tagged-union-with-interface shape, so Plan 04's metadata path can render it with a single reflect pass and no evaluation."
  - "topoOrder uses a DFS post-order traversal (not a separate stable-sort pass) so that visiting steps in declaration order and appending on finish is sufficient, by construction, to make declaration order the tie-break among independent steps."

patterns-established:
  - "addDBStep(f, name, constructor, run, opts) as the single mechanism behind Step/CreateSelf/UpdateSelf/Create/Update/Query/Check."
  - "requireStepName / requireUniqueStepName as shared, package-level declaration-time guards called from every step constructor (DB steps, Activity, Emit alike)."
  - "runStep's per-step recover boundary and RunInTx's transaction-level recover boundary as two permanently separate defer sites."

requirements-completed: [CORE-03, CORE-04, CORE-05, CORE-09]

coverage:
  - id: D1
    description: "All seven DB-step constructors (Step, CreateSelf, UpdateSelf, Create, Update, Query, Check) are declarable as package-level generic functions over one shared mechanism, with full type inference and no explicit type arguments at any call site"
    requirement: "CORE-03"
    verification:
      - kind: unit
        ref: "steps_test.go#TestDBStepAllSevenConstructors"
        status: pass
      - kind: unit
        ref: "steps_test.go#TestDuplicateStepNamePanics"
        status: pass
      - kind: unit
        ref: "steps_test.go#TestReadOnlyTransitionOnQueryPanics"
        status: pass
    human_judgment: false
  - id: D2
    description: "An Activity closure structurally cannot receive a transaction handle — a closure declaring one is a compile error, machine-checked by shelling out to `go build` against a fixture designed to fail"
    requirement: "CORE-04"
    verification:
      - kind: unit
        ref: "activity_test.go#TestActivityCompileFail"
        status: pass
    human_judgment: false
  - id: D3
    description: "Step ordering via After(...) resolves out-of-declaration-order dependencies into a topological execution order (declaration order as tie-break), and conditional execution via When(SelfWas(...)) skips or runs a step against the entry-time status snapshot"
    requirement: "CORE-05"
    verification:
      - kind: unit
        ref: "exec_test.go#TestAfterOrdersStepsByDependency"
        status: pass
      - kind: unit
        ref: "exec_test.go#TestWhenSelfWasRunsWhenMatched"
        status: pass
      - kind: unit
        ref: "exec_test.go#TestWhenSelfWasSkipsWhenMismatched"
        status: pass
      - kind: unit
        ref: "exec_test.go#TestSelfWasSnapshotIsEntryValue"
        status: pass
    human_judgment: false
  - id: D4
    description: "A retry policy is attachable to an Activity via Retry(Backoff(maxAttempts, initial, max)) and no other knobs; Retry on a DB step is rejected at declaration time"
    requirement: "CORE-09"
    verification:
      - kind: unit
        ref: "activity_test.go#TestActivityRecordsKindAndRetry"
        status: pass
      - kind: unit
        ref: "activity_test.go#TestRetryPolicyFixedSurface"
        status: pass
      - kind: unit
        ref: "activity_test.go#TestRetryOnDBStepPanics"
        status: pass
    human_judgment: false
  - id: D5
    description: "Exec refuses any flow containing an Activity or Emit step with an error wrapping ErrRequiresDurableRun, before executing any step's closure — the fixture CancelOrder flow (entflow.md §3.1 in full) demonstrates this end to end"
    verification:
      - kind: unit
        ref: "exec_test.go#TestRequiresDurableRunCancelOrderFlow"
        status: pass
      - kind: unit
        ref: "exec_test.go#TestRequiresDurableRunNoStepInvoked"
        status: pass
    human_judgment: false
  - id: D6
    description: "A panic inside any step closure, including Use[T]'s panic on a missing provider, is recovered at that step's boundary and returned as a *StepError whose cause is still errors.Is-reachable, with a non-empty stack"
    verification:
      - kind: unit
        ref: "exec_test.go#TestRecoverStepPanicYieldsStepError"
        status: pass
      - kind: unit
        ref: "exec_test.go#TestRecoverUsePanicSurfacesAsNotProvided"
        status: pass
    human_judgment: false
  - id: D7
    description: "Any step error aborts the flow with no partial commit: RunInTx rolls the transaction back and leaves the database unchanged, commits on success, and Exec itself never calls Commit or Rollback"
    verification:
      - kind: unit
        ref: "exec_test.go#TestRunInTxCommitsAndIsReadable"
        status: pass
      - kind: unit
        ref: "exec_test.go#TestRunInTxRollsBackOnStepError"
        status: pass
      - kind: unit
        ref: "exec_test.go#TestExecDoesNotCallCommitOrRollback"
        status: pass
      - kind: unit
        ref: "exec_test.go#TestRunInTxRollsBackAndRepanicsOnEscapedPanic"
        status: pass
      - kind: unit
        ref: "exec_test.go#TestRunInTxJoinsRollbackErrorWhenPanicAndRollbackBothFail"
        status: pass
    human_judgment: false
  - id: D8
    description: "A later step reads an earlier step's typed result through entflow.Result[T](ctx, \"step\") during a real execution"
    verification:
      - kind: unit
        ref: "exec_test.go#TestResultAccessibleAcrossSteps"
        status: pass
    human_judgment: false
  - id: D9
    description: "go build ./..., go vet ./..., and go test ./... -race all exit 0 with the compile-fail fixture present, and no new non-stdlib/non-ent dependency enters the non-test build graph"
    verification:
      - kind: unit
        ref: "go build ./... && go vet ./... && go test ./... -race"
        status: pass
      - kind: unit
        ref: "go list -deps github.com/smintz/entflow (stdlib + entgo.io/ent/schema only, unchanged from Plan 01/02)"
        status: pass
    human_judgment: false

duration: 34min
completed: 2026-08-08
status: complete
---

# Phase 1 Plan 3: Step Kinds and Executor Summary

**All seven DB-step constructors, declarable-but-inert Activity/Emit steps with a build-level proof that a transaction handle cannot reach an Activity closure, and a six-phase executor (topological order, entry-status snapshot, per-step panic recovery, RunInTx) — completing the Phase 1 builder API.**

## Performance

- **Duration:** ~34 min
- **Tasks:** 3
- **Files modified:** 13 (8 created, 5 modified)
- **Commits:** 3

## Accomplishments

- Implemented `addDBStep`, the single unexported mechanism behind all seven DB-step constructors (`Step`, `CreateSelf`, `UpdateSelf`, `Create`, `Update`, `Query`, `Check`), each a thin typed wrapper recording its own constructor name and delegating to shared adapters (`dbAdapter`, `checkAdapter`) that use the comma-ok type-assertion form throughout
- Moved `UpdateSelf` out of `flow.go` into `steps.go` alongside its six siblings; `flow.go` now holds only the `Flow` interface, `FlowOf[In]`, `New`, and `FlowsOf`
- Added declaration-time guards shared across every step kind: `requireStepName` (empty name), `requireUniqueStepName` (duplicate names across the whole flow, not just DB steps), and a read-only-transition check rejecting `Transition(...)` on `Query`/`Check`
- Implemented `Activity[In, Ent, Out]` and `Emit[In]`: declarable but not executable in Phase 1 (D-13) — their `run` adapters panic defensively if ever invoked directly, since `Exec` refuses any flow containing one before executing anything
- Proved CORE-04 is machine-checked, not asserted in prose: `internal/testdata/compilefail/activity_tx.go` declares an Activity closure with a transaction parameter, and `TestActivityCompileFail` shells `go build` against it, requiring a non-zero exit whose combined output contains both `Activity` and `does not match`
- Implemented `JSON[Out]`/`NewJSON` (transparent `MarshalJSON`/`UnmarshalJSON` delegation — no wrapper envelope on the wire) and `Attempt.IdempotencyKey()` (`runID:stepName:attempt` per ACT-02), both pure derivations correctly unused by any Phase 1 executor path
- Implemented `RetryPolicy`/`Backoff`/`Retry`, validated to exactly 3 fields (`MaxAttempts`, `Initial`, `Max`) and rejecting `Retry` on a DB step at declaration time
- Extended the fixture `CancelOrder` flow with the `refund` Activity (`When(SelfWas("paid"))`, `Retry(Backoff(5, time.Second, time.Minute))`) and the `order.cancelled` `Emit(After("cancel"))`, matching entflow.md §3.1's worked example in full
- Consolidated `StepOption`, `Transition`, `After`, `When`, `SelfWas`, and the data-only `Condition` type into `stepoptions.go`
- Added `WithSelfStatus[In, TX]` (an explicit, declared `FlowOption` mirroring `WithCodec`'s type-erasure pattern), `Tx`, `TxOpener[T]`, and rewrote `Exec` into six ordered phases: resolve `After` edges into a topological order (cycle/unknown-step errors before touching the transaction), refuse durable-only flows, take the entry-time `SelfWas` snapshot once, derive a fresh per-execution result store, run each step behind its own `recover()` boundary converting panics into `*StepError` with `debug.Stack()`, and record results for a later step's `Result[T]`
- Added `RunInTx[In, T Tx]`: opens a transaction, calls `Exec`, commits on success, rolls back on a step error, and on a panic escaping `Exec` rolls back and re-panics — joining a rollback failure into the re-panicked error rather than discarding either
- Verified `exec.go` contains exactly two `recover()` call sites (per-step in `runStep`, transaction-level in `RunInTx`) — structurally separate, per D-16's requirement that they protect different things

## Task Commits

Each task was committed atomically:

1. **Task 1: The full DB-step constructor set over one internal representation** — `e7c34fa` (feat)
2. **Task 2: Activity and Emit declared but not executable, plus the compile-error proof** — `6e584c6` (feat)
3. **Task 3: Ordering, conditions, and the executor — topology, snapshot, recovery, RunInTx** — `850c409` (feat)

**Plan metadata:** pending (this commit)

## Files Created/Modified

- `steps.go` — `addDBStep`, `requireStepName`, `requireUniqueStepName`, `dbAdapter`, `checkAdapter`, and the seven exported DB-step constructors
- `steps_test.go` — declaration-data coverage for all seven constructors, tx-type-mismatch error formatting, duplicate-name and read-only-transition panics; also hosts `requirePanicsWithError`, the shared declaration-time-panic assertion helper this package's other `_test.go` files reuse
- `activity.go` — `Activity[In, Ent, Out]`, `Emit[In]`
- `activity_test.go` — Activity/Emit declaration-data coverage, `RetryPolicy`'s fixed field count, `Retry`-on-DB-step rejection, and `TestActivityCompileFail`
- `json.go` — `JSON[Out]`, `NewJSON`, `Attempt`, `Attempt.IdempotencyKey()`
- `retry.go` — `RetryPolicy`, `Backoff`, `Retry`
- `stepoptions.go` — `StepOption`, `After`, `When`, `Transition`, `Condition`, `SelfWas`
- `internal/testdata/compilefail/activity_tx.go` — the CORE-04 compile-fail fixture (excluded from `go build ./...`'s wildcard match by its `testdata` path segment)
- `step.go` — `step` struct gains `conditions []Condition` and `retry *RetryPolicy`; `StepOption`/`Transition` moved out to `stepoptions.go`
- `flow.go` — `flowConfig`/`FlowOf[In]` gain the type-erased `selfStatus`/`selfStatusReader` fields `WithSelfStatus` installs
- `exec.go` — full six-phase `Exec`, `topoOrder`, `runStep`, `evalConditions`, `needsSelfStatus`, `Tx`, `TxOpener[T]`, `WithSelfStatus`, `RunInTx`
- `exec_test.go` — full executor coverage: durable-run refusal, topological ordering (including cycle/unknown-dependency errors), `SelfWas` snapshot semantics, panic recovery (arbitrary panic and `Use[T]`'s `ErrNotProvided`), cross-step `Result[T]`, and `RunInTx` commit/rollback/re-panic/joined-rollback-error paths
- `internal/testdata/ent/schema/order_flows.go` — `CancelOrder` extended with the `refund` Activity and `order.cancelled` Emit; new `RefundResult` struct

## Decisions Made

See `key-decisions` in the frontmatter for the four decisions recorded during execution (Check's nil-result adapter shape, Emit's topic-as-name, `WithSelfStatus` as an explicit declared mechanism, `Condition`'s two-field data-only shape, and the DFS-based topological sort). All were within the plan's own "Claude's Discretion" latitude or were direct, undisputed implementations of the plan's `<interfaces>` block.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Task 2's own fixture and behavior spec required Task 3's stepoptions.go ahead of schedule**

- **Found during:** Task 2, while implementing the fixture `CancelOrder` flow extension
- **Issue:** The plan's own Task 2 action text requires `Activity(f, "refund", closure, When(SelfWas("paid")), Retry(Backoff(5, time.Second, time.Minute)))` and `Emit(f, "order.cancelled", After("cancel"))` to compile as part of Task 2 — but `When`, `SelfWas`, `Condition`, and `After` are all Task 3's `stepoptions.go` additions per the plan's own file-scoping. Task 2's `<files>` list does not include `stepoptions.go`, `step.go`, or `exec.go`, yet building the fixture flow and keeping `go build ./...`/`go vet ./...`/`go test ./... -race` green after Task 2's own commit (as its acceptance criteria require) is impossible without a minimal condition/dependency system and a `conditions`/`retry` field on `step` already existing.
- **Fix:** Added a minimal `stepoptions.go` (`Condition`, `SelfWas`, `When`, `After`) and `retry`/`conditions` fields on `step` as part of Task 2's commit — the smallest slice of Task 3's eventual `stepoptions.go` needed for Task 2's own fixture and tests to compile and pass. Also pulled forward a minimal durable-run refusal check into `exec.go` (three lines: refuse before executing if any step is `KindActivity`/`KindEmit`) so that extending the fixture flow with Activity/Emit didn't crash Plan 01's pre-existing `TestCancelOrderFlow` (renamed to `TestRequiresDurableRunCancelOrderFlow` to match the new, intended behavior) via an unrecovered panic from the still-tracer-shaped `Exec`. Task 3 then completed `stepoptions.go` (moving `StepOption`/`Transition` in from `step.go`) and replaced the minimal refusal check with the full six-phase executor.
- **Files modified:** `stepoptions.go` (created early), `step.go`, `exec.go`, `exec_test.go` — all subsequently extended, not reverted, by Task 3.
- **Verification:** `go build ./...`, `go vet ./...`, and `go test ./... -race` all exit 0 at the end of every task's commit, individually verified before each commit.
- **Committed in:** `6e584c6` (Task 2 commit, minimal slice); extended in `850c409` (Task 3 commit, completed).

---

**Total deviations:** 1 auto-fixed (1 blocking — Rule 3, a forward dependency inherent in the plan's own task decomposition, not a bug in the plan's design intent)
**Impact on plan:** No scope creep beyond what Task 2's own acceptance criteria already required to stay green. No behavior was implemented differently than the plan specified — only the *timing* of when a small slice of Task 3's file landed was pulled forward, and Task 3's commit still fully completes and matches its own file list and action text.

## Issues Encountered

- The acceptance-criteria grep for `Activity`'s exact published signature required the function declaration on a single source line; my first draft wrapped the parameter list across multiple lines for readability. `gofmt` does not re-wrap based on line length, so reformatting to one line and re-running `gofmt -w` was sufficient — no functional change.
- `internal/testdata/compilefail/activity_tx.go`'s actual `go build` failure message needed to be captured empirically (Go's generic-argument-mismatch phrasing is not guaranteed a priori) before writing `TestActivityCompileFail`'s assertions; confirmed the message contains both `Activity` (via the qualified `entflow.Activity` call site) and `does not match` (Go's own generic-inference-failure phrasing).

## User Setup Required

None — no external service configuration required.

## Next Phase Readiness

- The complete Phase 1 builder API is now in place: all seven DB-step constructors, `Activity`/`Emit` (declarable-only), retry policies, step ordering/conditions, and the full DB-only executor with `RunInTx` sugar.
- `Exec`'s six-phase structure and its exactly-two-`recover()`-sites invariant are the exact seam Phase 2's worker will call into and extend — Phase 2 is expected to swap the per-execution in-memory result store for the persisted run row without changing `Result[T]`'s signature, per D-11.
- Activity and Emit's builder data (retry policy, emit topic, conditions) is fully populated and ready for Plan 04's `Describe()`/`meta` package to render without evaluating any closure — no changes needed to this plan's data shapes.
- Phase 3 (Activities) and Phase 4 (Emits/outbox) can replace this plan's defensive panic-if-invoked `run` adapters with real three-beat-protocol execution without touching any declaration-time code in `activity.go`.
- No blockers for Plan 04.

---
*Phase: 01-runtime-core*
*Completed: 2026-08-08*

## Self-Check: PASSED

All created/modified files verified present on disk (steps.go, steps_test.go, activity.go, activity_test.go, json.go, retry.go, stepoptions.go, internal/testdata/compilefail/activity_tx.go, step.go, flow.go, exec.go, exec_test.go, internal/testdata/ent/schema/order_flows.go, this SUMMARY.md). All three task commits (`e7c34fa`, `6e584c6`, `850c409`) verified present in `git log --oneline --all`. `go build ./...`, `go vet ./...`, and `go test ./... -race` all exit 0 on the final tree.
