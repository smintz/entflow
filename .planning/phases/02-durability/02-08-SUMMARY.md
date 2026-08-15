---
phase: 02-durability
plan: 08
subsystem: testing
tags: [crash-simulation, postgres, testcontainers, sigkill, release-gate, worker]

# Dependency graph
requires:
  - phase: 02-durability
    provides: "plan 02-02's claim-execute-advance seams, plan 02-03's certified SkipLockedStrategy and internal/testdata/pgtest, plan 02-06's span wiring, plan 02-07's internal/testdata/crashworker binary and pgtest.StartNWithDSN"
provides:
  - "internal/crashpoint — the tier-1 logical crash-point registry (At/Install/Clear/Matrix), a single RLock-guarded map lookup with no hook installed, no build tag"
  - "worker/dbstep.go instrumented at every boundary crashpoint.Matrix enumerates (pre-claim once; after-claim/after-hydrate/before-step/after-step/after-advance/before-commit per step)"
  - "ProcessOrder — the multi-DB-step (reserve/charge/ship) crash-simulation fixture flow on Order, sharing CancelOrderFlowRun's table, with Order.effect_count as the D-55 atomic side-effect counter"
  - "entflow.RunMixin(flows ...Flow) — now variadic, deriving a shared run table's failed:<step> enum from every flow that persists through it"
  - "worker.Options.Flows — now actually honored by Worker.ClaimOnce (previously declared, never consulted)"
  - "crashsim_test.go (TestCrashSimTier1) — 19 graph-derived crash points against real Postgres, paired-absence + exact-baseline assertions"
  - "crashsim_tier2_test.go (TestCrashSimTier2) — 3 real SIGKILL crash points against real Postgres, sentinel-file + DB-state synchronization"
  - "internal/testdata/crashworker arming: ENTFLOW_CRASHWORKER_CRASH_POINT/ENTFLOW_CRASHWORKER_SENTINEL_PATH"
affects: []

# Actuals (#2632)
actuals:
  tokens: 19400
  tasks: 3
  commits: 3

tech-stack:
  added: []
  patterns:
    - "crashpoint.At is a single map lookup under RWMutex.RLock, no build tag — a hook returning a non-nil error propagates out of claimOnce exactly like any other claim-time error, rolling the whole transaction back (the same outcome a real crash produces)."
    - "Matrix(flow, steps) panics on an empty step list rather than returning an empty slice — an empty matrix is the one crash-harness failure mode that silently masquerades as a pass (ranging over zero subtests reports zero failures)."
    - "AfterClaim and AfterHydrate boundaries fire together, immediately after store.Load, rather than at their literal separate code positions — naming a crash point requires knowing which step is next (run.CurrentStep), which is only resolvable after the read-only Load; since Load has no durability consequence of its own, this produces an identical observable outcome to firing them strictly separately."
    - "RunMixin(flows ...Flow) is now variadic so two flows can legally share one physical run table — each flow's steps contribute their own failed:<step> enum values in the order passed; a shared step name would collide at ent codegen time (duplicate-value validation), which is the intended failure mode, not a silent merge."
    - "worker.Options.Flows was declared in plan 02-02/02-07 but never consulted by ClaimOnce — became load-bearing the instant two flows shared one physical table with no per-row flow discriminator, so ClaimOnce now filters w.eng.Flows() against it."
    - "crashSimBaseline + establishCrashSimBaseline are shared between both tiers (crashsim_test.go) so tier 1 and tier 2 certify the identical uncrashed ground truth rather than two independently-computed ones that could silently drift apart."
    - "Tier 2's crash-point arming hook writes its sentinel file via a temp-file-then-rename (POSIX atomic rename), then select{} — the process sits there, holding the claim's transaction open and uncommitted, until SIGKILLed; the parent test synchronizes on the sentinel file's existence AND the run's own committed database state via assert.Eventually's polling, never a bare time.Sleep."

