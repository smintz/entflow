---
phase: 02-durability
plan: 02
subsystem: database
tags: [ent, worker, sqlite, claim-loop, sql-execquery, durable-run]

# Dependency graph
requires:
  - phase: 02-durability
    provides: "plan 02-01's sql/execquery-enabled fixture client, entflow.RunMixin, entflow.WithOwnerRef, entflow.Run/RunTable/RawQuerier, and the real migrated cancel_order_flow_runs table this plan's Start/Runner/RunStore/worker are built directly on"
provides:
  - "entflow.Engine / entflow.Start[In] — the boot-time registry and generic run-start entry point (D-25)"
  - "entflow.RunStore — the tx-erased port a hand-written (Phase 2) or generated (Phase 6) per-flow adapter implements"
  - "entflow.Runner / StepCall / StepOutcome — the non-generic per-step execution seam the worker calls into"
  - "the entflow/worker package: Worker, New, Run, Shutdown, ClaimOnce, Options, ClaimStrategy, SQLiteStrategy, SkipLockedStrategy, StrategyForDialect"
  - "internal/testdata/entflowfixture.RunStore — the hand-written adapter over CancelOrderFlowRun, Phase 6's template target"
  - "docs/dialects.md — the ratified D-35 dialect support matrix, closing STATE.md's open Phase 2 blocker"
affects: [02-03, 02-04, 02-05, 02-06, 02-07, 02-08]

# Actuals (#2632)
actuals:
  tokens: 20383
  tasks: 3
  commits: 2

tech-stack:
  added: []
  patterns:
    - "entflow.RunStore: every method takes the transaction as any (D-24), matching Exec(ctx, tx any, in In)'s erasure — no second erasure mechanism invented"
    - "entflow.Runner: the non-generic per-step seam a heterogeneous worker calls through, implemented on *FlowOf[In] for every In via StepOrder/RequiresDurableRun/EntrySelfStatus/OwnerRef/ExecStep"
    - "worker.ClaimStrategy: the one non-portable claim query isolated behind a dialect-selected interface (D-36), every value placeholder-bound (T-02-02)"
    - "claim-execute-advance in one transaction (D-30): worker/dbstep.go opens exactly one transaction per claim, executes exactly one DB step, writes the effect and the progress pointer together, and commits — no lease anywhere"

key-files:
  created:
    - runstore.go
    - runner.go
    - engine.go
    - worker/claim.go
    - worker/dbstep.go
    - worker/worker.go
    - worker/options.go
    - worker/worker_test.go
    - internal/testdata/entflowfixture/runstore.go
    - durablerun_test.go
    - docs/dialects.md
    - README.md
  modified:
    - exec.go
    - errors.go
    - internal/testdata/entclient/client.go

key-decisions:
  - "D-30 ratified exactly as CONTEXT.md's §'Claim Loop' specifies: 'ratify-single-transaction' auto-selected under auto mode (checkpoint gate=blocking, not blocking-human). The claim transaction IS the step transaction — proven by TestClaimRollbackLeavesRunClaimable's paired absence claim, not just a terminal-state check."
  - "D-25 deviation recorded on Start's own doc comment: DUR-01's literal flow.Start(ctx, in) is unreachable in Phase 2 without an ambient global D-15 already rejected — Start is a package-level generic function, mirroring RunInTx[In,T]'s shape, because Go methods cannot declare their own type parameters. Phase 6's codegen restores the design-doc call shape as a generated typed facade."
  - "Runner.StepOrder() returns DB-kind steps only, never the full step graph. CancelOrder legitimately declares an Activity (refund) and an Emit (order.cancelled) alongside its one DB step (cancel) — durable execution in Phase 2 walks DB steps only, so 'next step after current_step' never lands on a step that must refuse to run. EntrySelfStatus is scoped the same way (a SelfWas condition on refund must not force a WithSelfStatus requirement Phase 2 never needed)."
  - "The 'worker is a pool, not a singleton' identity decision (the assumption-delta checkpoint) is recorded as its own docs/dialects.md section and as Worker's doc comment: the unit of ownership is the run claim, not the worker — no worker-ID column, no in-memory run registry, no code path keys behavior on worker identity."
  - "worker/options.go's TracerProvider field is typed any, not the real go.opentelemetry.io/otel/trace.TracerProvider — D-57's dependency exception to the META-02 test hasn't landed yet, this plan adds no new module (T-02-SC), and TestNoTransportDeps is the standing gate. Narrows to the real type with no call-site change once that plan lands."

