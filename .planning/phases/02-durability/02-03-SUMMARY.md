---
phase: 02-durability
plan: 03
subsystem: database
tags: [postgres, testcontainers, pgx, worker, skip-locked, concurrency, conformance-suite]

# Dependency graph
requires:
  - phase: 02-durability
    provides: "plan 02-02's ClaimStrategy seam, the claim-execute-advance transaction (worker/dbstep.go), the single-goroutine Worker.Run poll loop, and the ratified D-35 dialect matrix (docs/dialects.md) this plan hardens and certifies"
provides:
  - "internal/testdata/pgtest — a testcontainers-backed real-Postgres ent client: Start/StartN (one container per package via sync.Once, a fresh schema per test via the pgx search_path startup parameter), DockerAvailable, RequireCrashSim, SkipUnlessDockerAvailable (D-54)"
  - "worker.SkipLockedStrategy, certified: proven against real Postgres by worker/claim_test.go (lowest-ID ordering, the retry_after boundary on the database's own clock, every terminal state structurally unclaimable) and by the D-38 conformance suite"
  - "Worker.Run as a pool of Options.Concurrency independent claim goroutines (D-31), each with its own recover boundary (claimOnceRecovered) separate from runStep's per-closure recover"
  - "worker/conformance_test.go — the D-38 claim-strategy conformance suite: no-double-claim and rolled-back-claim-stays-claimable, proven on Postgres and SQLite, MySQL an explicit named skip"
affects: [02-04, 02-05, 02-06, 02-07, 02-08]

# Actuals (#2632)
actuals:
  tokens: 12400
  tasks: 3
  commits: 3

tech-stack:
  added:
    - "github.com/testcontainers/testcontainers-go v0.44.0 (test-only, confined to internal/testdata/pgtest and _test.go files)"
    - "github.com/testcontainers/testcontainers-go/modules/postgres v0.44.0 (same tag, same repo)"
    - "github.com/jackc/pgx/v5 v5.10.0 (stdlib mode, registers the \"pgx\" database/sql driver name)"
  patterns:
    - "pgtest.Start/StartN: one Postgres container per test package (sync.Once), a fresh schema per test via pgx's search_path DSN startup parameter — every new pooled connection sends it at connect time, so sql/execquery's raw QueryContext claim query (which never sees an explicit schema qualifier) resolves against the right isolated schema"
    - "DockerAvailable is a custom docker/moby client Ping probe, never testcontainers.SkipIfProviderIsNotHealthy (open panic-instead-of-skip bug, testcontainers-go#2859) — SkipUnlessDockerAvailable is the one D-54 skip-vs-ENTFLOW_REQUIRE_CRASHSIM-fail gate every Postgres-backed test in the module calls"
    - "claimOnceRecovered: a goroutine-iteration-scoped recover, mirroring RunInTx's transaction-level recover but for worker bookkeeping itself, deliberately separate from runStep's per-closure recover — a step author's panic is already a *StepError long before this would ever fire"
    - "Worker.Run: sync.WaitGroup-bounded pool of N independent pollLoop goroutines, nothing shared but the engine/stores/database; no goroutine identity, no run-ID-keyed in-memory structure"
    - "The D-38 conformance suite advances a successfully claimed run to a terminal state within the SAME transaction before commit — proving 'no two claimers hold a row concurrently', not the unrelated (and not guaranteed by the raw claim statement alone) 'a row never gets re-selected across separate later transactions', which is Advance's job in production"

key-files:
  created:
    - internal/testdata/pgtest/pgtest.go
    - internal/testdata/pgtest/docker.go
    - worker/claim_test.go
    - worker/conformance_test.go
  modified:
    - worker/claim.go
    - worker/worker.go
    - worker/worker_test.go
    - go.mod
    - go.sum