key-files:
  created:
    - internal/crashpoint/crashpoint.go
    - internal/crashpoint/crashpoint_test.go
    - crashsim_test.go
    - crashsim_tier2_test.go
  modified:
    - worker/dbstep.go
    - worker/worker.go
    - run.go
    - internal/testdata/ent/schema/order.go
    - internal/testdata/ent/schema/order_flows.go
    - internal/testdata/ent/schema/cancelorderflowrun.go
    - internal/testdata/ent/ (regenerated: cancelorderflowrun, order, migrate, mutation, runtime)
    - internal/testdata/crashworker/main.go
    - internal/testdata/crashworker/README.md
    - README.md
    - topology_test.go
    - meta_test.go
    - exec_test.go

key-decisions:
  - "D-55's side-effect counter is an integer column on Order (effect_count), not a dedicated table — every DB step of ProcessOrder increments it inside the step's own transaction, the same write as the status mutation, so a duplicated effect is a wrong count and the counter is atomic with the effect it counts by construction."
  - "ProcessOrder (reserve/charge/ship) shares CancelOrderFlowRun's table with CancelOrder rather than getting its own run entity — matches D-23's per-flow-entity intent loosely but keeps this plan's file scope to schema edits already anticipated by plan 02-07's own forward note, at the cost of requiring RunMixin to become variadic (see below) and worker.Options.Flows to actually be honored."
  - "entflow.RunMixin(f Flow) became entflow.RunMixin(flows ...Flow) (Rule 2 — auto-added missing critical functionality, required by the plan's own instruction that RunMixin 'derives automatically from the new flow's graph'): backward compatible with every existing single-flow call site (RunMixin(f) still compiles unchanged)."
  - "worker.Options.Flows is now actually consulted by Worker.ClaimOnce (Rule 1 — a genuine, pre-existing bug: the field's own doc comment already promised this restriction, but ClaimOnce ignored it, which was invisible until two flows shared one physical table and a worker registered for both would let whichever flow's turn came first in map iteration order execute a row a different flow actually started)."
  - "The crash-point matrix and its per-step boundaries are keyed by string concatenation (step + \":\" + boundary) rather than a struct — simple, grep-able, and exactly what worker/dbstep.go and crashsim_test.go both independently reconstruct via crashpoint.Name, so the two can never name a boundary differently from each other."
  - "AfterClaim and AfterHydrate fire together in worker/dbstep.go's actual code, immediately after store.Load resolves which step is next, rather than AfterClaim firing strictly before Load as its name alone might suggest — documented inline at the call site and in crashpoint.go's own Boundary doc comment, since Load is a read with no durability consequence and firing both checks there is observably identical to firing them separately."

requirements-completed: [DUR-05, TEST-01, TEST-02]