requirements-completed: [DUR-01, DUR-03, DUR-06, DUR-08]

coverage:
  - id: D1
    description: "entflow.Start persists exactly one run row in the pending state with codec-serialized input and an edge to the owning aggregate, and returns a *entflow.Run whose ID matches the persisted row"
    requirement: DUR-01
    verification:
      - kind: unit
        ref: "durablerun_test.go#TestDurableRunReachesDone"
        status: pass
    human_judgment: false
  - id: D2
    description: "A worker claim opens one transaction, claims a run, executes exactly one DB step's closure, writes the step's effect and the run's progress pointer, and commits — all on the same transaction handle"
    requirement: DUR-03
    verification:
      - kind: unit
        ref: "durablerun_test.go#TestDurableRunReachesDone"
        status: pass
      - kind: unit
        ref: "durablerun_test.go#TestClaimRollbackLeavesRunClaimable"
        status: pass
    human_judgment: false
  - id: D3
    description: "A run advances at most one step per claim; a two-DB-step flow needs two successful claims, and the run is claimable between them — no lease anywhere in the code"
    requirement: DUR-06
    verification:
      - kind: unit
        ref: "durablerun_test.go#TestRunAdvancesOneStepPerClaim"
        status: pass
    human_judgment: false
  - id: D4
    description: "A run whose every remaining step is skipped by a false condition advances to done without any step closure invoked and with no results recorded"
    requirement: DUR-06
    verification:
      - kind: unit
        ref: "durablerun_test.go#TestAllStepsSkippedReachesDone"
        status: pass
    human_judgment: false
  - id: D5
    description: "worker.New refuses an unsupported dialect and a multi-worker SQLite configuration at construction, naming both the offending value and docs/dialects.md, with no database round trip"
    requirement: DUR-08
    verification:
      - kind: unit
        ref: "worker/worker_test.go#TestNewRefusesUnsupportedDialect"
        status: pass
      - kind: unit
        ref: "worker/worker_test.go#TestNewRefusesSQLiteConcurrency"
        status: pass
      - kind: unit
        ref: "worker/worker_test.go#TestNewValidatesBeforeAnyDatabaseRoundTrip"
        status: pass
    human_judgment: false
  - id: D6
    description: "docs/dialects.md ratifies the D-35 matrix (Postgres first-class/certified, MySQL compatible/uncertified with the missing partial index named concretely, SQLite dev/test-only single-worker, everything else refused) and is linked from README.md"
    requirement: DUR-08
    verification:
      - kind: other
        ref: "grep -ci 'partial index' docs/dialects.md; grep -c 'dialects.md' README.md"
        status: pass
    human_judgment: false

duration: ~55min
completed: 2026-08-15
status: complete
---

# Phase 02 Plan 02: End-to-end durable run tracer Summary

**A run started with `entflow.Start` is claimed by a `worker.Worker` in one transaction, advances exactly one DB step per claim through the new `entflow.Runner`/`RunStore` seams, and reaches `done` with the owning `Order` mutated — proven by a rollback test that asserts the paired absence of both the step's effect and the progress pointer, not just a terminal-state check — and `docs/dialects.md` ratifies the D-35 dialect matrix, closing STATE.md's open Phase 2 blocker.**

## Performance

