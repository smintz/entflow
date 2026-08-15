---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
current_phase: 02
current_phase_name: Durability
status: executing
stopped_at: Completed 02-07-PLAN.md
last_updated: "2026-08-15T14:23:53.883Z"
last_activity: 2026-08-15
last_activity_desc: Phase 02 execution started
progress:
  total_phases: 2
  completed_phases: 1
  total_plans: 12
  completed_plans: 11
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-08-08)

**Core value:** A multi-step business process declared in the ent schema executes durably — surviving crashes at any point without duplicating external effects or committing a step without its progress record.
**Current focus:** Phase 02 — Durability

## Current Position

Phase: 02 (Durability) — EXECUTING
Plan: 8 of 8
Status: Ready to execute
Last activity: 2026-08-15 — Phase 02 execution resumed (wave continue)

Progress: [█████████░] 92%

## Performance Metrics

**Velocity:**

- Total plans completed: 4
- Average duration: N/A
- Total execution time: 0 hours

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| 01 | 4 | - | - |

**Recent Trend:**

- Last 5 plans: N/A
- Trend: N/A

*Updated after each plan completion*
**Per-Plan Metrics:**

| Plan | Duration | Tasks | Files |
|------|----------|-------|-------|
| Phase 01 P01 | 12min | 2 tasks | 30 files |
| Phase 01 P02 | 7min | 3 tasks | 8 files |
| Phase 01 P03 | 34min | 3 tasks | 13 files |
| Phase 01 P04 | 28min | 3 tasks | 13 files |
| Phase 02-durability P01 | 18min | 2 tasks | 29 files |
| Phase 02 P02 | 55min | 3 tasks | 15 files |
| Phase 02-durability P03 | 30min | 3 tasks | 9 files |
| Phase 02-durability P04 | 22min | 3 tasks | 13 files |
| Phase 02-durability P05 | 50min | 3 tasks | 25 files |
| Phase 02-durability P06 | 20min | 3 tasks | 11 files |
| Phase 02-durability P07 | 35min | 3 tasks | 9 files |