coverage:
  - id: D1
    description: "internal/crashpoint is a build-tag-free registry: At is a no-op with no hook installed, an installed hook fires on every call until uninstalled, concurrent At/Install is race-clean, and Matrix panics on an empty step list rather than silently returning an empty matrix"
    requirement: TEST-01
    verification:
      - kind: unit
        ref: "internal/crashpoint/crashpoint_test.go#TestAtWithNoHookInstalledReturnsNil"
        status: pass
      - kind: unit
        ref: "internal/crashpoint/crashpoint_test.go#TestInstallFiresAndUninstallRestoresInertBehavior"
        status: pass
      - kind: unit
        ref: "internal/crashpoint/crashpoint_test.go#TestConcurrentAtDuringInstallIsRaceClean"
        status: pass
      - kind: unit
        ref: "internal/crashpoint/crashpoint_test.go#TestMatrixNeverEmptyForNonEmptyStepsAndPanicsOnEmpty"
        status: pass
      - kind: unit
        ref: "internal/crashpoint/crashpoint_test.go#TestMatrixThreeStepFlowMoreCrashPointsThanOneStepFlow"
        status: pass
      - kind: unit
        ref: "internal/crashpoint/crashpoint_test.go#TestEveryMatrixBoundaryTypeReachableInClaimPath"
        status: pass
    human_judgment: false
  - id: D2
    description: "Tier 1: all 19 crash points crashpoint.Matrix enumerates from ProcessOrder's 3-step graph (pre-claim once, plus 6 boundaries each for reserve/charge/ship) each assert D-55's paired-absence claim and, after resuming with a different worker, exact equality with the uncrashed baseline including the effect counter"
    requirement: TEST-01
    verification:
      - kind: integration
        ref: "crashsim_test.go#TestCrashSimTier1 (19 subtests + baseline, real Postgres via testcontainers)"
        status: pass
    human_judgment: false
  - id: D3
    description: "Tier 1's own mutation-testing verification: a deliberately duplicated effect and a deliberately unpaired progress advance (advance without applying the effect) each made TestCrashSimTier1/baseline fail; both were manually reverted, verified via git diff clean"
    requirement: TEST-01
    verification:
      - kind: other
        ref: "manual verification pass, documented in Deviations/Verification below — not a permanent test artifact"
        status: pass
    human_judgment: false
  - id: D4
    description: "Tier 2: a real internal/testdata/crashworker subprocess is SIGKILLed at 3 explicitly chosen crash points (first step post-effect-pre-commit, one mid-flow boundary, last step post-effect-pre-commit) against real Postgres, synchronized on the sentinel file plus observed database state with zero time.Sleep calls in the file; paired absence and exact-baseline resume are asserted from a fresh connection"
    requirement: TEST-02
    verification:
      - kind: integration
        ref: "crashsim_tier2_test.go#TestCrashSimTier2 (3 subtests + baseline, real Postgres, real SIGKILL)"
        status: pass
    human_judgment: false
  - id: D5
    description: "Both tiers skip loudly with Docker unavailable, and ENTFLOW_REQUIRE_CRASHSIM=1 turns that skip into a failure instead of a silent pass; the harness's own t.Logf report names tiers, dialect, enumerated/executed/skipped counts, and states the gate certifies PostgreSQL only"
    requirement: TEST-02
    verification:
      - kind: other
        ref: "manual verification via ENTFLOW_TEST_FORCE_DOCKER_UNAVAILABLE=1 (both with and without ENTFLOW_REQUIRE_CRASHSIM=1), both tiers"
        status: pass
    human_judgment: false
  - id: D6
    description: "Standing build-graph gates hold: go list -deps ./... contains zero testcontainers/jackc-pgx/otel-sdk/otelhttp/bare-otel-root packages; TestNoTransportDeps and TestTracingDependencyNarrowness pass; gofmt is clean on every hand-written file"
    requirement: TEST-02
    verification:
      - kind: unit
        ref: "deps_test.go#TestNoTransportDeps"
        status: pass
      - kind: unit
        ref: "deps_test.go#TestTracingDependencyNarrowness"
        status: pass
      - kind: other
        ref: "go list -deps ./... | grep -Ei 'testcontainers|jackc/pgx|otel/sdk|otelhttp' (excluding internal/testdata) — empty; gofmt -l . (excluding internal/testdata/ent/) — empty"
        status: pass
    human_judgment: false

duration: ~2h10min
completed: 2026-08-15
status: complete
---

# Phase 02 Plan 08: The crash-simulation release gate — internal/crashpoint, a multi-step fixture, and two real tiers against real Postgres Summary

**A build-tag-free crash-point registry (`internal/crashpoint`) instruments every boundary of `worker/dbstep.go`'s claim-execute-advance-commit transaction; a new three-step `ProcessOrder` fixture flow with an atomic `effect_count` counter gives the harness something worth crashing; and two real test suites — 19 in-process logical crash points (tier 1) and 3 real `SIGKILL`s against a real worker subprocess (tier 2) — both pass against real Postgres, proving no step effect ever commits without its progress record and no effect is ever applied twice.**

## Performance

- **Duration:** ~2h10min
- **Started:** 2026-08-15T14:24:00Z (approx.)
- **Completed:** 2026-08-15T16:35:00Z (approx.)
- **Tasks:** 3 (all `type="auto"`)
- **Files modified:** 26 (4 created, 22 modified, including 5 regenerated ent packages)