- **Duration:** ~55 min
- **Started:** 2026-08-15T11:44:00Z (approx.)
- **Completed:** 2026-08-15T12:08:36Z
- **Tasks:** 1 checkpoint:decision (auto-resolved) + 2 (tracer, auto)
- **Files modified:** 15 (12 created, 3 modified)

## Accomplishments

- `entflow.Start[In](ctx, eng, f, in) (*entflow.Run, error)` persists a run row in the pending state through a registered `entflow.RunStore`, following `RunInTx`'s open/commit/rollback/panic discipline — proven end to end by `TestDurableRunReachesDone`.
- `worker/dbstep.go`'s `claimOnce` is D-30's whole safety argument made real: one transaction opens, `worker.ClaimStrategy` claims a row (`SQLiteStrategy` here — a plain `SELECT` correct only because the transaction is `BEGIN IMMEDIATE`, `SkipLockedStrategy`/`StrategyForDialect` cover Postgres/MySQL for plan 02-03 to harden), exactly one DB step's closure runs through `entflow.Runner.ExecStep`, the effect and the progress pointer write together via `RunStore.Advance`, and the transaction commits.
- `TestClaimRollbackLeavesRunClaimable` drives the claim-execute sequence by hand through the same exported primitives (`RunStore`, `Runner`, `worker.ClaimStrategy`) and rolls back instead of advancing/committing — asserting BOTH the `Order` mutation and the run row's state/current_step survived unchanged, the paired absence claim D-55 will later generalize into the crash harness.
- `TestRunAdvancesOneStepPerClaim` (a local two-DB-step fixture flow sharing the `CancelOrderFlowRun` table) proves the run needs exactly two successful claims and stays claimable between them; `TestAllStepsSkippedReachesDone` (a local flow whose steps are all gated `When(SelfWas("never-this-status"))`) proves a run whose every remaining step is skipped reaches `done` with zero closures invoked and an empty results map.
- `worker.New` validates eagerly (D-37): `ErrUnsupportedDialect` names the dialect and `docs/dialects.md`; `ErrSQLiteConcurrency` refuses `Concurrency > 1` on SQLite rather than silently serializing — both proven to make no database round trip.
- `docs/dialects.md` ratifies D-35 as a real document, names MySQL's missing partial index concretely, and records the "worker is a pool, not a singleton" identity decision; linked from a new `README.md`.

## Task Commits

1. **Task 1: End-to-end durable run tracer** - `3a9dd82` (feat)
2. **Task 2: Ratify the dialect matrix** - `7174601` (feat)

**Checkpoint:** D-30 ratification — auto-selected "ratify-single-transaction" (auto mode active, gate=blocking not blocking-human per checkpoint protocol).

_Note: no separate plan-metadata commit yet — STATE.md/ROADMAP.md/REQUIREMENTS.md updates land in the final commit below._

## Files Created/Modified

- `runstore.go` - `entflow.RunStore` port, `InsertRun`/`ClaimQuery`/`Advance`/`Fail`
- `runner.go` - `entflow.Runner`, `StepCall`/`StepOutcome`, implemented on `*FlowOf[In]`
- `engine.go` - `entflow.Engine`, `NewEngine`/`Register`/`RunnerFor`/`StoreFor`/`Flows`/`Notify`/`Nudges`/`WithDI`, `entflow.Start[In]`
- `exec.go` - extracted `runOneStep`, the shared per-step primitive `Exec`'s loop and `Runner.ExecStep` both call
- `errors.go` - `ErrUnknownFlow`, `ErrRunNotClaimable`, `ErrRunNotAdvanced`
- `worker/claim.go` - `ClaimStrategy`, `SQLiteStrategy`, `SkipLockedStrategy`, `StrategyForDialect`, `ErrUnsupportedDialect`
- `worker/dbstep.go` - `claimOnce`: claim, execute one step, advance, commit — one transaction (D-30)
- `worker/worker.go` - `Worker`, `New`, `Run`, `Shutdown`, `ClaimOnce`, `ErrSQLiteConcurrency`
- `worker/options.go` - full Phase 2 `Options` field surface, `Default*` constants
- `worker/worker_test.go` - dialect/concurrency construction-time validation coverage
- `internal/testdata/entflowfixture/runstore.go` - hand-written `RunStore` adapter over `CancelOrderFlowRun`
- `internal/testdata/entclient/client.go` - `_txlock=immediate` added to the fixture DSN
- `durablerun_test.go` - the tracer's four proof tests
- `docs/dialects.md` - the ratified D-35 dialect support matrix
- `README.md` - new; project overview, links `docs/dialects.md`

