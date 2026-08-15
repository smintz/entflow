---
phase: 02-durability
plan: 07
subsystem: worker
tags: [worker-lifecycle, poll-loop, jitter, graceful-shutdown, deployment-topology, postgres]

# Dependency graph
requires:
  - phase: 02-durability
    provides: "plan 02-02's ClaimStrategy seam and Options.PollInterval/Jitter/DrainTimeout fields, plan 02-03's N-goroutine claim pool and internal/testdata/pgtest, plan 02-05's privacy-governed claim path and Options.Context viewer hook, plan 02-06's worker.Options.TracerProvider narrowing and span wiring"
provides:
  - "worker/loop.go — the jittered poll loop (Run/pollLoop/claimOnceRecovered/jitteredInterval), extracted from worker.go so the constructor file stays about construction and validation"
  - "worker.Worker.Shutdown — D-49's three-beat graceful drain: stop claiming, wait bounded by DrainTimeout/ctx, cancel past the deadline and report the in-flight count"
  - "internal/testdata/crashworker — the DUR-09 dedicated-worker worked example, and plan 02-08's tier-2 crash-simulation kill target"
  - "internal/testdata/pgtest.StartNWithDSN — StartN's DSN-exposing variant, needed by any test that must point a real OS subprocess at the same isolated Postgres schema"
  - "topology_test.go — proves a dedicated binary and an in-process worker claim from one Postgres schema with no coordination, and that the binary honors a termination signal within its drain window"
affects: [02-08]

# Actuals (#2632)
actuals:
  tokens: 15600
  tasks: 3
  commits: 3

tech-stack:
  added: []
  patterns:
    - "worker/loop.go vs worker/worker.go split: worker.go owns construction/validation (New, ClaimOnce, Shutdown's public entry point), loop.go owns the running loop (Run, pollLoop, claimOnceRecovered, jitteredInterval) — a file boundary that mirrors the plan's own Task 1/Task 2 split."
    - "Claim-lifecycle bookkeeping (beginClaim/endClaim, worker.go) is a mutex-guarded counter plus a sync.Cond, deliberately NOT a sync.WaitGroup: N independent claim goroutines starting and finishing claims while Shutdown waits for zero hits WaitGroup's documented 'Add racing Wait once the counter has touched zero' hazard. Caught live by -race during development (see Deviations) and fixed by switching primitives, not by adding synchronization around the WaitGroup."
    - "beginClaim gates every claim attempt atomically against Shutdown's stopping flag: once true, beginClaim returns false and claimOnceRecovered returns the same (false, nil) shape an empty poll returns — so pollLoop's own inner claiming loop stops on its own, with no separate stopCh check needed inside it."
    - "Run derives its own cancellable work context (workCtx) from the ctx it's given, so cancelling Run's own ctx propagates into every in-flight claim transaction's context immediately — literally the same code path a zero-drain Shutdown takes, not merely an equivalent one."
    - "internal/testdata/crashworker registers every flow entflow.FlowsOf(schema.Order{}) returns, rather than hand-listing CancelOrder — a flow plan 02-08 adds to order_flows.go is picked up with no edit to main.go."
    - "pgtest.StartN/Start now delegate to the new StartNWithDSN, which also returns the connection string those clients were opened against — needed the moment a test has to hand a real OS subprocess (not another goroutine in the same process) the same isolated schema."

key-files:
  created:
    - worker/loop.go
    - worker/loop_test.go
    - worker/shutdown_test.go
    - internal/testdata/crashworker/main.go
    - internal/testdata/crashworker/README.md
    - topology_test.go
  modified:
    - worker/worker.go
    - internal/testdata/pgtest/pgtest.go
    - README.md