## Accomplishments

- `internal/crashpoint` (`crashpoint.go`) is a single `RWMutex`-guarded map: `At` is one `RLock` plus a lookup returning `nil` with nothing installed — no build tag, no allocation, nothing a production deployment has to know about. `Matrix(flow, steps)` enumerates the flow-scoped `pre-claim` boundary once, then six per-step boundaries (`after-claim`, `after-hydrate`, `before-step`, `after-step`, `after-advance`, `before-commit`) in a stable, deterministic order, and panics rather than returning an empty slice for zero steps — an empty matrix is the one failure mode that can silently pass as a release gate.
- `worker/dbstep.go` gained 7 `crashpoint.At` call sites at exactly those boundaries, named identically via the shared `crashpoint.Name` construction so the registry and the claim path can never drift apart.
- `internal/testdata/ent/schema`: `order.go` gained `effect_count` (D-55's atomic side-effect counter — a column on `Order`, not a dedicated table); `order_flows.go` added `ProcessOrder`, a three-DB-step fixture flow (`reserve`→`charge`→`ship`) declared out of dependency order in source and wired back correctly via `After` edges, sharing `CancelOrderFlowRun`'s table with `CancelOrder`.
- `entflow.RunMixin` became variadic (`RunMixin(flows ...Flow)`) so `CancelOrderFlowRun`'s `state` enum derives `failed:<step>` values from **both** flows sharing its table — required by the plan's own instruction and backward compatible with every existing single-flow call site.
- `worker.Options.Flows`, declared since plan 02-02 but never consulted, is now actually honored by `Worker.ClaimOnce` — a real, previously-latent bug that became load-bearing the moment two flows shared one physical run table with no per-row flow discriminator.
- `crashsim_test.go` (`TestCrashSimTier1`) establishes an uncrashed `ProcessOrder` baseline (its own subtest) and runs 19 subtests — one per `crashpoint.Matrix`-enumerated name — each installing a hook that aborts a specific claim, asserting D-55's paired-absence claim as a single combined condition, then resuming with a **different** worker and asserting exact equality (terminal state, owning `Order`, and effect counter) with the baseline. Verified as a real mutation-catching harness, not merely a passing one: a deliberately doubled effect counter and a deliberately unpaired progress-advance-without-effect were each injected, observed to fail `TestCrashSimTier1/baseline`, and reverted (`git diff` clean).
- `internal/testdata/crashworker/main.go` now arms a crash point from `ENTFLOW_CRASHWORKER_CRASH_POINT`/`ENTFLOW_CRASHWORKER_SENTINEL_PATH`: the installed hook writes the sentinel file atomically (temp file + rename) and then blocks forever, holding the claim's transaction open until the process is `SIGKILL`ed.
- `crashsim_tier2_test.go` (`TestCrashSimTier2`) terminates the real `crashworker` binary with `SIGKILL` at 3 explicitly chosen crash points (first step's post-effect-pre-commit boundary, one mid-flow boundary, last step's post-effect-pre-commit boundary), synchronized on the sentinel file plus the run's own observed committed database state — zero `time.Sleep` calls anywhere in the file (confirmed via `grep -c`). Asserts paired absence from a fresh connection, then resumes with a different in-process worker and asserts exact baseline equality.
- `README.md` gained a "The crash-simulation release gate" section documenting both tiers, exactly what they certify (PostgreSQL only), and the `ENTFLOW_REQUIRE_CRASHSIM=1` release-gate environment variable.

## Task Commits

1. **Task 1: The crash-point registry, the graph-derived matrix, and a fixture worth crashing** - `b6bffa9` (feat)
2. **Task 2: Tier 1 — every logical crash point, with paired-absence assertions** - `798556e` (test)
3. **Task 3: Tier 2 — terminate a real worker process, and make the harness report what it certifies** - `1005520` (test)

## Files Created/Modified

- `internal/crashpoint/crashpoint.go` - `At`/`Install`/`Clear`/`Matrix`/`Name`/`PreClaimName`/`Boundary` constants
- `internal/crashpoint/crashpoint_test.go` - registry unit tests, Matrix property tests, boundary-type reachability check against `worker/dbstep.go`'s source
- `worker/dbstep.go` - 7 `crashpoint.At` call sites in `claimOnce`
- `worker/worker.go` - `ClaimOnce` now filters `w.eng.Flows()` against `w.opts.Flows`
- `run.go` - `RunMixin(f Flow)` → `RunMixin(flows ...Flow)`
- `internal/testdata/ent/schema/order.go` - `effect_count` field
- `internal/testdata/ent/schema/order_flows.go` - `ProcessOrder` flow (`reserve`/`charge`/`ship`)
- `internal/testdata/ent/schema/cancelorderflowrun.go` - `Mixin()` now passes `Order{}.Flows()...`
- `internal/testdata/ent/{cancelorderflowrun,order,migrate,mutation.go,order_create.go,order_update.go,runtime}` - regenerated
- `crashsim_test.go` - `TestCrashSimTier1`, `establishCrashSimBaseline`, shared helpers (`newCrashSimWorker`, `driveToTerminal`, `advanceExactClaims`, `parseCrashPointName`, `stepIndex`)
- `crashsim_tier2_test.go` - `TestCrashSimTier2`, `waitForSentinelAndUnchangedRun`, `tier2CrashworkerEnv`
- `internal/testdata/crashworker/main.go` - `armCrashPoint`, `writeSentinelAtomically`
- `internal/testdata/crashworker/README.md` - documents the new env var pair
- `README.md` - "The crash-simulation release gate" section
- `topology_test.go` - pins `Options.Flows`/`ENTFLOW_CRASHWORKER_FLOWS` to `CancelOrder` (regression fix — see Deviations)
- `meta_test.go`, `exec_test.go` - `cancelOrderFlow`/local helper selects `CancelOrder` by name instead of assuming a single-element `Flows()` slice (regression fix)

## Decisions Made

- **D-55's counter is a column on `Order`, not a dedicated table** (already Claude's Discretion per 02-RESEARCH.md) — every `ProcessOrder` step increments it inside the step's own transaction, atomic with the effect it counts by construction.
- **`ProcessOrder` shares `CancelOrderFlowRun`'s table** rather than getting its own run entity — this is what actually made "which entflow.RunMixin derives automatically from the new flow's graph" (the plan's own words) a real requirement rather than a hypothetical, and is why `RunMixin` became variadic.
- **`entflow.RunMixin` is now variadic** (Rule 2, auto-added missing critical functionality) — backward compatible; `RunMixin(f)` still compiles for every existing single-flow caller.
- **`worker.Options.Flows` is now actually honored** (Rule 1, a genuine pre-existing bug) — its own doc comment already promised the restriction ClaimOnce silently never implemented; this was invisible until two flows shared one physical table.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing Critical Functionality] `entflow.RunMixin` needed to become variadic**
- **Found during:** Task 1, wiring `ProcessOrder`'s steps into `CancelOrderFlowRun`'s shared `state` enum
- **Issue:** The plan's own action text requires "the additional `failed:<step>` enum values the new flow's steps produce, which `entflow.RunMixin` derives automatically from the new flow's graph" — but `RunMixin(f Flow)` only ever derived from ONE flow, and `cancelorderflowrun.go`'s `Mixin()` passed only `Order{}.Flows()[0]` (CancelOrder).
- **Fix:** Changed `RunMixin(f Flow)` to `RunMixin(flows ...Flow)`, unioning every flow's step-derived `failed:<step>` values in the order passed; `cancelorderflowrun.go` now calls `entflow.RunMixin(Order{}.Flows()...)`. Backward compatible — every existing single-flow call site (`run_test.go`) is unaffected.
- **Files modified:** run.go, internal/testdata/ent/schema/cancelorderflowrun.go
- **Verification:** regenerated ent output shows `StateFailedReserve`/`StateFailedCharge`/`StateFailedShip` alongside the existing `StateFailedCancel`/`StateFailedRefund`/`StateFailedOrderCancelled`; full suite green.
- **Committed in:** `b6bffa9` (Task 1 commit)

