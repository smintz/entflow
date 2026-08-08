---
phase: 01-runtime-core
plan: 04
subsystem: runtime
tags: [go, generics, ent, workflow, metadata, codegen-contract]

# Dependency graph
requires:
  - phase: 01-01
    provides: "Flow/FlowOf[In]/New[In]/FlowsOf builder surface, StepKind, step struct data/closure separation, TransitionsAnnotation"
  - phase: 01-02
    provides: "Codec[In]/WithCodec, Result[T], StepError — the flowConfig/FlowOption erasure pattern Meta()'s owner field reuses"
  - phase: 01-03
    provides: "The full DB-step constructor set, Activity/Emit (declarable-only), Condition/RetryPolicy data shapes Meta() projects from"
provides:
  - "github.com/smintz/entflow/meta — stdlib-only FlowMeta/StepMeta/ConditionMeta/RetryMeta contract (META-01)"
  - "(*entflow.FlowOf[In]).Meta() — an immutable, deep-copied snapshot of a flow's declared structure; entflow.WithOwner — the explicit owning-entity declaration"
  - "(*entflow.FlowOf[In]).Describe() — CORE-12's dry-run surface, rendering Meta() as stable, diffable data"
  - "entflow.Flow interface extended with Meta()/Describe(), so a heterogeneous []Flow from FlowsOf is directly useful to codegen and entconnect"
  - "A machine-checked proof (extractability_test.go) that CORE-11's extractability invariant holds: no meta type is function-typed, and FlowsOf/Meta/Describe invoke zero step closures — closes the [Phase 1 -> Phase 5] cross-phase blocker"
  - "testdata/callshapes.golden — D-20's committed snapshot of the builder-chain call shapes Phase 5's AST parser must handle, with a drift sub-test"
  - "deps_test.go — META-02's dependency gate (D-04), with an allowlist derived programmatically from entflow's own ent imports rather than hand-curated"
  - "transitions_graph_test.go — D-14's proof that the Transitions annotation round-trips off a loaded ent schema graph via ent's own Annotations[Name()] -> json.Marshal -> json.Unmarshal decode idiom"
affects: [phase-02-durability, phase-03-activities, phase-04-outbox, phase-05-codegen-injection, phase-06-codegen-validation]

actuals:
  tokens: 9984
  tasks: 3
  commits: 3

tech-stack:
  added: []
  patterns:
    - "meta.Kind duplicates entflow.StepKind's constant strings rather than aliasing it — an alias would make the meta package import entflow, inverting META-01's one-directional boundary. FlowOf[In].Meta() converts between the two via a same-underlying-type Go type conversion (meta.Kind(s.kind))."
    - "Meta() deep-copies every slice (Steps, DependsOn, Conditions) so a caller mutating a returned FlowMeta can never reach back into the live flow declaration — the mechanical form of D-19's 'immutable snapshot' requirement, proven by a test that mutates a returned value and re-reads Meta()."
    - "Describe() renders Meta(), never the internal step slice directly — the metadata path is the ONLY path, so a future divergence between what Meta() reports and what Describe() prints is impossible by construction, not just by convention."
    - "Golden-file tests share exactly one hand-rolled `-update` flag (declared once in describe_test.go, reused by callshapes_test.go) rather than each declaring its own — flag.Bool panics on a duplicate registration within the same package."
    - "META-02's dependency allowlist is computed by running `go list -deps` a second time against whatever entgo.io/ent packages actually appear in entflow's own dependency graph, rather than a literal hand-written package list — this is what keeps the gate meaningful across ent version bumps instead of being red on day one (01-RESEARCH.md's verified finding about ariga.io/atlas and its transitive tree)."
    - "entc.LoadGraph and entc/gen are imported from transitions_graph_test.go only — a test-only dependency that deps_test.go's own TestNoTransportDeps proves never enters the non-test build graph, preserving D-02's zero-codegen-dependency root package."