key-decisions:
  - "Claim-lifecycle counting uses mu + inFlight int + sync.Cond (zeroCond), not a sync.WaitGroup — a real, -race-detected bug during development, not a stylistic choice. sync.WaitGroup forbids a positive-delta Add racing a Wait once the counter has touched zero, which is exactly the shape N independent claim goroutines (each Add/Done-ing per claim) racing against Shutdown's drain wait produces. A mutex + condition variable has no such restriction."
  - "stopCh (a channel, closed once) and the mu-guarded stopping bool serve DIFFERENT purposes and both stay: stopCh exists only to wake a pollLoop goroutine's select PROMPTLY even mid-wait (a long PollInterval otherwise delays Shutdown noticing); stopping/beginClaim is the atomic 'may a new claim start' decision. Collapsing them into one mechanism was tried first and is what produced the WaitGroup race — see Deviations."
  - "topology_test.go measures the cross-process 'no duplicated effect' claim via CancelOrder's own single step (the owning Order's status becomes cancelled) rather than a dedicated numeric counter — CancelOrder has no counter field, and adding one to the fixture schema is explicitly plan 02-08 Task 1's own decision (an integer column on Order) to make, not this plan's to preempt. Documented as a known scope note: this test proves the topology claims (one terminal transition per run, no coordination needed) by construction (D-30 + certified SKIP LOCKED, plan 02-03) rather than by a regression-proof counter; the counter-based mutation-tested version of this claim is 02-08's own mandate."
  - "internal/testdata/pgtest.StartN/Start now delegate to a new StartNWithDSN rather than duplicating schema-creation logic — Claude's discretion (Rule 2: missing critical functionality), since topology_test.go cannot share this test process's in-memory *sql.DB pool with a real OS subprocess and needs the raw connection string to point crashworker at the identical isolated schema. Every existing StartN/Start call site is unaffected."
  - "Shutdown's own doc comment answers the plan's required finding directly: graceful shutdown needed NOTHING crash-resume did not already provide — no drain table, no in-memory claimed-run registry, no handoff protocol. The only new mechanism is a signal to stop claiming and a deadline-bounded wait; 'rolled back' was already a safe, ordinary outcome (D-30) before Shutdown existed."

requirements-completed: [DUR-09, DUR-10]

coverage:
  - id: D1
    description: "The poll loop jitters every wait (not once at construction), samples verified against a configured band across 30 consecutive waits, and never memoizes the factor"
    requirement: DUR-09
    verification:
      - kind: unit
        ref: "worker/loop_test.go#TestPollJitterBandAcrossManyWaits"
        status: pass
      - kind: unit
        ref: "worker/loop_test.go#TestPollJitterZeroDisablesIt"
        status: pass
    human_judgment: false
  - id: D2
    description: "entflow.Start's automatic Engine.Notify wakes a co-located worker's poll loop materially sooner than the configured poll interval; discarding the nudge still completes the run via polling, proving polling is the correctness backstop"
    requirement: DUR-09
    verification:
      - kind: integration
        ref: "worker/loop_test.go#TestNudgeShortensWaitMaterially"
        status: pass
      - kind: integration
        ref: "worker/loop_test.go#TestPollingBackstopCompletesRunsWithNudgesDiscarded"
        status: pass
    human_judgment: false
  - id: D3
    description: "A goroutine that successfully claimed a run attempts its next claim without waiting a full poll interval, so a multi-run backlog drains in a small fraction of N*PollInterval"
    requirement: DUR-09
    verification:
      - kind: integration
        ref: "worker/loop_test.go#TestPollLoopClaimsWithoutFullIntervalWaitBetweenRuns"
        status: pass
    human_judgment: false
  - id: D4
    description: "Shutdown issued mid-step waits for the in-flight step's transaction to commit before returning; the effect is present afterward"
    requirement: DUR-10
    verification:
      - kind: integration
        ref: "worker/shutdown_test.go#TestShutdownWaitsForInFlightStepToCommit"
        status: pass
    human_judgment: false
  - id: D5
    description: "A drain deadline shorter than a deliberately slow step returns a non-nil error naming the in-flight count, the step's effect and the run's progress pointer are both absent (paired absence, D-49/D-30), and a subsequently started worker claims and completes the same run"
    requirement: DUR-10
    verification:
      - kind: integration
        ref: "worker/shutdown_test.go#TestShutdownDrainTimeoutRollsBackAndReportsInFlightCount"
        status: pass
    human_judgment: false
  - id: D6
    description: "Shutdown is idempotent (a second call, on either an unstarted or a fully-drained worker, returns nil without redoing work) and safe on a worker whose Run was never invoked"
    requirement: DUR-10
    verification:
      - kind: unit
        ref: "worker/shutdown_test.go#TestShutdownCalledTwiceIsANoOpTheSecondTime"
        status: pass
      - kind: unit
        ref: "worker/shutdown_test.go#TestShutdownOnNeverStartedWorkerReturnsWithoutError"
        status: pass
    human_judgment: false
  - id: D7
    description: "Cancelling Run's own context produces the identical run-state outcome as calling Shutdown with a zero drain window"
    requirement: DUR-10
    verification:
      - kind: integration
        ref: "worker/shutdown_test.go#TestShutdownZeroDrainEquivalentToCancellingRunContext"
        status: pass
    human_judgment: false
  - id: D8
    description: "A dedicated crashworker binary and an in-process worker — two call sites of the identical worker.New — claim from one shared Postgres schema with no coordination, and every run reaches done exactly once"
    requirement: DUR-09
    verification:
      - kind: integration
        ref: "topology_test.go#TestTopologyDedicatedBinaryAndInProcessWorkerShareOneDatabase"
        status: pass
    human_judgment: false
  - id: D9
    description: "The dedicated binary exits within its configured drain window on a termination signal, leaving any claimed run either terminal or claimable — never stuck"
    requirement: DUR-10
    verification:
      - kind: integration
        ref: "topology_test.go#TestTopologyCrashworkerExitsWithinDrainWindowOnTermination"
        status: pass
    human_judgment: false