**2. [Rule 1 - Bug] `worker.Options.Flows` was declared but never consulted by `ClaimOnce`**
- **Found during:** Task 1, running `topology_test.go`'s pre-existing suite after `ProcessOrder` was added — `TestTopologyDedicatedBinaryAndInProcessWorkerShareOneDatabase` failed: seeded `CancelOrder` runs ended up in status `"shipped"` instead of `"cancelled"`.
- **Issue:** `crashworker`'s own `main.go` registers "every flow `entflow.FlowsOf(schema.Order{})` returns" (its documented behavior since plan 02-07) — now two flows. `Worker.ClaimOnce` iterated `w.eng.Flows()` unconditionally, ignoring the already-declared `Options.Flows` restriction entirely, so whichever flow's turn came first in (unordered) map iteration claimed and executed a row a DIFFERENT flow actually started — the shared table carries no per-row flow discriminator.
- **Fix:** `ClaimOnce` now filters `w.eng.Flows()` against `w.opts.Flows` when non-empty — restoring the field's own documented, previously-unimplemented behavior.
- **Files modified:** worker/worker.go
- **Verification:** `topology_test.go`'s two pre-existing tests pass again with `Flows`/`ENTFLOW_CRASHWORKER_FLOWS` pinned to `CancelOrder`; full suite green with `-race`.
- **Committed in:** `b6bffa9` (Task 1 commit)

