---
phase: 02-durability
plan: 05
subsystem: database
tags: [ent, privacy, worker, sqlite, context-key, ops]

# Dependency graph
requires:
  - phase: 02-durability
    provides: "plan 02-02's claim-execute-advance transaction (worker/dbstep.go) and RunStore/Engine seams, plan 02-03's certified SkipLockedStrategy/claim pool, and plan 02-04's terminal states (done/failed:<step>) and honest per-claim context this plan's marker and viewer hooks thread through"
provides:
  - "internal/wfmarker + entflow.IsWorkflow — the workflow marker (D-44), unsettable by construction outside the module and, within it, called from exactly one file (worker/dbstep.go), both proven mechanically (AST scan + a real compile-fail fixture)"
  - "internal/testdata/entflowfixture.Viewer + WithViewer/ViewerFromContext/AdminViewer — the application's own viewer type entflow core never names (D-45)"
  - "CancelOrderFlowRun's Policy() — an ordinary ent privacy.Policy proving OPS-02 with no entflow-supplied privacy rule"
  - "worker.Options.Context applied before the workflow marker on every claim (D-45), documented as load-bearing ordering"
  - "entflow.Engine.Cancel — the D-43 guarded cancel transition, run under the caller's own context/viewer"
  - "the OPS-01 query proof (ops_test.go) — runs queried by owner edge, terminal failure state, and creation order entirely through the generated client"
affects: [02-06, 02-07, 02-08]

# Actuals (#2632)
actuals:
  tokens: 18644
  tasks: 3
  commits: 3

tech-stack:
  added: []
  patterns:
    - "internal/wfmarker: the unexported-context-key pattern (result.go's resultsKey) applied a second time, but under internal/ rather than the public module — the compiler, not just an AST-scan test, keeps the marker's key type unreachable from any code outside the package, so even a same-module caller cannot forge it by constructing the key directly (only wfmarker.Set can)."
    - "marker_test.go: go/parser-based AST scans (deps_test.go's own style) over `go list -json ./...`'s GoFiles — computed, not hand-curated — assert exactly one call site of wfmarker.Set and exactly one file (worker/claim.go) calling QueryContext/ExecContext."
    - "cancelorderflowrun.go's Policy(): a hand-written privacy.QueryRule adapter (queryRuleFunc) alongside entgo.io/ent/privacy's own MutationRuleFunc — the base privacy package ships no generic QueryRuleFunc without the unused 'privacy' codegen feature flag, so this fixture supplies the one-method adapter itself rather than enabling a feature this plan doesn't otherwise need."
    - "worker/dbstep.go's claim-context derivation order is fixed and documented as load-bearing: Options.Context runs first, wfmarker.Set is applied to ITS result, so an application-supplied hook can never observe or forge the marker it has not yet been given."

key-files:
  created:
    - internal/wfmarker/wfmarker.go
    - workflowmarker.go
    - internal/testdata/compilefail/wfmarker_internal.go
    - marker_test.go
    - internal/testdata/entflowfixture/viewer.go
    - ops_test.go
    - cancel_test.go
  modified:
    - worker/dbstep.go
    - worker/options.go
    - internal/testdata/ent/schema/cancelorderflowrun.go
    - internal/testdata/ent/ (regenerated: cancelorderflowrun/, cancelorderflowrun_create.go, cancelorderflowrun_query.go, cancelorderflowrun_update.go, client.go, runtime/runtime.go)
    - engine.go
    - durablerun_test.go
    - cancelorderflowrun_test.go
    - selfloader_test.go
    - worker/claim_test.go
    - worker/conformance_test.go
    - worker/worker_test.go
    - worker/retry_test.go