key-files:
  created:
    - meta/meta.go
    - describe.go
    - meta_test.go
    - describe_test.go
    - extractability_test.go
    - callshapes_test.go
    - deps_test.go
    - transitions_graph_test.go
    - testdata/describe.golden
    - testdata/callshapes.golden
  modified:
    - flow.go
    - internal/testdata/ent/schema/order_flows.go
    - go.mod

key-decisions:
  - "OutType is left empty in Phase 1 and documented as such in meta.FlowMeta's doc comment: no step kind in Phase 1 declares a flow-level output type, and inventing one now would fix a shape Phase 2's run-row response has not yet determined (plan's own explicit instruction, not a new decision)."
  - "Describe()'s output format (Claude's Discretion per 01-CONTEXT.md): a declaration-ordered, indented block per step with an explicit '(none)' marker for every optional fact a step did not declare (transition, emit topic, dependencies, conditions, retry) — so a diff shows a removed claim as a changed line rather than a shifted block, per PITFALLS.md's UX row."
  - "The call-shape golden file's rendering is derived from Meta() (data), not from re-parsing entflow.md's or order_flows.go's literal source text — Phase 1 has no AST extraction (D-20 reserves that for Phase 5), so the snapshot's job is to pin the DATA shape Phase 5's parser must reconstruct, not to prove Phase 1 can already parse Go source."
  - "The counting fixture flow in extractability_test.go is a test-local schema type (countingSchema) built with the same three constructors (UpdateSelf/Activity/Emit) the real fixture uses, rather than modifying internal/testdata/ent/schema/order_flows.go's actual closures to count invocations — keeps the production-shaped fixture's closures doing real work while still proving the invariant end-to-end through the real entflow.FlowsOf retrieval path."

patterns-established:
  - "stepMetaOf(s *step) meta.StepMeta as the single unexported projection function Meta() calls per step — the one place that must stay in sync if step.go ever adds a new declaration-data field."
  - "orNone/joinOrNone/conditionsOrNone/retryOrNone as Describe()'s small set of explicit-marker renderers, each handling exactly one optional-fact shape."

requirements-completed: [CORE-11, CORE-12, META-01, META-02, SM-01]

coverage:
  - id: D1
    description: "The builder chain records every codegen-needed fact as discrete data readable without evaluating any closure body (CORE-11, D-19)"
    requirement: "CORE-11"
    verification:
      - kind: unit
        ref: "extractability_test.go#TestNoClosureInMeta"
        status: pass
      - kind: unit
        ref: "extractability_test.go#TestClosuresNeverInvoked"
        status: pass
    human_judgment: false
  - id: D2
    description: "Describe() prints a flow's structure as data, covering step, kind, deps, transition claims, emit topics, and retry policy, and doubles as a dry-run"
    requirement: "CORE-12"
    verification:
      - kind: unit
        ref: "describe_test.go#TestDescribeMatchesGolden"
        status: pass
      - kind: unit
        ref: "describe_test.go#TestDescribeCoversEveryDeclaredFact"
        status: pass
      - kind: unit
        ref: "describe_test.go#TestDescribeIsStableAcrossCalls"
        status: pass
    human_judgment: false
  - id: D3
    description: "An external consumer can read flow metadata through a dedicated meta package that imports nothing but the standard library"
    requirement: "META-01"
    verification:
      - kind: unit
        ref: "meta_test.go#TestMetaPackageDependenciesAreStdlibOnly"
        status: pass
      - kind: unit
        ref: "meta_test.go#TestFlowMetaCoversDeclaredSteps"
        status: pass
      - kind: unit
        ref: "meta_test.go#TestMetaIsIndependentSnapshot"
        status: pass
    human_judgment: false
  - id: D4
    description: "entflow's non-test dependency graph contains only stdlib, its own module, and entgo.io/ent with its transitive requirements — verified by a test whose allowlist is derived programmatically"
    requirement: "META-02"
    verification:
      - kind: unit
        ref: "deps_test.go#TestNoTransportDeps"
        status: pass
      - kind: unit
        ref: "deps_test.go#TestDependenciesExcludeTestdataFixture"
        status: pass
    human_judgment: false
  - id: D5
    description: "The Transitions annotation declared on the fixture status enum is readable back off a loaded ent schema graph"
    requirement: "SM-01"
    verification:
      - kind: unit
        ref: "transitions_graph_test.go#TestTransitionsGraph"
        status: pass
    human_judgment: false
  - id: D6
    description: "The builder-chain call shapes Phase 5 must AST-parse are captured in a committed golden file that fails loudly if the surface drifts"
    verification:
      - kind: unit
        ref: "callshapes_test.go#TestCallShapesMatchesGolden"
        status: pass
      - kind: unit
        ref: "callshapes_test.go#TestCallShapesDetectsDrift"
        status: pass
    human_judgment: false
  - id: D7
    description: "go build ./..., go vet ./..., and go test ./... -race all exit 0 with GOFLAGS=-mod=readonly (matching CI), and the non-test dependency graph is unchanged from Plan 03 (stdlib + entgo.io/ent/schema only)"
    verification:
      - kind: unit
        ref: "GOFLAGS=-mod=readonly go build ./... && go vet ./... && go test ./... -race"
        status: pass
      - kind: unit
        ref: "go list -deps ./... | grep -v ^github.com/smintz/entflow (stdlib + entgo.io/ent/schema only, unchanged)"
        status: pass
    human_judgment: false