**3. [Rule 1 - Bug] Three existing tests assumed `Order{}.Flows()` returns exactly one flow**
- **Found during:** Task 1, full-suite regression run after `ProcessOrder` was added to `Order.Flows()`
- **Issue:** `meta_test.go`'s `cancelOrderFlow` helper (reused by several files) and `exec_test.go`'s own inline check both asserted `require.Len(t, flows, 1)`.
- **Fix:** Both now select the `CancelOrder` flow by name instead of assuming a single-element slice.
- **Files modified:** meta_test.go, exec_test.go
- **Verification:** full suite green.
- **Committed in:** `b6bffa9` (Task 1 commit)

---

**Total deviations:** 3 auto-fixed (1 Rule 2 — a required, backward-compatible API widening the plan's own text called for; 2 Rule 1 — genuine bugs surfaced, not caused in the sense of new defects, by two flows legitimately sharing one physical table for the first time). No scope creep beyond what the plan's own multi-step-fixture-flow requirement necessitated.

## Verification (mutation testing, per acceptance criteria — not permanent code)

Per the plan's own acceptance criteria for Task 2, two mutations were manually introduced, observed to fail the suite, and reverted:

1. **Deliberately duplicated effect** — `ProcessOrder`'s `reserve` step temporarily changed `AddEffectCount(1)` to `AddEffectCount(2)`. Result: `TestCrashSimTier1/baseline` failed (`expected: 3, actual: 4`). Reverted; `git diff` on `order_flows.go` clean afterward.
2. **Progress advanced without applying the effect** — `ProcessOrder`'s `charge` step temporarily replaced its `SetStatus(...).AddEffectCount(1).Save(ctx)` with a plain `tx.Order.Get(ctx, in.OrderID)` (still returns success, so the run's progress pointer still advances). Result: `TestCrashSimTier1/baseline` failed (`expected: 3, actual: 2`). Reverted; `git diff` clean afterward.

Both mutations were caught by the exact-equality effect-counter check against the uncrashed baseline — precisely the mechanism the plan calls out as catching "a run that duplicated an effect and reached the right state anyway."

Docker-unavailable / `ENTFLOW_REQUIRE_CRASHSIM=1` behavior was verified for both tiers via `ENTFLOW_TEST_FORCE_DOCKER_UNAVAILABLE=1`:
- Without the env var: both tiers skip with a message naming Docker.
- With `ENTFLOW_REQUIRE_CRASHSIM=1`: both tiers fail (`t.Fatalf`) instead of skipping.