key-decisions:
  - "D-38 ratified exactly as CONTEXT.md/RESEARCH.md specify: one table-driven suite, Postgres and SQLite both execute and pass, MySQL an explicit named skip stating it is compatible-but-uncertified rather than a silently absent row."
  - "SQLite's conformance client is deliberately NOT internal/testdata/entclient.New's DSN (mode=memory&cache=shared): shared-cache in-memory SQLite enforces its own table-level locking that surfaces as SQLITE_LOCKED on writer contention, which modernc.org/sqlite's _busy_timeout busy-handler retry does not reliably cover. worker/conformance_test.go opens its own file-backed (t.TempDir()) SQLite client with _txlock=immediate AND _busy_timeout so concurrent BEGIN IMMEDIATE transactions block-and-retry instead of erroring — an SQLite-specific finding this plan surfaces for any later real-concurrency SQLite test to reuse."
  - "pgtest.StartN(t, n) added beyond the plan's literal Start(t) surface (Claude's discretion, RESEARCH.md's own scoping note) to give TestTwoWorkers N independent connection pools against the SAME schema — the shape a real multi-worker-process test needs, which a fresh-schema-per-client Start would defeat."
  - "worker.SkipLockedStrategy's own SQL (SELECT ... FOR UPDATE SKIP LOCKED, $-placeholders, database-clock now()) was already written correctly in plan 02-02's tracer expansion — this plan's Task 1 is exclusively the real-Postgres proof (pgtest + claim_test.go), not a rewrite; claim.go's doc comment updated to say 'certified', not 'plan 02-03 hardens'."

requirements-completed: [DUR-04]

coverage:
  - id: D1
    description: "The Postgres claim statement (SELECT ... FOR UPDATE SKIP LOCKED, database-clock retry_after comparison) runs through sql/execquery on the same transaction the run is then hydrated and mutated on, proven against a real containerized Postgres"
    requirement: DUR-04
    verification:
      - kind: integration
        ref: "worker/claim_test.go#TestClaimSkipLockedClaimsLowestID"
        status: pass
      - kind: integration
        ref: "worker/claim_test.go#TestClaimRetryAfterBoundary"
        status: pass
    human_judgment: false
  - id: D2
    description: "Every terminal state (done, cancelled, every failed:<step>) is structurally unclaimable — absent from the claim predicate's state list — so cancellation needs no separate stop signal"
    requirement: DUR-04
    verification:
      - kind: integration
        ref: "worker/claim_test.go#TestClaimTerminalStatesUnclaimable"
        status: pass
    human_judgment: false
  - id: D3
    description: "N concurrent claim transactions against a shared table never both claim the same run row; the total number of distinct runs claimed equals the number of runnable runs"
    requirement: DUR-04
    verification:
      - kind: integration
        ref: "worker/worker_test.go#TestWorkerConcurrency"
        status: pass
      - kind: integration
        ref: "worker/worker_test.go#TestTwoWorkers"
        status: pass
      - kind: integration
        ref: "worker/conformance_test.go#TestClaimStrategyConformance/postgres/no_double_claim"
        status: pass
      - kind: integration
        ref: "worker/conformance_test.go#TestClaimStrategyConformance/sqlite/no_double_claim"
        status: pass
    human_judgment: false
  - id: D4
    description: "A claim transaction rolled back rather than committed leaves the row claimable by the next claimer, on both certified dialects"
    requirement: DUR-04
    verification:
      - kind: integration
        ref: "worker/conformance_test.go#TestClaimStrategyConformance/postgres/rolled_back_claim_stays_claimable"
        status: pass
      - kind: integration
        ref: "worker/conformance_test.go#TestClaimStrategyConformance/sqlite/rolled_back_claim_stays_claimable"
        status: pass
    human_judgment: false
  - id: D5
    description: "Options.Concurrency runs N independent claim goroutines, each opening its own transaction and claiming at most one run — never a batch — and a panicking step closure leaves its run claimable without terminating Worker.Run"
    requirement: DUR-04
    verification:
      - kind: integration
        ref: "worker/worker_test.go#TestPanickingStep"
        status: pass
      - kind: other
        ref: "grep -c 'LIMIT 1' worker/claim.go"
        status: pass
    human_judgment: false
  - id: D6
    description: "When Docker is unavailable the Postgres-backed suites skip loudly naming Docker, and ENTFLOW_REQUIRE_CRASHSIM=1 turns that skip into a failure"
    requirement: DUR-04
    verification:
      - kind: other
        ref: "ENTFLOW_TEST_FORCE_DOCKER_UNAVAILABLE=1 go test ./worker/... -run TestClaimSkipLockedClaimsLowestID (skip); same + ENTFLOW_REQUIRE_CRASHSIM=1 (fail) — manually verified this session, both paths exercised"
        status: pass
    human_judgment: false

