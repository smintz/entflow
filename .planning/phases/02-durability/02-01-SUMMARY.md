---
phase: 02-durability
plan: 01
subsystem: database
tags: [ent, entc, sql-execquery, postgres, sqlite, mixin, codegen]

# Dependency graph
requires:
  - phase: 01-runtime-core
    provides: "Flow/FlowOf[In]/meta.FlowMeta, the WithSelfStatus type-erasure pattern, the CancelOrder fixture flow, and META-02's dependency gate machinery this plan reuses verbatim"
provides:
  - "sql/execquery-enabled fixture ent client: ExecContext/QueryContext on *ent.Client and *ent.Tx (via promoted *config/*txDriver methods), proven to share one open transaction with the generated builders"
  - "entflow.RunMixin(f) — the run row's framework-owned column set and step-graph-derived state enum, ready for any concrete run schema to embed"
  - "entflow.WithOwnerRef — the declared lineage-edge FlowOption, same type-erasure/panic pattern as WithSelfStatus"
  - "entflow.Run / RunTable / RawQuerier — the transport-free vocabulary plan 02-02's claim query is written against"
  - "A real, migrated cancel_order_flow_runs table with a working owner edge to orders"
affects: [02-02, 02-03, 02-04, 02-05, 02-06, 02-07, 02-08]

# Actuals (#2632)
actuals:
  tokens: 59886
  tasks: 2
  commits: 2

tech-stack:
  added: []
  patterns:
    - "sql/execquery feature flag on the fixture ent generate directive, unlocking ExecContext/QueryContext for the worker's future claim query — the only sanctioned raw-SQL escape hatch, scoped narrowly per T-02-02"
    - "RunMixin(f Flow) ent.Mixin: a mixin whose Fields()/Indexes() are derived from a flow's live meta.FlowMeta.Steps at schema-load time, not a hand-copied literal list"
    - "WithOwnerRef / WithSelfStatus type-erasure + New-time mismatch panic (WR-04) as the reusable pattern for every future declared FlowOption"

key-files:
  created:
    - run.go
    - ownerref.go
    - run_test.go
    - execquery_test.go
    - cancelorderflowrun_test.go
    - internal/testdata/ent/schema/cancelorderflowrun.go
  modified:
    - flow.go
    - internal/testdata/ent/generate.go
    - internal/testdata/ent/client.go
    - internal/testdata/ent/tx.go
    - internal/testdata/ent/schema/order.go
    - internal/testdata/ent/schema/order_flows.go

key-decisions:
  - "D-23 + D-27 ratified exactly as CONTEXT.md's §3.5 specifies (auto-selected 'ratify-as-specified' under auto mode; gate=blocking, not blocking-human) — the RunMixin column set and the failed:<step> colon-form enum are now the persisted contract Phase 5 codegen must reproduce."
  - "Closed RESEARCH.md's Assumption A1: on the pinned ent v0.14.6 tag, sql/execquery's ExecContext/QueryContext are generated on *config (embedded in *Client) and *txDriver (embedded via config.driver in *Tx), not directly on *Client/*Tx as the master-branch research read suggested. Signatures are identical; both *ent.Client and *ent.Tx satisfy a RawQuerier-shaped interface through Go's method promotion, so every later plan's claim code written against 'tx.QueryContext(...)' works unchanged."
  - "Phase 2 ships no MySQL-specific partial-index variant (RESEARCH.md open question 2, resolved in run.go's Indexes() doc comment): the D-34 index is Postgres/SQLite-only, documented rather than silently omitted."

requirements-completed: [DUR-02]

coverage:
  - id: D1
    description: "sql/execquery regenerates the fixture ent client and proves the claim query's future transaction-sharing mechanism (D-30) works on the pinned ent version"
    requirement: DUR-02
    verification:
      - kind: unit
        ref: "execquery_test.go#TestExecQuerySharesTheEntTransaction"
        status: pass
    human_judgment: false
  - id: D2
    description: "entflow.RunMixin derives the run row's state enum and framework-owned columns from a flow's declared step graph"
    requirement: DUR-02
    verification:
      - kind: unit
        ref: "run_test.go#TestRunStatesDerivedFromStepGraph"
        status: pass
      - kind: unit
        ref: "run_test.go#TestRunMixinBuildsFieldsAndIndexes"
        status: pass
    human_judgment: false
  - id: D3
    description: "entflow.WithOwnerRef gets the same declaration-time mismatch protection as WithSelfStatus (WR-04)"
    verification:
      - kind: unit
        ref: "run_test.go#TestOwnerRefMismatchedInputTypePanicsFromNew"
        status: pass
    human_judgment: false
  - id: D4
    description: "A CancelOrderFlowRun row persists through real generated codegen with state=pending, serialized input, and a traversable owner edge to Order"
    requirement: DUR-02
    verification:
      - kind: unit
        ref: "cancelorderflowrun_test.go#TestCancelOrderFlowRunCreatesWithOwnerEdge"
        status: pass
    human_judgment: false