## Issues Encountered

None beyond the three deviations documented above, all resolved during Task 1 before Task 2/3 began.

## User Setup Required

None — Docker was already available and pre-pulled (`postgres:16-alpine`, `testcontainers/ryuk:0.11.0`/`0.14.0`) in this execution environment.

## Release-Gate Evidence

Actual test names and outcomes from this environment, run with `ENTFLOW_REQUIRE_CRASHSIM=1 go test ./... -count=1 -race -v` (167 total test cases, 0 failures):

**Tier 1 — `TestCrashSimTier1`** (real Postgres via testcontainers, 3.55s): `baseline`, `pre-claim`, and 6 boundaries each (`after-claim`/`after-hydrate`/`before-step`/`after-step`/`after-advance`/`before-commit`) for `reserve`, `charge`, `ship` — 20 subtests total, all PASS. Harness report:
```
crash-simulation harness report | tier: tier-1 (in-process logical crash points) | dialect: postgres | flow: ProcessOrder | crash points enumerated: 19 | executed: 19 | skipped: 0 | this run certifies postgres only, not MySQL or SQLite
```

**Tier 2 — `TestCrashSimTier2`** (real Postgres via testcontainers, real `SIGKILL`, 0.26s): `baseline`, `first step, post-effect-pre-commit (reserve:after-step)`, `mid-flow boundary (charge:before-commit)`, `last step, post-effect-pre-commit (ship:after-step)` — 4 subtests, all PASS. Harness report:
```
crash-simulation harness report | tier: tier-2 (real subprocess SIGKILL against real Postgres) | dialect: postgres | flow: ProcessOrder | crash points enumerated: 3 | executed: 3 | skipped: 0 | this gate certifies PostgreSQL only — it does NOT certify MySQL or SQLite
```

Standing gates also verified in this run: `go build ./...`, `go vet ./...` exit 0; `gofmt -l .` clean on every hand-written file (`internal/testdata/ent/` generated output exempt); `go list -deps ./...` contains zero `testcontainers`/`jackc/pgx`/`otel/sdk`/`otelhttp`/bare-`otel`-root packages outside `internal/testdata`; `TestNoTransportDeps` and `TestTracingDependencyNarrowness` pass; `go generate ./internal/testdata/ent/...` is idempotent (no diff on a second run); no compiled `crashworker` binary left in the repository.

## Next Phase Readiness

- DUR-05, TEST-01, and TEST-02 are fully proven, not merely documented: a worker interrupted at any of 19 logical boundaries (tier 1) or 3 real-process-kill boundaries (tier 2) resumes to the exact same terminal state as an uncrashed baseline, with the effect counter proving no duplicated effect and the paired-absence assertion proving no orphaned progress pointer.
- The harness is extensible by construction (D-56): `crashpoint.Matrix` derives its enumeration from the flow's own step graph via `Runner.StepOrder()`, so Phase 3's activity beats and Phase 4's relay boundaries extend the same registry and the same matrix shape rather than starting a new mechanism.
- `entflow.RunMixin`'s variadic signature and `worker.Options.Flows` actually being honored are both now load-bearing, tested behaviors any future plan adding a second flow to a shared run table (or a worker that must poll a subset of registered flows) can rely on.
- No blockers.

---
*Phase: 02-durability*
*Completed: 2026-08-15*

## Self-Check: PASSED

All created/modified files verified present on disk (internal/crashpoint/crashpoint.go, internal/crashpoint/crashpoint_test.go, crashsim_test.go, crashsim_tier2_test.go, worker/dbstep.go, worker/worker.go, run.go, internal/testdata/ent/schema/order.go, internal/testdata/ent/schema/order_flows.go, internal/testdata/ent/schema/cancelorderflowrun.go, internal/testdata/crashworker/main.go, internal/testdata/crashworker/README.md, README.md, topology_test.go, meta_test.go, exec_test.go). All referenced commit hashes (b6bffa9, 798556e, 1005520) verified present in `git log --oneline --all`.