duration: ~30min
completed: 2026-08-15
status: complete
---

# Phase 02 Plan 03: Concurrent claiming, certified against real Postgres Summary

**`worker.SkipLockedStrategy`'s `FOR UPDATE SKIP LOCKED` claim statement — already written in plan 02-02 — is now proven against a real containerized Postgres by `internal/testdata/pgtest`, `Worker.Run` is a pool of N independent claim goroutines (D-31) instead of a single loop, and `worker/conformance_test.go`'s D-38 suite proves no-double-claim and rolled-back-claim-stays-claimable on both Postgres and SQLite with MySQL an honest, named, uncertified skip.**

## Performance

- **Duration:** ~30 min
- **Started:** 2026-08-15T12:15:00Z (approx.)
- **Completed:** 2026-08-15T12:30:41Z
- **Tasks:** 3 (all `type="auto"`)
- **Files modified:** 9 (4 created, 5 modified)

## Accomplishments

- `internal/testdata/pgtest` (`pgtest.go`, `docker.go`) gives every Postgres-backed test in the module a ready `*ent.Client`: one container per test package (`sync.Once`), a fresh Postgres schema per test isolated via the pgx `search_path` startup parameter (so `sql/execquery`'s raw `QueryContext` claim query, which never sees an explicit schema qualifier, still resolves correctly), and a custom `DockerAvailable` probe (a real docker/moby client `Ping`, never `testcontainers.SkipIfProviderIsNotHealthy`'s panic-prone skip helper) wired through `SkipUnlessDockerAvailable`'s D-54 skip-vs-`ENTFLOW_REQUIRE_CRASHSIM`-fail gate.
- `worker.SkipLockedStrategy`'s statement — written correctly in plan 02-02's tracer expansion but never run against a real Postgres — is now certified by `worker/claim_test.go`: lowest-ID claim ordering among several claimable runs, the `retry_after` boundary evaluated against the *database's* clock (a run in the future is not claimed; the same run is claimed once that time passes), and every terminal state (`done`, `cancelled`, a `failed:<step>` value) proven structurally unclaimable.
- `Worker.Run` turns `Options.Concurrency` into a pool of independent `pollLoop` goroutines (D-31: N independent claim transactions, never a batch — `LIMIT 1` stays the only claim), coordinated with a `sync.WaitGroup` so `Run` drains in-flight iterations and returns cleanly on context cancellation. Each iteration gets its own recover boundary (`claimOnceRecovered`), a second, deliberately separate site from `runStep`'s existing per-closure recover (exec.go) — a step author's panic is already converted to a `*StepError` long before it would ever reach this new boundary; this one exists so a bug in the worker's own claim-execute-advance bookkeeping can never crash the whole process.
- `TestWorkerConcurrency` (8 runs, `Concurrency: 4`, real Postgres, `-race`) and `TestTwoWorkers` (two independently constructed `*ent.Client`/`Worker` pairs against the same schema via new `pgtest.StartN`) both prove exactly one terminal transition and exactly one step-effect mutation per run — never more. `TestPanickingStep` proves a panicking step closure leaves its run claimable and never terminates `Worker.Run`.
- `worker/conformance_test.go`'s `TestClaimStrategyConformance` is the D-38 suite: one table-driven property pair (no-double-claim; rolled-back-claim-stays-claimable) run against every row of `docs/dialects.md`'s matrix — Postgres (via `pgtest`) and SQLite (via a dedicated file-backed, `_txlock=immediate` + `_busy_timeout` client) both execute and pass under `-race`; MySQL is an explicit, named `t.Skip` stating it shares the Postgres statement (D-36) but is uncertified by the release gate (D-35) — present, not silently absent. Each subtest logs its dialect and mutual-exclusion mechanism via `t.Log`.

## Task Commits

1. **Task 1: Real Postgres under test — testcontainers helper, a custom Docker probe, and the certified SKIP LOCKED claim** - `2458458` (feat)
2. **Task 2: N independent claim goroutines — concurrency without a coordination service** - `feac169` (feat)
3. **Task 3: The claim-strategy conformance suite across all three dialects** - `3159423` (test)