## Decisions Made

- **D-30 ratified exactly as specified** (auto-selected under auto mode, checkpoint `gate="blocking"` not `blocking-human`). The claim transaction is the step transaction; no lease exists anywhere in the code.
- **D-25's DUR-01 deviation is documented on `Start`'s own doc comment**, not just in this summary — `go doc github.com/smintz/entflow Start` renders the full explanation.
- **`Runner.StepOrder`/`EntrySelfStatus` scope to DB-kind steps only**, discovered as a real bug while getting `TestDurableRunReachesDone` green (see Deviations below) — this is now the durable-execution contract Phase 6's generated runner must also honor.
- **The worker-is-a-pool identity decision** (the assumption-delta checkpoint CONTEXT.md flagged) is answered in `docs/dialects.md` and on `Worker`'s doc comment: the run claim, not the worker, is the unit of ownership.
- **`TracerProvider` stays `any` in this plan**, deliberately not the real `go.opentelemetry.io/otel/trace.TracerProvider` — see Deviations.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `Runner.EntrySelfStatus` demanded a `WithSelfStatus` reader for a condition on a step that will never execute in Phase 2**
- **Found during:** Task 1, `TestDurableRunReachesDone` verification
- **Issue:** `needsSelfStatus(f.steps)` (shared with `Exec`) scans every step regardless of kind. CancelOrder's `refund` Activity step legitimately declares `When(SelfWas("paid"))`, so `EntrySelfStatus` demanded a `WithSelfStatus` reader CancelOrder never declares (correctly — Phase 1 never needed one, since `Exec` refuses the whole flow via `ErrRequiresDurableRun` before ever reaching the self-status check). The durable path has no such early-refusal step, so the check fired for real and the worker's poll loop looped forever failing every claim.
- **Fix:** Scoped `EntrySelfStatus` to DB-kind steps only before calling `needsSelfStatus`, with a doc comment explaining why this differs from `Exec`'s identical-looking check.
- **Files modified:** runner.go
- **Verification:** `TestDurableRunReachesDone` passes; full suite green.
- **Committed in:** 3a9dd82 (Task 1 commit)