## Accumulated Context

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- [Roadmap]: Runtime-first, codegen-last ordering (Phases 1-4 durable/hand-written, Phases 5-6 codegen) — every capability through the outbox milestone is expressible by hand, codegen is additive automation only.
- [Roadmap]: Codegen milestone split into two phases (5: entity-injection spike, 6: cross-validation + runner generation) to isolate the single highest-risk, least-precedented mechanism (schemast-based run-entity injection with a self-referential transitions annotation) from the lower-risk template/validation work around it.
- [Roadmap]: Crash-simulation harness is a living artifact — born in Phase 2 (Durability) as a release gate, extended in Phase 3 (activity beats) and Phase 4 (relay delivery), not a single late deliverable.
- [Phase ?]: Task 1 auto-selected A1-N1: UpdateSelf(f, name, closure, opts...) with closure third, variadic StepOption last; Flow stays the schema-facing interface name, generic builder is FlowOf[In].
- [Phase ?]: Added a genuine no-op passthrough Hook to the fixture Order schema to force ent's codegen to route schema-stitching through the separate ent/runtime package, breaking a real import cycle between schema and ent that Phase 1 research had assumed would not occur.
- [Phase ?]: Plan 01-02: codec_test.go and result_test.go use internal package entflow (not entflow_test) to exercise unexported seams (flowConfig, codecOf, withResults/putResult) that Plan 03's executor also needs.
- [Phase ?]: Plan 01-03: Check's closure returns only an error; its adapter records a nil result so the result-recording path stays uniform across all seven DB-step constructors.
- [Phase ?]: Plan 01-03: WithSelfStatus is an explicit declared FlowOption (mirroring WithCodec's type-erasure pattern), never inferred from a flow's *Self steps, per the 'codegen must never infer facts by inspecting closure bodies' invariant.
- [Phase ?]: Plan 01-03: Task 2's fixture and behavior spec required a minimal slice of Task 3's stepoptions.go (Condition/SelfWas/When/After) plus a durable-run refusal check pulled forward into exec.go, to keep every task's commit independently buildable and green (Rule 3 deviation).
- [Phase ?]: Plan 01-04: meta.Kind duplicates entflow.StepKind's constant strings rather than aliasing it, keeping the meta package's dependency direction strictly one-way (META-01).
- [Phase ?]: Plan 01-04: Meta() deep-copies every slice (Steps, DependsOn, Conditions) so a caller mutating a returned FlowMeta can never reach the live flow declaration (D-19 immutable snapshot).
- [Phase ?]: Plan 01-04: META-02's dependency allowlist is derived by running go list -deps against entflow's own entgo.io/ent imports at test time, never hand-curated, so the gate stays meaningful across ent version bumps.
- [Phase ?]: Plan 01-04: OutType left empty in Phase 1 (documented) — no step kind declares a flow-level output type yet; testdata/callshapes.golden pins the builder-chain call shapes Phase 5's AST parser must handle.
- [Phase ?]: [Phase 02-01]: D-23 + D-27 ratified exactly as CONTEXT.md specifies (auto-selected under auto mode, checkpoint gate=blocking not blocking-human) — RunMixin's column set and the colon-bearing failed:<step> enum are now the persisted contract Phase 5 codegen must reproduce.
- [Phase ?]: [Phase 02-01]: Closed RESEARCH.md Assumption A1 — sql/execquery's ExecContext/QueryContext land on *config/*txDriver (promoted onto *Client/*Tx via embedding) on the pinned ent v0.14.6 tag, not directly on *Client/*Tx as master-branch research suggested; signatures match exactly, no adaptation needed in later claim code.
- [Phase ?]: [Phase 02-01]: Phase 2 ships no MySQL-specific partial-index variant (RESEARCH.md open question 2) — the D-34 index is documented Postgres/SQLite-only in run.go.
- [Phase ?]: D-30 ratified exactly as specified (auto-selected under auto mode, checkpoint gate=blocking not blocking-human) — the claim transaction IS the step transaction, proven by TestClaimRollbackLeavesRunClaimable's paired absence claim.
- [Phase ?]: D-25 deviation documented on entflow.Start's own doc comment: DUR-01's literal flow.Start(ctx, in) is unreachable in Phase 2 without an ambient global D-15 rejects — Start is a package-level generic function; Phase 6 codegen restores the design-doc call shape.
- [Phase ?]: Runner.StepOrder/EntrySelfStatus scope to DB-kind steps only, not the full step graph — CancelOrder legitimately declares an Activity/Emit step alongside its one DB step, and durable execution in Phase 2 must not demand a WithSelfStatus reader for a condition on a step that will never run.
- [Phase ?]: worker/options.go's TracerProvider field stays typed any (not the real otel trace.TracerProvider) until D-57's dependency exception to the META-02 test lands in a later plan — this plan adds no new module and TestNoTransportDeps is the standing gate.
- [Phase ?]: [Phase 02-03]: D-38 ratified — one table-driven conformance suite proves no-double-claim and rolled-back-claim-stays-claimable on Postgres and SQLite for real (internal/testdata/pgtest), MySQL an explicit named skip, not a silently absent row.
- [Phase ?]: [Phase 02-03]: SQLite concurrency testing must use a file-backed (not shared-cache in-memory) client with both _txlock=immediate and _busy_timeout — shared-cache mode's SQLITE_LOCKED error path is not covered by modernc.org/sqlite's busy-handler retry the way ordinary file-lock SQLITE_BUSY is.
- [Phase ?]: [Phase 02-03]: Worker.Run is now a pool of Options.Concurrency independent claim goroutines (D-31), each with its own claimOnceRecovered recover boundary separate from runStep's per-closure recover — a bug in worker bookkeeping itself can no longer crash the whole process.
- [Phase ?]: [Phase 02-04]: D-40 ratified as a deliberate behavior change — Result[T] now round-trips every path through JSON (map[string]json.RawMessage), including a single uninterrupted worker pass; TestResultTypedHit moved from require.Same to require.Equal+require.NotSame.
- [Phase ?]: [Phase 02-04]: Self[T]/WithSelfLoader added as the third WithSelfStatus/WithOwnerRef erasure instance (D-41) — a live per-claim re-read via Runner.LoadSelf/StepCall.Self, deliberately asymmetric with Result[T] (inert) and a SelfWas condition (entry snapshot, D-10/D-42).
- [Phase ?]: [Phase 02-04]: A step-closure error is now always a claimed=true outcome from ClaimOnce once RunStore.Fail's guard matches (retry-scheduled or failed:<step>) — only a guard miss or write error surfaces as a Go error; classifyRetryable (worker/retry.go) uses a locally-declared sqlStater interface plus driver.ErrBadConn plus Options.RetryableError, importing no database driver.
- [Phase ?]: [Phase 02-05]: D-44's compile-fail fixture proves the marker's context-key type is unforgeable even intra-module (unexported ctxKey, real isolated go build failure), not Go's cross-module internal-package rule — internal/testdata/compilefail is itself inside the entflow module, so that import legally compiles; the AST scan (TestWorkflowMarkerHasExactlyOneSetter) is what confines the setter to worker/dbstep.go.
- [Phase ?]: [Phase 02-05]: CancelOrderFlowRun's Policy() mutation side gained a universal admin-allow rule and a Create-ownership check beyond the plan's literal marker+cancel-guard description — required so an admin viewer isn't denied on mutation shapes outside Create/cancel-set-Update, and so entflow.Start has a legitimate non-worker allow path at all.
- [Phase ?]: [Phase 02-05]: worker/dbstep.go applies Options.Context before wfmarker.Set (D-45) — the application hook can never observe or forge the marker; a worker with no hook configured is honestly denied by a privacy-governed run entity, not silently granted access.
- [Phase ?]: [Phase 02-05]: entflow.Engine.Cancel is a method, not a package-level generic function like Start — it needs no second type parameter, so Start's erasure gymnastics don't apply; it runs under the caller's own context/viewer, never the worker's.
- [Phase ?]: [Phase 02-06]: D-57 landed — trace API only, defaults to trace/noop, never the ambient global; measured footprint at pinned v1.45.0 corrects RESEARCH.md Assumption A2 (attribute's xxhash wrapper pulls the real external cespare/xxhash/v2 module, not a vendored copy).
- [Phase ?]: [Phase 02-06]: D-58 resolved — a run's root span is started and ended within the single claim that first seeds trace_context, never held open across a claim boundary; later claims restore it as a remote parent for that claim's own child span.
- [Phase ?]: [Phase 02-06]: D-59 resolved — trace_context stores <traceID>-<spanID>-<flags>; the flags byte is load-bearing (a real SDK's ParentBased sampler silently drops unsampled-flagged restored spans), and Fail (not just Advance) now persists it so a retried first step doesn't fork a new trace.
- [Phase ?]: [Phase 02-06]: deps_test.go's derived-allowlist technique extended to tracing — TestNoTransportDeps unions the ent-derived and tracing-derived closures; TestTracingDependencyNarrowness fails the graph on any of the five ambient-global modules or the SDK itself.
- [Phase ?]: [Phase 02-07]: Claim-lifecycle counting (Shutdown's drain wait) is a mutex + sync.Cond, not a sync.WaitGroup — a real -race-detected bug (Add racing Wait once the counter touched zero), fixed by switching primitives.
- [Phase ?]: [Phase 02-07]: stopCh (channel, wakes selects promptly) and the mu-guarded stopping flag (atomic new-claim gate via beginClaim) are two separate mechanisms, both kept — merging them was the first design tried and is what produced the WaitGroup race.
- [Phase ?]: [Phase 02-07]: topology_test.go proves cross-process no-duplicated-effect via CancelOrder's own single step rather than a dedicated counter — the effect counter is explicitly plan 02-08 Task 1's own decision (an integer column on Order) to make.
- [Phase ?]: [Phase 02-07]: internal/testdata/pgtest.StartNWithDSN added beyond the plan's literal file list (Rule 2) — a real OS subprocess needs the raw DSN to reach the same isolated schema; StartN/Start now delegate to it, unaffected for existing callers.
- [Phase ?]: [Phase 02-07]: Finding required by the plan's output instruction — graceful shutdown needed nothing crash-resume did not already provide; a cancelled context routes an in-flight claim into claimOnce's pre-existing deferred-rollback path (D-30).

### Pending Todos

[From .planning/todos/pending/ — ideas captured during sessions]

None yet.

### Blockers/Concerns

[Issues that affect future work]

- [Phase 3]: Activity lease/heartbeat mechanism (duration, reclaim predicate, interaction with claim query) is unspecified in the source design doc — needs its own design pass during Phase 3 planning, not assumed to fall out of existing decisions.
- [Phase 1 → Phase 5]: AST-extractability of the `Flows()` builder chain is a design intent, not yet a proven mechanism — Phase 1 should include a unit test asserting builder data-producing methods never require evaluating the closure, since Phase 5's entity-injection spike depends on this holding.
- [Phase 2]: Dialect support matrix (Postgres-first, MySQL/SQLite status) is a research recommendation, not yet a ratified project decision — must be explicitly decided and documented during Phase 2 planning, before the claim query is finalized.
- [Phase 6]: The unclaimed-transition suppression-annotation shape is a one-way door (breaking change to alter after user adoption) — needs a focused design pass before implementation, not just before release.

## Deferred Items

Items acknowledged and carried forward from previous milestone close:

| Category | Item | Status | Deferred At |
|----------|------|--------|-------------|
| *(none — first milestone)* | | | |

## Session Continuity

Last session: 2026-08-15T14:23:53.861Z
Stopped at: Completed 02-07-PLAN.md
Resume file: None