## Files Created/Modified

- `internal/testdata/pgtest/pgtest.go` - `Start`/`StartN`: testcontainers-backed real-Postgres `*ent.Client`, one container per package, fresh schema per test via pgx `search_path`
- `internal/testdata/pgtest/docker.go` - `DockerAvailable`, `RequireCrashSim`, `SkipUnlessDockerAvailable` — the D-54 skip-vs-fail gate
- `worker/claim_test.go` - real-Postgres proof of `SkipLockedStrategy`: lowest-ID ordering, retry_after boundary, terminal-state unclaimability
- `worker/conformance_test.go` - the D-38 conformance suite: no-double-claim, rolled-back-claim-stays-claimable, across Postgres/SQLite/MySQL(skipped)
- `worker/claim.go` - doc-comment update only: `SkipLockedStrategy` now says "certified", names what certifies it and that MySQL shares the statement but not the release gate
- `worker/worker.go` - `Worker.Run` rewritten as an N-goroutine pool (`pollLoop`, `claimOnceRecovered`), `sync.WaitGroup`-drained
- `worker/worker_test.go` - `TestWorkerConcurrency`, `TestTwoWorkers`, `TestPanickingStep` (all real-Postgres, `-race`)
- `go.mod` / `go.sum` - `testcontainers-go`, `testcontainers-go/modules/postgres`, `jackc/pgx/v5` pinned at RESEARCH.md's exact verified versions (no re-resolution needed — well within the 7-14 day validity window)

## Decisions Made

- **D-38 ratified exactly as specified**: Postgres and SQLite both execute the two properties for real; MySQL is an explicit, named skip rather than an absent row.
- **The SQLite conformance client intentionally does not reuse `entclient.New`'s DSN.** `entclient.New` uses `mode=memory&cache=shared`, whose table-level shared-cache locking surfaces as `SQLITE_LOCKED` on writer contention — a different error path than `SQLITE_BUSY`, which `modernc.org/sqlite`'s `_busy_timeout` busy-handler retry does not reliably cover. `worker/conformance_test.go` opens its own file-backed (`t.TempDir()`) client with both `_txlock=immediate` and `_busy_timeout` so concurrent `BEGIN IMMEDIATE` transactions block-and-retry instead of erroring. This is a real, reusable finding for any future SQLite-concurrency test in this repo.
- **`pgtest.StartN(t, n)`** was added beyond the plan's literal `Start(t)` surface — Claude's discretion, matching RESEARCH.md's own note that internal pgtest layout details are unspecified. `TestTwoWorkers` needs N independent connection pools against the *same* schema (mirroring N separate worker processes); `Start`'s one-fresh-schema-per-call design would put each client on its own isolated schema, defeating the test's whole point.
- **`SkipLockedStrategy`'s SQL was not rewritten** — it was already correct, written during plan 02-02's tracer expansion (visible in that plan's own `worker/claim.go`). Task 1's actual work was proving it against a real Postgres, not implementing it; the doc comment is updated to say "certified", removing the stale "plan 02-03 hardens and certifies this" forward-reference.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] The conformance suite's initial "no double claim" test design produced a genuine race**
- **Found during:** Task 3, first run of `TestClaimStrategyConformance`
- **Issue:** The test claimed a run and committed without ever changing its state — meaning a straggler transaction that opened after the winner committed would legitimately re-observe the row as still `pending` and reclaim it. On Postgres this showed up as an intermittent "run claimed more than once" failure (a real race in the *test's* timing, not the strategy); on SQLite the "ready-then-go" synchronization barrier (wait for every goroutine's `BeginTx` to complete before racing the claim) deadlocked, because SQLite's `BEGIN IMMEDIATE` itself blocks on the write lock, so not every goroutine could reach "ready" concurrently.
- **Fix:** (a) Advance the successfully claimed run to a terminal state within the same transaction before commit, so the property under test is genuinely "no two claimers hold a row concurrently" rather than the unrelated (and not actually guaranteed by the raw claim statement alone) "a row is never re-selected across separate later transactions." (b) Removed the "wait for everyone to have opened their transaction" barrier; goroutines now race from before `BeginTx`, letting each dialect's own mechanism (SKIP LOCKED's row lock; `BEGIN IMMEDIATE`'s whole-database write lock) do the actual serialization.
- **Files modified:** worker/conformance_test.go
- **Verification:** `go test ./worker/... -run TestClaimStrategyConformance -race -count=1` passed cleanly across 3 consecutive runs after the fix.
- **Committed in:** `3159423` (Task 3 commit)