duration: 18min
completed: 2026-08-15
status: complete
---

# Phase 02 Plan 01: sql/execquery + entflow.RunMixin Summary

**Regenerated the fixture ent client with `sql/execquery` (proving raw SQL shares the transaction ent's generated builders use) and shipped `entflow.RunMixin`/`WithOwnerRef`, landing a real, migrated `cancel_order_flow_runs` table with a step-graph-derived `failed:<step>` state enum and a working owner edge to `orders`.**

## Performance

- **Duration:** 18 min
- **Started:** 2026-08-15T11:24:00Z
- **Completed:** 2026-08-15T11:42:02Z
- **Tasks:** 2 (auto) + 1 checkpoint:decision (auto-resolved)
- **Files modified:** 29 (6 hand-authored source/test files created, 6 hand-authored files modified, 17 ent-generated files created/regenerated)

## Accomplishments

- `sql/execquery` feature flag enabled on the fixture ent codegen directive; `TestExecQuerySharesTheEntTransaction` proves a `tx.QueryContext` SELECT sees a row created through `tx.Order.Create()` before commit, while the identical SELECT through the top-level `*ent.Client` cannot observe it at all until the transaction resolves — D-30's single-transaction claim is now mechanically proven, not assumed.
- `entflow.RunMixin(f Flow) ent.Mixin` ships the full D-28 framework-owned column set plus the D-42 `self_was` snapshot column, with the `state` enum's Go identifiers and stored values (including the literal `failed:<step>` colon form, D-27) derived live from `f.Meta().Steps` — verified against the real generated `cancelorderflowrun` package (`StateFailedCancel = "failed:cancel"`, `StateFailedRefund = "failed:refund"`, `StateFailedOrderCancelled = "failed:order.cancelled"`).
- `entflow.WithOwnerRef[In, ID]` declares the D-26 lineage edge with the same type-erasure and New-time mismatch panic `WithSelfStatus` already has (WR-04).
- `internal/testdata/ent/schema/cancelorderflowrun.go`: the hand-written `CancelOrderFlowRun` entity, `Mixin()` genuinely deriving from `Order{}.Flows()[0]`'s live step graph; `Order` gained the inverse `runs` edge; `order_flows.go`'s `CancelOrder` flow gained `WithOwnerRef` resolving `CancelOrderRequest.OrderID`.
- A `CancelOrderFlowRun` row creates, persists, and reads back with a traversable `owner` edge through real generated codegen against SQLite.

## Task Commits

1. **Task 1: Enable sql/execquery and prove raw SQL runs on the same transaction the generated client uses** - `64fba27` (feat)
2. **Task 2: Ship entflow.RunMixin, entflow.WithOwnerRef, and the hand-written CancelOrderFlowRun entity** - `cbf85a1` (feat)

**Checkpoint:** D-23 + D-27 ratification — auto-selected "ratify-as-specified" (auto mode active, gate=blocking not blocking-human per checkpoint protocol).

_Note: no separate plan-metadata commit yet — STATE.md/ROADMAP.md/REQUIREMENTS.md updates land in the final commit below._

## Files Created/Modified

- `run.go` - `entflow.RunMixin`, `RunStates`/`RunStateFailed`, `Run`/`RunTable`/`RawQuerier`, the `goIdent` step-name-to-Go-identifier helper
- `ownerref.go` - `entflow.WithOwnerRef[In, ID]`
- `flow.go` - `flowConfig`/`FlowOf[In]` gain `ownerRef`/`ownerRefInType`, `New[In]` gains the WithOwnerRef mismatch panic
- `execquery_test.go` - proves D-30's single-transaction claim mechanically
- `run_test.go` - RunStates/RunStateFailed/goIdent/RunMixin/WithOwnerRef unit coverage (package `entflow`, unexported-seam access)
- `cancelorderflowrun_test.go` - real-codegen creation/read-back proof (package `entflow_test`)
- `internal/testdata/ent/generate.go` - `--feature sql/execquery` added to the go:generate directive
- `internal/testdata/ent/schema/cancelorderflowrun.go` - the hand-written run entity
- `internal/testdata/ent/schema/order.go` - `runs` inverse edge added
- `internal/testdata/ent/schema/order_flows.go` - `WithOwnerRef` added to the `CancelOrder` flow declaration
- `internal/testdata/ent/client.go`, `internal/testdata/ent/tx.go`, and the rest of `internal/testdata/ent/` - regenerated codegen output (ExecContext/QueryContext, the new `CancelOrderFlowRun` client/builders/predicates, migration schema)

## Decisions Made

- **D-23 + D-27 ratified exactly as specified.** Auto-selected under auto mode since the checkpoint's `gate="blocking"` (not `blocking-human`) — see `<auto_mode_detection>`/checkpoint protocol. The mixin column set and the colon-bearing `failed:<step>` enum are now the shape Phase 5 codegen is contractually obliged to reproduce.
- **RESEARCH.md Assumption A1 closed, functionally unaffected.** The generated `ExecContext`/`QueryContext` land on `*config`/`*txDriver` (promoted onto `*Client`/`*Tx` via Go embedding) rather than directly on `*Client`/`*Tx` as the master-branch research read suggested. Verified via a throwaway compile check (`var _ rawQuerier = (*ent.Client)(nil)`, `(*ent.Tx)(nil)`) that both types satisfy the exact signature `entflow.RawQuerier` requires — no adaptation needed in this or any later plan's claim code.
- **Phase 2 ships no MySQL-specific partial-index variant** (RESEARCH.md open question 2). Documented directly in `run.go`'s `Indexes()` doc comment rather than left implied.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed a real deadlock in my own execquery_test.go, not a plan defect**
- **Found during:** Task 1 verification
- **Issue:** The first draft of `TestExecQuerySharesTheEntTransaction` issued a synchronous `client.QueryContext` read (outside the open transaction) against the fixture's shared-cache SQLite DSN. `modernc.org/sqlite`'s `sqlite3_unlock_notify` blocks that read on a mutex until the transaction holding the table lock resolves — the test hung indefinitely (confirmed via `go test -timeout=30s` producing a goroutine dump pointing at `modernc.org/sqlite.(*conn).retry`).
- **Fix:** Restructured the test to run the outside read in a goroutine, assert (via a bounded `select`/`time.After`) that it stays blocked for 200ms — itself the isolation proof, since it cannot observe committed-or-uncommitted state until the transaction resolves — then `Rollback()` and assert the goroutine unblocks with count 0. `readCount` was factored out with no `*testing.T` dependency so it is safe to call off the test goroutine (testify's `FailNow`-based assertions must run on the test's own goroutine).
- **Files modified:** execquery_test.go
- **Verification:** `go test . -run TestExecQuerySharesTheEntTransaction -count=1 -timeout=30s` passes in ~0.2s.
- **Committed in:** 64fba27 (Task 1 commit)

**2. [Rule 1 - Bug] Reworded two `run.go` doc comments that defeated the plan's own literal acceptance-criteria grep**
- **Found during:** Task 2 acceptance-criteria verification
- **Issue:** `grep -c 'Edges()' run.go` was expected to be 0 (proving the mixin never declares an edges method), but two doc comments used the literal English phrase "Edges()" when describing what `runMixin` does *not* declare, inflating the count to 2 despite no such method existing.
- **Fix:** Reworded both comments ("its edges, hooks, interceptors..." / "never declares an edges method") to convey the same fact without the literal substring, so the mechanical check matches the actual code.
- **Files modified:** run.go
- **Verification:** `grep -c 'Edges()' run.go` now returns 0; `TestRunMixinBuildsFieldsAndIndexes` already asserted `m.Edges()` is empty independent of this grep.
- **Committed in:** cbf85a1 (Task 2 commit)

**3. [Rule 1 - Bug] Renamed a test so it matches the plan's own `<verify>` -run pattern**
- **Found during:** Task 2 acceptance-criteria verification
- **Issue:** The WithOwnerRef mismatch-panic test was originally named `TestWithOwnerRefMismatchedInputTypePanicsFromNew`, which does not contain the literal substring `TestOwnerRef` the plan's `<verify>` command filters on (`-run 'TestRun|TestOwnerRef|TestCancelOrderFlowRun'`) — it would have silently been excluded from that specific verification run (though still covered by the full suite).
- **Fix:** Renamed to `TestOwnerRefMismatchedInputTypePanicsFromNew`.
- **Files modified:** run_test.go
- **Verification:** `go test . -run 'TestRun|TestOwnerRef|TestCancelOrderFlowRun' -v -count=1` now lists and passes it explicitly.
- **Committed in:** cbf85a1 (Task 2 commit)

---

**Total deviations:** 3 auto-fixed (all Rule 1 — bugs found in my own test authoring/verification-matching, not in the plan's design). No scope creep; no architectural changes.

## Issues Encountered

- SQLite's shared-cache cross-connection locking (`sqlite3_unlock_notify`) blocks rather than immediately erroring when a connection outside an open write transaction tries to read a locked table — a real, useful data point for plan 02-02's claim-loop design (RESEARCH.md's Pattern 1 already anticipated `_txlock=immediate` for SQLite's claim strategy; this plan's test is the first concrete evidence of the blocking behavior under the fixture's exact DSN).

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- The `sql/execquery`-enabled fixture client, `entflow.RunMixin`, `WithOwnerRef`, and a real migrated `cancel_order_flow_runs` table are all in place — plan 02-02's claim-loop tracer can now build directly on `entflow.Run`/`RunTable`/`RawQuerier` and the `RunStates`-derived enum with no further schema work.
- No blockers. The one open follow-up (documented, not deferred) is that plan 02-02 owns writing `docs/dialects.md`, which this plan's `run.go` doc comments already anticipate by name.

---
*Phase: 02-durability*
*Completed: 2026-08-15*