key-decisions:
  - "The compile-fail fixture (wfmarker_internal.go) does not attempt to reproduce Go's cross-module internal-package import restriction — internal/testdata/compilefail is itself inside the entflow module, so importing internal/wfmarker from it legally compiles. Instead it documents the boundary the compiler actually enforces universally, including intra-module: wfmarker's context-key type is unexported, so no package anywhere — inside the module or out — can forge a marked context by constructing that key directly; only wfmarker.Set can produce one. Verified with a real, isolated `go build` of the single file (Go's own 'file-list-as-package' rule keeps this independent of activity_tx.go's unrelated, pre-existing compile failure in the same directory)."
  - "CancelOrderFlowRun's Policy() extends the plan's literal two-rule mutation description (marker-allow, cancel-guard) with a third, universal admin-allow rule, and a Create-ownership check the plan's prose didn't spell out. Without a universal admin bypass, an admin-viewer mutation that isn't a Create or a cancel-set Update (e.g. a plain field update) would fall through to the final AlwaysDenyRule with no path to Allow — the query side already gives an admin viewer unrestricted read access, and the mutation side needed the same guarantee for consistency (Rule 2: missing critical functionality, since a Policy() an admin cannot administer through defeats OPS-02's own purpose)."
  - "Every pre-existing Phase 2 test that reaches CancelOrderFlowRun through the generated client (durablerun_test.go, cancelorderflowrun_test.go, selfloader_test.go, worker/claim_test.go, worker/conformance_test.go, worker/worker_test.go, worker/retry_test.go) now attaches entflowfixture.AdminViewer() to its base context. This is a direct, in-scope consequence of adding Policy() at all (Rule 1/2 deviation) — before this plan, no privacy pipeline existed to deny a viewer-less context; the fix is one line per test function, not a redesign of any test's actual assertions."
  - "entflow.Cancel is a method on *Engine, not a package-level generic function like Start: Start needed to be package-level only because Go methods cannot declare a second type parameter (In), and Cancel needs no type parameter at all (runID is already erased to any). A method is the more natural fit and needed no new erasure mechanism."
  - "The mid-step cancel test (cancel_test.go) deliberately does NOT use entclient.New's shared-cache in-memory SQLite client — plan 02-03's own documented pitfall (shared-cache mode's SQLITE_LOCKED error path is not covered by modernc.org/sqlite's busy-handler retry) would make a genuine blocking-then-retry assertion flaky. It reuses the file-backed, _busy_timeout-bearing setup worker/conformance_test.go already certified for exactly this purpose."

requirements-completed: [OPS-01, OPS-02, OPS-04]

coverage:
  - id: D1
    description: "internal/wfmarker's Set/Is are reachable only from inside the entflow module by Go's own import rule; entflow.IsWorkflow is the marker's only public read accessor; the internal setter has exactly one call site (worker/dbstep.go), proven by an AST scan over the whole module rather than a hand-maintained list; a real compile-fail fixture proves even an in-module caller cannot forge the marker by constructing its context key directly"
    requirement: OPS-04
    verification:
      - kind: unit
        ref: "marker_test.go#TestWorkflowMarkerHasExactlyOneSetter"
        status: pass
      - kind: unit
        ref: "marker_test.go#TestIsWorkflowFalseForEveryPublicContextPath"
        status: pass
      - kind: unit
        ref: "marker_test.go#TestWorkflowMarkerKeyCompileFail"
        status: pass
    human_judgment: false
  - id: D2
    description: "The generated raw-query/raw-exec escape hatch (QueryContext/ExecContext) is called from exactly one non-test, non-testdata file in the module — worker/claim.go — closing T-02-02's remaining half mechanically"
    verification:
      - kind: unit
        ref: "marker_test.go#TestRawQueryConfinedToClaim"
        status: pass
    human_judgment: false
  - id: D3
    description: "CancelOrderFlowRun's Policy() denies a viewer-less query outright, scopes a non-admin viewer's reads to their own order, and lets an admin viewer see every run"
    requirement: OPS-02
    verification:
      - kind: unit
        ref: "ops_test.go#TestRunQueryDeniedWithNoViewer"
        status: pass
      - kind: unit
        ref: "ops_test.go#TestRunQueryScopedToOwnerOrAdmin"
        status: pass
    human_judgment: false
  - id: D4
    description: "A non-owner's cancel mutation is denied and leaves the stored state unchanged; the worker's own context, derived through Options.Context, can advance a run, while a worker with no hook configured is honestly denied by the same Policy()"
    requirement: OPS-02
    verification:
      - kind: unit
        ref: "ops_test.go#TestCancelDeniedForNonOwner"
        status: pass
      - kind: unit
        ref: "ops_test.go#TestWorkerAdvancesRunOnlyWithOptionsContext"
        status: pass
    human_judgment: false
  - id: D5
    description: "Cancelling a pending run moves it to cancelled and it is never claimed again; cancelling twice reports the guard did not match and leaves state/finished_at byte-identical; cancelling a done run reports no match"
    requirement: OPS-04
    verification:
      - kind: unit
        ref: "cancel_test.go#TestCancelPendingRunMovesToCancelledAndNeverClaimedAgain"
        status: pass
      - kind: unit
        ref: "cancel_test.go#TestCancelTwiceIsIdempotent"
        status: pass
      - kind: unit
        ref: "cancel_test.go#TestCancelDoneRunReportsNoMatch"
        status: pass
    human_judgment: false
  - id: D6
    description: "A cancel issued while a step is mid-flight blocks on the claim transaction's row lock until that step commits, then applies: the step's effect is present, the run reaches cancelled, and the second step of a two-step flow never executes"
    requirement: OPS-04
    verification:
      - kind: unit
        ref: "cancel_test.go#TestCancelWhileStepInFlightAppliesAfterCommit"
        status: pass
    human_judgment: false
  - id: D7
    description: "An operator queries runs by owner edge, by terminal failure state, and in creation order entirely through the generated client and predicate package, with no entflow-specific query helper"
    requirement: OPS-01
    verification:
      - kind: unit
        ref: "ops_test.go#TestOpsQueryRunsByOwnerFailureStateAndCreationOrder"
        status: pass
    human_judgment: false