**2. [Rule 1 - Bug] `worker.ClaimStrategy.Claim` returns `int64`; `RunStore.Load`'s ID assertion only accepted `int`**
- **Found during:** Task 1, `TestRunAdvancesOneStepPerClaim`/`TestAllStepsSkippedReachesDone` verification
- **Issue:** `strategy.Claim` returns a universal SQL-scan `int64` (the worker never knows the application's generated ID type), but `worker/dbstep.go` passed that value straight to `RunStore.Load`, and the fixture adapter's ID assertion only accepted `int` (the generated ent ID type) — every claim after the first failed with a type-mismatch error.
- **Fix:** `entflowfixture.asID` now accepts both `int` and `int64`, converting the latter — the conversion belongs in the hand-written (Phase 6: generated) adapter, per D-24, not in the dialect-agnostic worker.
- **Files modified:** internal/testdata/entflowfixture/runstore.go
- **Verification:** all four tracer tests pass.
- **Committed in:** 3a9dd82 (Task 1 commit)

**3. [Rule 3 - Blocking] `worker/options.go`'s `TracerProvider` field typed `any`, not the real OTel type**
- **Found during:** Task 2, while wiring `Options`'s full field surface
- **Issue:** The plan's action text says `TracerProvider` should be "typed as the OpenTelemetry tracer-provider interface," but the plan's own threat model (T-02-SC) states this plan adds no new module and that `TestNoTransportDeps` remains the standing gate. `go.opentelemetry.io/otel/trace` is not yet in `go.mod`, and D-57's narrow dependency exception to the META-02 test hasn't landed (it amends a Phase 1 decision and is explicitly flagged in CONTEXT.md as "the one decision in this phase most worth a second look"). Adding the real import here would both violate "no new modules" and fail `TestNoTransportDeps`.
- **Fix:** Declared `TracerProvider any` with a doc comment explaining the narrowing will happen with no call-site change once D-57's exception lands (a later plan's job — the field name and position are locked in now, which is what "declare every field up front" is for).
- **Files modified:** worker/options.go
- **Verification:** `TestNoTransportDeps` passes; `grep -c 'TracerProvider' worker/options.go` is 3 (satisfies the plan's own acceptance criterion, which only checks the field's presence).
- **Committed in:** 7174601 (Task 2 commit)

---

**Total deviations:** 3 auto-fixed (2 Rule 1 — bugs found getting the tracer green, 1 Rule 3 — blocking module-dependency conflict between the plan's action text and its own threat model). No scope creep; no architectural changes.

## Issues Encountered

- SQLite's shared-cache DSN (`entclient.New`) needed `_txlock=immediate` added: `worker.SQLiteStrategy`'s claim query is a bare `SELECT`, which takes no lock at all under SQLite's default deferred-transaction mode — two concurrent claimants could otherwise both observe the same row as claimable before either wrote anything. This DSN change is shared with every Phase 1 test using `entclient.New`; the full existing suite stayed green after it (single-goroutine tests never depended on deferred-mode timing).
- The two-DB-step and all-skipped-step fixture flows (`newMultiStepFlow`, `newAllSkippedFlow`) are declared locally in `durablerun_test.go` rather than as new hand-written ent schema files: `RunMixin`'s `state` enum only needs its four base values (pending/running/done/cancelled) for these tests, which are shared regardless of which flow produced a row, so a second unrelated flow can register against the same `entflowfixture.RunStore`/`CancelOrderFlowRun` table with no schema change. This kept the plan from needing a second codegen round-trip; a genuinely distinct run entity per flow remains Phase 6's generated norm.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- `entflow.RunStore`/`Runner`/`Engine`/`Start` and `worker.Worker`/`ClaimStrategy` are all proven end to end on SQLite; plan 02-03 hardens `SkipLockedStrategy` against a real Postgres via `testcontainers-go` and adds the D-38 claim-strategy conformance suite (concurrent claimers never double-claim) that this plan's `must_haves` deliberately marked `verification: backstop` rather than requiring here.
- `docs/dialects.md` closes STATE.md's "dialect support matrix is not yet a ratified project decision" blocker outright.
- `worker/options.go` declares `Flows`, `Context`, `RetryableError`, and `DrainTimeout` already, unused in this plan — later plans (02-06 spans, 02-07 lifecycle, DB-step retry) wire them without widening the struct.
- No blockers. `TracerProvider` staying `any` until D-57's dependency exception lands is documented, not deferred silently.

---
*Phase: 02-durability*
*Completed: 2026-08-15*

## Self-Check: PASSED

All created files verified present on disk (runstore.go, runner.go, engine.go, worker/claim.go, worker/dbstep.go, worker/worker.go, worker/options.go, worker/worker_test.go, internal/testdata/entflowfixture/runstore.go, durablerun_test.go, docs/dialects.md, README.md). All referenced commit hashes (3a9dd82, 7174601) verified present in `git log --oneline --all`.