duration: ~35min
completed: 2026-08-15
status: complete
---

# Phase 02 Plan 07: Worker lifecycle — jittered polling, graceful shutdown, and two topologies of one object Summary

**`worker/loop.go` gives the claim pool a jittered, self-backstopping poll loop; `Worker.Shutdown` drains in-flight steps within a deadline and cancels past it with nothing crash-resume didn't already provide; and `internal/testdata/crashworker` proves the in-process and dedicated-binary topologies are the same `worker.New` by running both against one Postgres schema with zero coordination.**

## Performance

- **Duration:** ~35 min
- **Started:** 2026-08-15T13:55:00Z (approx.)
- **Completed:** 2026-08-15T14:30:00Z (approx.)
- **Tasks:** 3 (all `type="auto"`)
- **Files modified:** 9 (6 created, 3 modified)

## Accomplishments

- `worker/loop.go` carries `Run`/`pollLoop`/`claimOnceRecovered`/`jitteredInterval`, extracted out of `worker.go` so the constructor file stays about construction and validation. Jitter is applied fresh on every wait (never memoized at construction), a claimed run's goroutine attempts its next claim immediately with no interval wait, and `Engine.Notify`'s nudge short-circuits the wait while polling remains the correctness backstop — all four properties proven directly (`worker/loop_test.go`), the jitter band by calling `jitteredInterval` directly from an internal (white-box) test file, the rest by driving the loop through its exported surface against SQLite.
- `Worker.Shutdown` implements D-49's three beats — stop claiming, wait bounded by `DrainTimeout`/the caller's own context, cancel past the deadline and report how many claims were still in flight — using nothing crash-resume didn't already provide: a cancelled context makes an in-flight claim's remaining writes fail, which lands in the SAME deferred-rollback path a real crash already produces (D-30). Idempotent, safe on a never-started worker, and `Run` cancelling on its own `ctx` takes the literal same code path as a zero-drain `Shutdown` (`worker/shutdown_test.go`).
- `internal/testdata/crashworker` (a ~117-line `main`) is the DUR-09 worked example for the dedicated-binary topology and, verbatim, the binary plan 02-08's tier-2 harness will `SIGKILL`. It reads a Postgres DSN and `worker.Options` from environment variables (DSN value never logged), registers every flow `entflow.FlowsOf(schema.Order{})` returns, and drives graceful `Shutdown` on `SIGTERM`/`SIGINT`.
- `topology_test.go` builds the binary once in `TestMain`, then proves the plan's core claim against a real Postgres container: a dedicated subprocess and an in-process worker sharing one schema produce exactly one terminal transition per run with zero coordination, and the binary exits cleanly within its drain window on a termination signal.
- `README.md` documents both topologies as two call sites of one constructor (general shape first: N interchangeable claim goroutines against a shared table, in one or more processes), with the connection-pool guidance D-50 calls for.

## Task Commits

1. **Task 1: A jittered poll loop with an in-process nudge, where polling stays the backstop** - `84ec87f` (feat)
2. **Task 2: Graceful shutdown — stop claiming, let in-flight steps commit, cancel past the deadline** - `b95d4bf` (feat)
3. **Task 3: Both topologies, one object — the dedicated crashworker binary and the in-process worker** - `7a2fb89` (feat)

## Files Created/Modified