duration: ~50min
completed: 2026-08-15
status: complete
---

# Phase 02 Plan 05: Privilege boundary and ops surface Summary

**A run is now an ordinary, queryable, cancellable ent entity governed by a hand-written `Policy()` — no entflow-supplied privacy rule exists — and the privilege boundary that makes that safe (`internal/wfmarker`, unreachable from outside the module and mechanically proven to have exactly one setter) is closed three phases early, by the compiler and an AST scan rather than a comment.**

## Performance

- **Duration:** ~50 min
- **Started:** 2026-08-15T13:10:00Z (approx.)
- **Completed:** 2026-08-15T13:24:04Z
- **Tasks:** 3 (all `type="auto"`)
- **Files modified:** 25 (7 created, 18 modified)

## Accomplishments

- `internal/wfmarker` holds the workflow marker under an unexported context-key type: `Set`/`Is` are reachable from any package inside the entflow module (that's how `worker/dbstep.go` legitimately sets it) but from nowhere outside it, by Go's own import rule. `entflow.IsWorkflow` (`workflowmarker.go`) is the marker's only public surface. `marker_test.go`'s `TestWorkflowMarkerHasExactlyOneSetter` parses every non-test, non-testdata `.go` file in the module (via `go list -json ./...`, the same "derived, never hand-curated" style `deps_test.go` established) and asserts the internal setter is called from exactly one file. `TestWorkflowMarkerKeyCompileFail` proves the boundary the AST scan can't: a real, isolated `go build` of `internal/testdata/compilefail/wfmarker_internal.go`, which tries to forge a marked context by referencing the unexported key type directly, fails with "name ctxKey not exported by package wfmarker" — nobody, not even in-module code, can bypass `Set`.
- `TestRawQueryConfinedToClaim` closes T-02-02's remaining half mechanically: `QueryContext`/`ExecContext` are called from exactly one non-test file, `worker/claim.go`.
- `internal/testdata/ent/schema/cancelorderflowrun.go` gains an ordinary `privacy.Policy()` — entflow core ships nothing privacy-related. Query side denies a viewer-less context, scopes a non-admin viewer's reads to their own order (`Viewer.Subject`, tied to the owner edge), and lets an admin see everything. Mutation side allows any mutation carrying the workflow marker (the worker's own Advance/Fail writes) or coming from an admin viewer, then gates a Create to the viewer starting a run on their own order and a cancel-state Update to the run's owner, denying everything else via the final `AlwaysDenyRule`.
- `worker/dbstep.go` applies `Options.Context` to the claim's context **before** `wfmarker.Set`, so an application-supplied hook can never observe or forge the marker — documented as load-bearing on both the claim path and `Options.Context`'s own doc comment. A worker with no hook configured is honestly denied by a privacy-governed run entity (`TestWorkerAdvancesRunOnlyWithOptionsContext` asserts both the denial and the success once a hook is supplied).
- `entflow.Engine.Cancel` is the D-43 guarded transition an application calls without hand-writing the transaction dance: it resolves the flow's store, opens a transaction under the **caller's own** context/viewer (never the worker's), calls the already-existing `RunStore.Cancel` (forward-declared in plan 02-02), and commits. No stop signal exists or is needed — every terminal state is already absent from the claim predicate. `cancel_test.go` proves cancelling twice is a safe no-op (byte-identical state/`finished_at`), cancelling a done run is a no-op, and — against a real blocking-retry file-backed SQLite setup (the same one `worker/conformance_test.go` certified) — a cancel issued mid-step blocks on the claim transaction's row lock until that step commits, then applies, with the step's effect present and the second step of a two-step flow never executing.
- `ops_test.go` proves OPS-01 by using it: runs queried by owner edge, by terminal failure state (`failed:cancel`), and in creation order, entirely through the generated client and predicate package.

## Task Commits

1. **Task 1: The workflow marker — unsettable by construction, plus the raw-SQL confinement gate** - `9fb4203` (feat)
2. **Task 2: An ordinary Policy() on the run entity, and the worker's own viewer** - `d281f22` (feat)
3. **Task 3: Cancel an in-flight run as a guarded transition, and query runs as ordinary entities** - `fe71e35` (feat)

## Files Created/Modified

- `internal/wfmarker/wfmarker.go` - the marker: unexported context-key type, `Set`/`Is`
- `workflowmarker.go` - `entflow.IsWorkflow`, the marker's only public accessor
- `internal/testdata/compilefail/wfmarker_internal.go` - compile-fail fixture proving the key type is unforgeable
- `marker_test.go` - AST-scan setter-count test, public-context-path test, compile-fail test, raw-query-confinement test
- `internal/testdata/entflowfixture/viewer.go` - the application's own `Viewer` type (`Subject`, `Admin`), `WithViewer`/`ViewerFromContext`/`AdminViewer`
- `internal/testdata/ent/schema/cancelorderflowrun.go` - `Policy()`: query denial/scoping, mutation marker/admin/create/cancel rules
- `internal/testdata/ent/*` - regenerated output (privacy-aware query/mutation builders, runtime split)
- `worker/dbstep.go` - claim-context derivation order: `Options.Context` then `wfmarker.Set`
- `worker/options.go` - `Options.Context`'s doc comment states the privacy-governed-entity requirement
- `engine.go` - `Engine.Cancel`
- `ops_test.go` - OPS-02 query/mutation privacy tests, worker-viewer-necessity test, OPS-01 query proof
- `cancel_test.go` - D-43 idempotency, done-run no-op, and mid-step blocking-cancel tests
- `durablerun_test.go`, `cancelorderflowrun_test.go`, `selfloader_test.go`, `worker/claim_test.go`, `worker/conformance_test.go`, `worker/worker_test.go`, `worker/retry_test.go` - each now attaches `entflowfixture.AdminViewer()` to its base context, the direct consequence of `Policy()` existing at all

## Decisions Made

- **The compile-fail fixture proves an intra-module, not cross-module, boundary** — see key-decisions above. This is a deliberate reinterpretation of the plan's literal wording ("imports the internal package from a package path outside entflow's import tree"), grounded in the actual Go import-visibility rule: `internal/testdata/compilefail` is itself inside the module, so it legally *can* import `internal/wfmarker` — what it cannot do is reference the package's unexported key type, which is the property that actually matters for D-44.
- **Policy()'s mutation side gained a universal admin-allow rule and a Create-ownership check** beyond the plan's literal two-rule description — necessary so an admin viewer isn't accidentally denied on mutation shapes outside "Create" and "cancel-set Update", and so `entflow.Start` has a legitimate non-worker allow path at all.
- **Every pre-existing Phase 2 test touching `CancelOrderFlowRun` now carries an admin viewer** — a direct, in-scope Rule 1/2 fix, not new test surface, required to keep `go test ./... -count=1 -race` green after `Policy()` was added.
- **`Engine.Cancel` is a method, not a package-level generic function** like `Start` — it needs no second type parameter, so the erasure gymnastics `Start` requires don't apply.
- **The mid-step cancel test uses the file-backed, `_busy_timeout`-bearing SQLite setup**, not `entclient.New`'s shared-cache in-memory client, per plan 02-03's own documented SQLITE_LOCKED pitfall.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing Critical] Policy()'s mutation side needed a universal admin-allow rule**
- **Found during:** Task 2, while making `worker/conformance_test.go` and `worker/claim_test.go` (which perform plain `SetState(...).Save(ctx)` updates unrelated to cancelling) pass under an admin viewer
- **Issue:** The plan's mutation-side description names exactly two rules (marker-allow, cancel-state-guard). A plain admin-viewer Update that isn't a Create and doesn't set state to cancelled has no rule to Allow it and falls through to the final `AlwaysDenyRule` — denying even an admin, which defeats the point of an admin role.
- **Fix:** Added `allowAdminMutation` as Policy()'s second mutation rule (after the marker rule, before the create/cancel guard), and narrowed `guardRunMutation`'s own admin checks since the earlier rule already covers them.
- **Files modified:** internal/testdata/ent/schema/cancelorderflowrun.go
- **Verification:** full suite green, including the pre-existing conformance/claim tests that perform plain Updates under an admin viewer.
- **Committed in:** d281f22 (Task 2 commit)

