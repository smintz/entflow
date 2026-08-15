---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
current_phase: 2
current_phase_name: Durability
status: planning
stopped_at: Phase 2 context gathered
last_updated: "2026-08-15T08:38:54.913Z"
last_activity: 2026-08-08
last_activity_desc: ROADMAP.md and STATE.md created; REQUIREMENTS.md traceability populated (56/56 mapped)
progress:
  total_phases: 2
  completed_phases: 1
  total_plans: 4
  completed_plans: 4
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-08-08)

**Core value:** A multi-step business process declared in the ent schema executes durably — surviving crashes at any point without duplicating external effects or committing a step without its progress record.
**Current focus:** Phase 01 — runtime-core

## Current Position

Phase: 2 — Durability
Plan: Not started
Status: Ready to plan
Last activity: 2026-08-08 — Phase 01 complete, transitioned to Phase 2

Progress: [██████████] 100%

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

Last session: 2026-08-15T08:38:54.896Z
Stopped at: Phase 2 context gathered
Resume file: .planning/phases/02-durability/02-CONTEXT.md