- `worker/loop.go` - `Run`/`pollLoop`/`claimOnceRecovered`/`jitteredInterval`, moved from `worker.go`; doc comments record why jitter is per-wait, why a claimed run skips the wait, and why cross-process nudges are deliberately out of scope until the outbox work
- `worker/loop_test.go` - internal (white-box) tests: jitter band sampled directly against `jitteredInterval`, nudge-shortens-wait, polling-backstop-with-nudges-discarded, no-wait-after-claim
- `worker/worker.go` - `Worker` struct grows `stopCh`/`mu`/`stopping`/`inFlight`/`zeroCond`/`pollWG`/`shutdownMu`/`shutdownDone`; `beginClaim`/`endClaim` (the atomic claim-lifecycle gate); `Shutdown`'s full D-49 implementation
- `worker/shutdown_test.go` - mid-step drain, drain-timeout paired-absence-and-resume, idempotency (both never-started and after-a-real-drain), never-started no-op, `Run`-context-cancellation-equals-zero-drain-Shutdown
- `internal/testdata/crashworker/main.go` - the dedicated worker binary worked example / tier-2 kill target
- `internal/testdata/crashworker/README.md` - environment variables, build instructions, dual role
- `topology_test.go` - `TestMain` builds the binary; two tests prove no-coordination cross-process claiming and graceful termination within the drain window
- `internal/testdata/pgtest/pgtest.go` - added `StartNWithDSN`; `StartN`/`Start` now delegate to it
- `README.md` - "Deploying the worker" section documenting both topologies as two call sites of one constructor, plus D-50's connection-pool guidance

## Decisions Made