**2. [Rule 1 - Bug] Every pre-existing Phase 2 test reaching CancelOrderFlowRun through the generated client broke once Policy() existed**
- **Found during:** Task 2, first full-suite run after regenerating with Policy()
- **Issue:** `durablerun_test.go`, `cancelorderflowrun_test.go`, `selfloader_test.go`, and all of `worker/claim_test.go`, `worker/conformance_test.go`, `worker/worker_test.go`, `worker/retry_test.go` called `client.CancelOrderFlowRun.*` (directly, via `entflow.Start`, or via `w.ClaimOnce`) with a bare `context.Background()` — no viewer, which the new Query/Mutation policy correctly denies.
- **Fix:** Each affected test's base `ctx := context.Background()` now attaches `entflowfixture.AdminViewer()` via `entflowfixture.WithViewer`; three files gained the `entflowfixture` import they didn't previously need.
- **Files modified:** durablerun_test.go, cancelorderflowrun_test.go, selfloader_test.go, worker/claim_test.go, worker/conformance_test.go, worker/worker_test.go, worker/retry_test.go
- **Verification:** `go test ./... -count=1 -race` green, including Postgres-backed conformance tests.
- **Committed in:** d281f22 (Task 2 commit)

---

**Total deviations:** 2 auto-fixed (1 Rule 2 — missing critical mutation-side coverage for admin viewers, 1 Rule 1 — pre-existing tests broken by adding Policy() at all). Both are direct, necessary consequences of this plan's own change; no scope creep, no architectural changes.