**2. [Rule 1 - Bug] SQLite shared-cache in-memory DSN produced `SQLITE_LOCKED`, not a busy-timeout-retryable error**
- **Found during:** Task 3, same test run
- **Issue:** Reusing `internal/testdata/entclient.New`'s DSN (`mode=memory&cache=shared`) plus a `_busy_timeout` parameter still failed with `ent: starting a transaction: database table is locked (262)` under concurrent writers — shared-cache mode's table-level locking is a distinct error path (`SQLITE_LOCKED`) that `modernc.org/sqlite`'s busy-handler retry does not reliably cover, unlike ordinary file-lock contention (`SQLITE_BUSY`).
- **Fix:** `worker/conformance_test.go` opens its own file-backed SQLite database in `t.TempDir()` instead of an in-memory shared-cache one, with both `_txlock=immediate` and `_busy_timeout=5000` — ordinary whole-file locking, where `_busy_timeout`'s blocking-retry semantics are well-defined.
- **Files modified:** worker/conformance_test.go
- **Verification:** `TestClaimStrategyConformance/sqlite` passes consistently under `-race` across repeated runs.
- **Committed in:** `3159423` (Task 3 commit)

---

**Total deviations:** 2 auto-fixed (both Rule 1 — bugs found getting the D-38 conformance suite genuinely green, not stale-until-lucky). No scope creep; no architectural changes.

## Issues Encountered

- `go vet ./...` initially failed with a stale `go.sum` entry (`missing go.sum entry for github.com/google/go-cmp/cmp`) after `go get`ing the three new modules bumped `go-cmp` from v0.6.0 to v0.7.0 as a transitive dependency; `go mod tidy` resolved it. Not a deviation — routine dependency-graph hygiene after adding new test-only modules.

## User Setup Required

None - no external service configuration required. Docker is already available and pre-pulled (`postgres:16-alpine`, `testcontainers/ryuk:0.11.0`→`0.14.0` auto-updated by testcontainers-go itself) in this execution environment; a developer machine without Docker gets the loud, named skip `SkipUnlessDockerAvailable` produces, converted to a failure by `ENTFLOW_REQUIRE_CRASHSIM=1` in CI.

## Next Phase Readiness

- DUR-04's "multiple workers run concurrently with no coordination service" is now a passing, race-detector-clean test against a real Postgres, not a design claim.
- `internal/testdata/pgtest` (`Start`, `StartN`, `DockerAvailable`, `RequireCrashSim`, `SkipUnlessDockerAvailable`) is the real-Postgres test infrastructure every later Phase 2 plan (crash-simulation harness, plan 02-08) builds on directly — `StartN`'s multi-client-one-schema shape in particular is what a real crash-kill test against a shared database will need.
- `worker.SkipLockedStrategy` is certified; `docs/dialects.md`'s matrix claims are now backed by executable proof for both dialects it names as supported-with-tests (Postgres certified, SQLite dev/test-only).
- No blockers. The two SQLite-concurrency findings (shared-cache `SQLITE_LOCKED` vs. file-backed `SQLITE_BUSY`) are documented in `worker/conformance_test.go`'s own doc comments for the next person who reaches for concurrent SQLite testing in this repo.

---
*Phase: 02-durability*
*Completed: 2026-08-15*

## Self-Check: PASSED

All created/modified files verified present on disk (internal/testdata/pgtest/pgtest.go, internal/testdata/pgtest/docker.go, worker/claim_test.go, worker/conformance_test.go, worker/claim.go, worker/worker.go, worker/worker_test.go, go.mod, go.sum). All referenced commit hashes (2458458, feac169, 3159423) verified present in `git log --oneline --all`.