- **Claim-lifecycle counting is a mutex + `sync.Cond`, not a `sync.WaitGroup`** — this is the plan's one real design deviation from the most obvious implementation, and it was forced by a genuine `-race` failure during development, not chosen up front. See Deviations below.
- **`stopCh` (a channel) and `stopping` (a mutex-guarded bool) are two separate mechanisms with two separate jobs**, both kept: `stopCh` wakes a `select` promptly even mid-wait; `stopping` is the atomic "may a claim start" decision `beginClaim` checks. Merging them was the FIRST design tried and is exactly what produced the `-race` failure.
- **`topology_test.go` proves "no duplicated effect" through `CancelOrder`'s own single step** (the owning `Order`'s status becoming `cancelled`) rather than a dedicated counter — `CancelOrder` has no counter field, and adding one to the fixture schema is explicitly plan 02-08 Task 1's own decision to make (an integer column on `Order`), not this plan's to preempt. This is documented as a deliberate scope boundary, not an oversight: this plan's job is proving the TOPOLOGY works with zero coordination (which SKIP LOCKED + D-30's atomic claim-execute-advance already certify, plan 02-03); a mutation-tested, regression-proof duplicate-effect assertion is 02-08's own mandate over its own multi-step fixture flow.
- **`pgtest.StartNWithDSN` added beyond the plan's literal file list** (Claude's discretion, Rule 2 — missing critical functionality): a real OS subprocess cannot share this test process's in-memory `*sql.DB` connection pool, so `topology_test.go` needs the raw connection string itself to point `crashworker` at the identical isolated schema `StartN`'s clients use. Every existing `StartN`/`Start` call site (`worker_test.go`, `conformance_test.go`, `claim_test.go`) is unaffected — they now delegate to the new function, discarding the DSN.
- **Finding required by the plan's own `<output>` instruction, stated explicitly:** graceful shutdown needed NOTHING crash-resume did not already provide. The only new mechanism `Shutdown` adds is a signal to stop claiming and a deadline-bounded wait; a cancelled context makes an in-flight claim's remaining database writes fail, which routes straight into `claimOnce`'s PRE-EXISTING deferred-rollback path (D-30) — the same path a real crash already takes. This is a "no" to the plan's own acceptance test, and no architectural gap was found in the crash-resume story while building it.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `sync.WaitGroup`-based claim-lifecycle tracking was a genuine, `-race`-detected concurrency bug, not a style preference**
- **Found during:** Task 2, running `go test ./worker/... -count=30 -race` after the first `Shutdown` implementation (which used `claimWG sync.WaitGroup` + `inFlight atomic.Int32`) passed cleanly in isolation but failed intermittently under repeated `-race` runs.
- **Issue:** `sync.WaitGroup`'s documented contract forbids a positive-delta `Add` racing a `Wait` call once the counter has previously reached zero — exactly the pattern N independent claim goroutines (each calling `Add(1)`/`Done()` per claim attempt) racing against `Shutdown`'s `claimWG.Wait()` produces the instant the in-flight count returns to zero between claims. The race detector caught a concrete instance: `Shutdown`'s drain goroutine calling `Wait()` at `worker.go:191` racing `claimOnceRecovered`'s `Add(1)` at `loop.go:150`. This was invisible in a single run and in the first several `-race` runs — exactly the kind of intermittent failure this plan's own SCOPE BOUNDARY warns against dismissing as flaky.
- **Fix:** Replaced `claimWG sync.WaitGroup` + `atomic.Int32` with `mu sync.Mutex` + `inFlight int` + `zeroCond *sync.Cond` (tied to the same `mu`). `beginClaim`/`endClaim` fully serialize increment/decrement/broadcast through one lock; `Shutdown`'s drain wait checks `inFlight > 0` under the SAME lock before ever calling `zeroCond.Wait()`, which is the standard race-free condition-variable pattern (no missed-wakeup window, because either the decrement-to-zero-and-Broadcast already happened before the check, or it happens later while genuinely inside `Wait`, which atomically releases the lock while blocked).
- **Files modified:** worker/worker.go, worker/loop.go
- **Verification:** `go test ./worker/... -run TestShutdown -count=50 -race` — 50 consecutive clean runs after the fix (0 failures), versus intermittent failures before it. Full suite (`ENTFLOW_REQUIRE_CRASHSIM=1 go test ./... -count=1 -race`) green, run multiple times.
- **Committed in:** `b95d4bf` (Task 2 commit — the fix landed before the task's own commit; no separate correction commit was needed since the bug was caught before ever being committed)

---

**Total deviations:** 1 auto-fixed (Rule 1 — a genuine concurrency bug caught by `-race` during development, fixed by changing the synchronization primitive rather than adding synchronization around the broken one). No scope creep; no architectural changes.

## Issues Encountered

- Two early test-writing bugs (both self-caught before any commit, not deviations in the plan-execution sense): (1) `worker/shutdown_test.go`'s slow-step tests initially called `w.Run(context.Background())` instead of the viewer-bearing test `ctx`, tripping `CancelOrderFlowRun`'s privacy `Policy()` denial (`no viewer in context`) and making the step closure appear to "never start" — traced with a throwaway debug test calling `w.ClaimOnce` directly to surface the swallowed error, then fixed by passing the correct `ctx`. (2) The `TestCancellingRunContextLeavesSameStateAsZeroDrainShutdown` test name didn't match the Task 2 verify command's `-run 'TestShutdown'` filter; renamed to `TestShutdownZeroDrainEquivalentToCancellingRunContext` so the task's own verify command exercises it.

## User Setup Required

None - no external service configuration required. Docker is already available and pre-pulled (`postgres:16-alpine`, `testcontainers/ryuk`) in this execution environment.

## Next Phase Readiness

- DUR-09 and DUR-10 are both fully proven: the poll loop's jitter/backstop/no-wait-after-claim properties, `Shutdown`'s three-beat drain with the paired-absence claim, and the two-topology no-coordination guarantee are all machine-checked against real SQLite and real Postgres, not just documented.
- `internal/testdata/crashworker` is exactly the binary plan 02-08's tier-2 harness needs: it already honors a graceful termination signal via `Shutdown`; 02-08's own Task 3 extends it (per that plan's `files_modified`) to additionally arm a named crash point from its environment and write a sentinel file before blocking, ahead of a real `SIGKILL`.
- `internal/crashpoint`'s registry (02-08 Task 1) will need call sites inside `worker/dbstep.go`'s claim-execute-advance path — this plan touched `worker/loop.go`/`worker/worker.go` but deliberately left `dbstep.go` untouched, so 02-08 starts from the same file plan 02-06 left it in.
- The multi-step fixture flow and its effect counter (02-08 Task 1's own decision) do not exist yet — `topology_test.go`'s "exactly once" proof rests on CancelOrder's single step plus the already-certified SKIP LOCKED/D-30 guarantees, not a dedicated counter. 02-08's crash-simulation suite is where the counter-based, mutation-tested version of that claim belongs, and this SUMMARY flags it explicitly so 02-08 doesn't need to rediscover the gap.
- No blockers.

---
*Phase: 02-durability*
*Completed: 2026-08-15*

## Self-Check: PASSED

All created/modified files verified present on disk (worker/loop.go, worker/loop_test.go, worker/shutdown_test.go, internal/testdata/crashworker/main.go, internal/testdata/crashworker/README.md, topology_test.go, worker/worker.go, internal/testdata/pgtest/pgtest.go, README.md). All referenced commit hashes (84ec87f, b95d4bf, 7a2fb89) verified present in `git log --oneline --all`.