## Issues Encountered

- `go generate ./internal/testdata/ent/...` (run via `-mod=mod`) added several unrelated indirect checksums to `go.sum` (e.g. `olekukonko/tablewriter`, `spf13/cobra`) — build-time tooling noise from the generator invocation itself, not new runtime dependencies. `TestNoTransportDeps` and the standing `testcontainers`/`jackc/pgx`/`opentelemetry` zero-count gates all stayed green, confirming none of it entered the actual build graph.
- `gofmt -l` still flags `worker/conformance_test.go` — the same pre-existing, unrelated struct-literal alignment drift plan 02-04's summary already documented and left untouched; this plan's own edits to that file (an added import, two context-line changes) are gofmt-clean.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- The privilege boundary Phase 6's `DenyStatusEscalation` will build on is closed three phases early, and closed by the compiler/AST-scan rather than by review — `internal/wfmarker` and its exactly-one-setter guarantee are now load-bearing infrastructure for any future privileged-write mechanism.
- `CancelOrderFlowRun`'s `Policy()` is the first `Policy()` in the fixture; its shape (marker-allow, admin-allow, create/cancel-guard, final deny) is a template Phase 6's generated equivalent can follow, though the ownership-lookup mechanics (`runMut.Client()` + a privacy-bypassed existence query) are specific to this hand-written fixture and worth re-examining once codegen exists.
- `entflow.Engine.Cancel` and the OPS-01 query proof give an application everything a real "cancel this stuck run" incident needs today, with no dashboard or bespoke API.
- No blockers.

---
*Phase: 02-durability*
*Completed: 2026-08-15*

## Self-Check: PASSED

All created/modified files verified present on disk (internal/wfmarker/wfmarker.go, workflowmarker.go, internal/testdata/compilefail/wfmarker_internal.go, marker_test.go, internal/testdata/entflowfixture/viewer.go, ops_test.go, cancel_test.go, engine.go, worker/dbstep.go, worker/options.go, internal/testdata/ent/schema/cancelorderflowrun.go). All referenced commit hashes (9fb4203, d281f22, fe71e35) verified present in `git log --oneline --all`.