duration: 28min
completed: 2026-08-08
status: complete
---

# Phase 1 Plan 4: Metadata Contract and Invariants Summary

**A stdlib-only `meta` package, `Flow.Meta()`/`Describe()`, and four machine-checked invariants — extractability (structural + behavioural), a drift-detecting call-shape golden file for Phase 5, a programmatically-derived META-02 dependency gate, and the Transitions annotation's round-trip off a loaded ent graph — closing Phase 1.**

## Performance

- **Duration:** ~28 min (task execution only; excludes upfront context reading)
- **Tasks:** 3
- **Files modified:** 13 (10 created, 3 modified)
- **Commits:** 3

## Accomplishments

- Implemented `meta/meta.go`: `Kind`/`KindDB`/`KindActivity`/`KindEmit` (duplicated from `entflow.StepKind` rather than aliased, keeping the package's dependency direction one-way), and the four data-only structs `FlowMeta`/`StepMeta`/`ConditionMeta`/`RetryMeta` — every field a value type or a slice of value types, no field anywhere function-typed
- Added `entflow.WithOwner(name string) FlowOption` and wired it through `flowConfig`/`FlowOf[In]`; set it explicitly in the fixture's `Flows()` method (`WithOwner("Order")`) rather than inferring it, per ARCHITECTURE.md's "codegen must never infer facts by inspecting closure bodies" invariant
- Implemented `(*FlowOf[In]).Meta() meta.FlowMeta` in `flow.go`: derives `InType` via `reflect.TypeFor[In]().String()`, leaves `OutType` empty (documented: no Phase 1 step kind declares one), and projects every step through the new unexported `stepMetaOf` helper — deep-copying `DependsOn` and `Conditions` slices so a caller mutating a returned snapshot can never reach the live flow
- Extended the `Flow` interface with `Meta() meta.FlowMeta` and `Describe() string`, so `FlowsOf`'s heterogeneous `[]Flow` is directly useful to codegen/entconnect without a type assertion back to `*FlowOf[In]`
- Implemented `describe.go`'s `Describe()`: renders `Meta()` (never the internal steps) as a declaration-ordered, indented block per step, with an explicit `(none)` marker for every optional fact a step didn't declare — never iterates a map, so output is byte-identical across repeated calls
- Wrote `meta_test.go` and `describe_test.go` covering every Task 1 behavior: per-step data correctness for all three fixture steps (`cancel`/DB, `refund`/Activity, `order.cancelled`/Emit), the immutable-snapshot guarantee (mutate a returned `FlowMeta`'s slices, re-read `Meta()`, confirm no change), output stability across calls, golden-file coverage of every declared fact, and the `meta` package's own stdlib-only dependency set (`go list -deps github.com/smintz/entflow/meta`)
- Implemented `extractability_test.go`'s two D-19 mechanisms: (a) a reflective walk (`assertNoFuncField`) over all four `meta` types, recursing into struct fields, slice/array elements, and pointer elements with a visited-type set, proving no field is function-typed; (b) a test-local `countingSchema` built with the same `UpdateSelf`/`Activity`/`Emit` constructors as the real fixture, whose closures increment a shared `atomic.AddInt32` counter, proving `FlowsOf`, `Meta()`, and `Describe()` all leave that counter at exactly zero — closing STATE.md's `[Phase 1 → Phase 5]` cross-phase blocker
- Implemented `callshapes_test.go`'s D-20 snapshot: renders one flat line per constructor invocation (the flow constructor plus each step constructor) from `Meta()`'s data, with a fixed `<CLOSURE>` placeholder and each option constructor's literal arguments, committed to `testdata/callshapes.golden` with a header documenting it as a cross-phase contract and recording the documented finding that the generics-forced flat-call-sequence shape is easier for `go/ast` to parse than a fluent dot-chain would have been; a companion `TestCallShapesDetectsDrift` proves an altered flow's rendered shapes actually differ from the committed golden
- Implemented `deps_test.go`'s META-02 gate: computes the actual non-test dependency set via `go list -deps ./...`, derives the allowed set by running `go list -deps` a second time against whichever `entgo.io/ent` packages actually appear (never a hand-written literal list), classifies stdlib by the dot-in-first-segment rule, and asserts every remaining package is in the derived set; a companion test documents that no package path in the set contains a `testdata` segment
- Implemented `transitions_graph_test.go`'s D-14 proof: loads the fixture schema via `entc.LoadGraph` (test-file-only import), walks to `Order`'s `status` field, and round-trips its `Transitions` annotation through ent's own `Annotations[Name()] → json.Marshal → json.Unmarshal` idiom, asserting the decoded map exactly matches all four source states and their ordered target lists declared in `order.go`
- Ran `go mod tidy` after adding the test-only `entc`/`entc/gen` import, which pulled `golang.org/x/sync` and `golang.org/x/tools` into `go.mod` as indirect requirements — verified via `deps_test.go` itself, and via `GOFLAGS=-mod=readonly` (CI's exact setting), that the non-test build graph is completely unaffected

## Task Commits

Each task was committed atomically:

1. **Task 1: The meta package, Flow.Meta(), and Describe()** — `1a2b604` (feat)
2. **Task 2: Machine-check the extractability invariant and snapshot the builder-chain call shapes** — `81a0255` (test)
3. **Task 3: The META-02 dependency gate and the Transitions annotation round-trip** — `08b7eac` (test)

**Plan metadata:** pending (this commit)

## Files Created/Modified

- `meta/meta.go` — `Kind`, `KindDB`/`KindActivity`/`KindEmit`, `FlowMeta`, `StepMeta`, `ConditionMeta`, `RetryMeta`
- `flow.go` — `WithOwner`, `flowConfig.owner`/`FlowOf.owner`, `(*FlowOf[In]).Meta()`, `stepMetaOf`, `Flow` interface extended with `Meta()`/`Describe()`
- `describe.go` — `(*FlowOf[In]).Describe()`, `orNone`/`joinOrNone`/`conditionsOrNone`/`retryOrNone`
- `internal/testdata/ent/schema/order_flows.go` — `entflow.New[...]("CancelOrder", entflow.WithOwner("Order"))`
- `meta_test.go` — per-step `Meta()` coverage for all three fixture steps, the immutable-snapshot test, the `meta` package's stdlib-only dependency test; hosts `cancelOrderFlow(t)`, the shared fixture-retrieval helper `describe_test.go`/`callshapes_test.go` reuse
- `describe_test.go` — golden-file comparison, stability-across-calls, every-declared-fact coverage; hosts the package's single `-update` flag
- `extractability_test.go` — `TestNoClosureInMeta` (reflective walk), `countingSchema`, `TestClosuresNeverInvoked` (behavioural zero-invocation proof)
- `callshapes_test.go` — `renderCallShapes`/`callShapeOf`/`conditionConstructorOf`/`renderGolden`, `TestCallShapesMatchesGolden`, `TestCallShapesDetectsDrift`
- `deps_test.go` — `TestNoTransportDeps` (META-02 gate), `TestDependenciesExcludeTestdataFixture`, `isStdlib`, `runGoListDeps`
- `transitions_graph_test.go` — `TestTransitionsGraph`
- `testdata/describe.golden`, `testdata/callshapes.golden` — committed golden snapshots
- `go.mod` — `golang.org/x/sync`, `golang.org/x/tools` added as indirect (test-only, via `entc.LoadGraph`)

## Decisions Made

See `key-decisions` in the frontmatter. All four were within the plan's own "Claude's Discretion" latitude (Describe()'s exact formatting, OutType's Phase-1 emptiness) or direct, undisputed implementations of the plan's own explicit instructions (call-shape rendering derived from Meta() data, the counting fixture as a test-local schema rather than a modified production fixture).

## Deviations from Plan

None — plan executed exactly as written. Every file in the plan's `files_modified` list was created or modified as specified; no Rule 1-4 deviations were needed.

## Issues Encountered

- `TestMetaPackageDependenciesAreStdlibOnly`'s first draft failed because `go list -deps github.com/smintz/entflow/meta` includes the `meta` package itself in its output (not just its dependencies) — a one-line fix to skip that self-entry before the stdlib classification loop.
- Adding `entc.LoadGraph` to `transitions_graph_test.go` required `go mod tidy` to resolve `golang.org/x/tools`/`golang.org/x/sync` as new indirect requirements; verified both `go vet ./...` and `GOFLAGS=-mod=readonly` (CI's exact setting) succeed afterward, and that `deps_test.go`'s own gate (added in the same task) confirms neither package enters the non-test build graph.

## User Setup Required

None — no external service configuration required.

## Next Phase Readiness

- Phase 1 (Runtime Core) is complete: the full builder API (flow/step declaration, DB-only execution, DI registry, codec, retry policies, ordering/conditions), the metadata contract (`meta` package, `Meta()`, `Describe()`), and all four Phase-1-owned machine-checked invariants (extractability, call-shape snapshot, META-02 dependency gate, Transitions annotation readability) are in place and green.
- `testdata/callshapes.golden` is the concrete contract Phase 5's entity-injection/AST-parsing spike must match — any change to the builder-chain call shapes going forward is a change to a committed, drift-detecting file, not a silent surface change discovered months later.
- `entc.LoadGraph` and ent's `Annotations[Name()] → json.Marshal → json.Unmarshal` decode idiom are now proven and captured in `transitions_graph_test.go`; Phase 6's cross-validator can reuse this pattern directly instead of rediscovering it.
- The `meta` package's stdlib-only dependency posture and the root package's `ent`-only posture are both now enforced by tests, not conventions — any future change that violates either fails `go test ./...` immediately.
- No blockers carried forward into Phase 2.

---
*Phase: 01-runtime-core*
*Completed: 2026-08-08*

## Self-Check: PASSED

All created/modified files verified present on disk (meta/meta.go, describe.go, meta_test.go, describe_test.go, extractability_test.go, callshapes_test.go, deps_test.go, transitions_graph_test.go, testdata/describe.golden, testdata/callshapes.golden, flow.go, internal/testdata/ent/schema/order_flows.go, go.mod, this SUMMARY.md). All three task commits (`1a2b604`, `81a0255`, `08b7eac`) verified present in `git log --oneline --all`. `go build ./...`, `go vet ./...`, and `go test ./... -race` all exit 0 on the final tree, including under `GOFLAGS=-mod=readonly` (CI's exact setting).
