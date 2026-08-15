---
phase: 02-durability
plan: 06
subsystem: observability
tags: [opentelemetry, tracing, spans, dependency-gate, worker]

# Dependency graph
requires:
  - phase: 02-durability
    provides: "plan 02-01's trace_context column on RunMixin (never wired), plan 02-02's worker/dbstep.go claim-execute-advance transaction and the Options.TracerProvider field deliberately typed any pending this plan, plan 02-05's privacy-governed claim path this plan's spans run inside"
provides:
  - "worker/span.go — the D-58 span naming/attribute convention (SpanName/RootSpanName/StepAttributes/RunAttributes) and the D-59 trace-context encode/decode/restore pair (EncodeTraceContext/DecodeTraceContext/WithRestoredParent), plus DefaultTracerProvider/ResolveTracerProvider"
  - "worker/dbstep.go's claimTelemetry — root-span-once-per-run, child-span-per-executed-step wiring into claimOnce and failStep"
  - "worker.Options.TracerProvider narrowed from any to the real trace.TracerProvider (D-57 landed)"
  - "the D-57 dependency decision itself: entflow.md's OTel exception is now real code, measured against the pinned tag, and machine-enforced by deps_test.go rather than assumed"
affects: [02-07, 02-08]

# Actuals (#2632)
actuals:
  tokens: 14500
  tasks: 3
  commits: 3

tech-stack:
  added:
    - "go.opentelemetry.io/otel/trace v1.45.0 (API only, direct) — entflow's one sanctioned dependency beyond ent (D-57)"
    - "go.opentelemetry.io/otel/sdk v1.45.0 (test-only, via observability_test.go's in-memory span recorder — never in the non-test build graph)"
  patterns:
    - "worker/span.go centralizes the span naming/attribute convention (SpanName/StepAttributes) so no call site in worker/dbstep.go can drift from it — mirrors codec.go's 'one place owns the format' discipline."
    - "claimTelemetry (worker/dbstep.go): a run's root span is started AND ended within the single claim that first seeds trace_context — never held open across a claim boundary, because a span cannot survive a process boundary. Every later claim restores the persisted trace/span/flags as a remote parent instead."
    - "firstClaim is keyed off run.TraceContext == \"\", not run.CurrentStep == \"\" — a retried first DB step (D-51) would otherwise fork a new root span, and a new trace, on every retry attempt."
    - "trace_context's stored format is <32-hex-trace-id>-<16-hex-span-id>-<2-hex-flags> — the flags byte is load-bearing, not cosmetic (see Deviations)."
    - "deps_test.go's derived-allowlist technique (established for ent in Phase 1) now also derives its tracing allowance from whatever go.opentelemetry.io/otel-prefixed packages entflow's own code actually imports, unioned with the ent-derived set — never a hand-picked prefix exemption."

key-files:
  created:
    - worker/span.go
    - worker/span_test.go
    - observability_test.go
  modified:
    - worker/dbstep.go
    - worker/options.go
    - worker/worker.go
    - runstore.go
    - internal/testdata/entflowfixture/runstore.go
    - deps_test.go
    - go.mod
    - go.sum

key-decisions:
  - "D-57 resolved and landed: the tracer provider defaults to trace/noop.NewTracerProvider(), never the ambient global (otel.GetTracerProvider()); an application that wants entflow's spans in its own pipeline passes its TracerProvider explicitly through worker.Options."
  - "D-57's measured footprint, closing RESEARCH.md Assumption A2: at the pinned v1.45.0 tag (not master, which RESEARCH.md read), entflow's non-test dependency graph gains exactly 9 go.opentelemetry.io/otel/* packages AND exactly ONE external module beyond that family — github.com/cespare/xxhash/v2, pulled in transitively by otel/attribute's own hashing helper. RESEARCH.md's master-branch read had this as a vendored-internal copy with zero external modules; the pinned tag's actual attribute/internal/xxhash wrapper imports the real external module. xxhash/v2 was already a test-only indirect dependency (via testcontainers-go) before this plan, so nothing new entered go.sum — it simply now also appears in the production build graph. This is a documented correction to RESEARCH.md's claim, not a decision reversal: D-57 stands (trace API only, no SDK, no ambient global), the footprint is one small, already-vetted module wider than research predicted."
  - "D-58 resolved: one root span per run, started and ended within the claim that first seeds trace_context and never held open across a claim boundary (a span cannot survive a process boundary) — later claims restore its trace/span identity as a remote parent instead of extending its lifetime. One child span per step that actually ran or errored, named workflow.<Flow>.<step>, zero spans for a skipped step or an empty poll cycle."
  - "D-59 resolved: trace context is persisted through both RunStore.Advance (already declared in plan 02-02) AND the newly-extended RunStore.Fail (this plan), because a step can fail on a run's very first claim — the one that would otherwise seed trace_context for every later retry of that same step."
  - "worker.Options.TracerProvider narrowed from `any` (plan 02-02's deliberate placeholder) to the real trace.TracerProvider now that D-57's dependency exception has landed — per this plan's explicit environment guidance, even though the plan's own files_modified frontmatter omitted worker/options.go."

requirements-completed: [OPS-05]

coverage:
  - id: D1
    description: "A run's trace context is persisted on the run row and restored as a remote parent at each claim, so steps executed in different processes across a crash or re-claim belong to one trace"
    requirement: OPS-05
    verification:
      - kind: unit
        ref: "worker/span_test.go#TestTraceContextRoundTrip"
        status: pass
      - kind: unit
        ref: "observability_test.go#TestTwoStepRunChildSpansInOrderShareRootTrace"
        status: pass
    human_judgment: false
  - id: D2
    description: "An empty or malformed stored trace-context value decodes to 'no parent' rather than an error, so a run written before this feature existed still executes"
    requirement: OPS-05
    verification:
      - kind: unit
        ref: "worker/span_test.go#TestTraceContextEmptyDecodesToNoParent"
        status: pass
      - kind: unit
        ref: "worker/span_test.go#TestTraceContextMalformedDecodesToNoParent"
        status: pass
    human_judgment: false
  - id: D3
    description: "A single-step run emits exactly one root span and one child span, named workflow.CancelOrder.cancel, carrying the five D-58 attributes with correct values and no run input or step result in any attribute"
    requirement: OPS-05
    verification:
      - kind: unit
        ref: "observability_test.go#TestSingleStepRunProducesRootAndOneChildWithAttributes"
        status: pass
    human_judgment: false
  - id: D4
    description: "A run whose every remaining step is skipped emits exactly one root span and zero child spans; a poll cycle that claims nothing emits no span at all"
    requirement: OPS-05
    verification:
      - kind: unit
        ref: "observability_test.go#TestAllStepsSkippedProducesRootAndZeroChildren"
        status: pass
      - kind: unit
        ref: "observability_test.go#TestPollCycleClaimingNothingProducesNoSpans"
        status: pass
    human_judgment: false
  - id: D5
    description: "A failing step's child span carries a recorded error and an error status, with the recorded message naming the step and never embedding the run's input"
    requirement: OPS-05
    verification:
      - kind: unit
        ref: "observability_test.go#TestFailingStepSpanRecordsErrorAndErrorStatus"
        status: pass
    human_judgment: false
  - id: D6
    description: "The tracer provider defaults to the OpenTelemetry no-op provider from the trace module itself, never the ambient global; entflow's non-test dependency graph gains exactly the trace API's own package closure"
    requirement: OPS-05
    verification:
      - kind: unit
        ref: "worker/span_test.go#TestResolveTracerProviderDefaultsToNoop"
        status: pass
      - kind: unit
        ref: "deps_test.go#TestNoTransportDeps"
        status: pass
      - kind: unit
        ref: "deps_test.go#TestTracingDependencyNarrowness"
        status: pass
    human_judgment: false

duration: ~20min
completed: 2026-08-15
status: complete
---

# Phase 02 Plan 06: Observability — spans, cross-process trace continuity, and the D-57 dependency boundary Summary

**A crashed or re-claimed run is one trace, not two: `worker/span.go` persists and restores an OTel trace context across the resume boundary, `worker/dbstep.go` wires exactly one root span per run and one child span per executed step, and `deps_test.go`'s derived — never hand-curated — dependency gate now proves D-57's tracing exception stays exactly as narrow as it claims.**

## Performance

- **Duration:** ~20 min
- **Started:** 2026-08-15T13:28:00Z (approx.)
- **Completed:** 2026-08-15T13:48:08Z
- **Tasks:** 3 (all `type="auto"`)
- **Files modified:** 11 (3 created, 8 modified)

## Accomplishments

- `worker/span.go` declares the D-58 naming/attribute convention (`SpanName`/`RootSpanName`/`StepAttributes`/`RunAttributes`) and a hand-rolled trace-context codec (`EncodeTraceContext`/`DecodeTraceContext`/`WithRestoredParent`) using only the trace package's own hex rendering — no `propagation` package, no ambient global. `DefaultTracerProvider`/`ResolveTracerProvider` make the no-op default (`trace/noop`) the one place `worker.Options.TracerProvider`'s nil case resolves through.
- `worker/dbstep.go`'s new `claimTelemetry` wires spans into the claim path exactly as D-58 requires: a run's root span is started **and ended within the single claim that first seeds `trace_context`** — never held open across a claim boundary, because a span cannot survive a process boundary — and every later claim restores that trace/span/flags identity as a remote parent for its own step's child span. `firstClaim` is keyed off `run.TraceContext == ""` rather than `run.CurrentStep == ""`, so a retried first DB step doesn't fork a second, unrelated trace on every attempt.
- `observability_test.go` proves the whole contract against a real OTel SDK span recorder, imported only from that test file: a single-step run emits exactly one root + one child (with correct name, attributes, and no leaked input/results); a two-step run emits exactly two children under one root, in order, with the second claim's child sharing the first claim's trace ID even though it was restored from a persisted string rather than an in-process parent; an all-skipped run emits one root and zero children; a failing step's span carries a recorded error and error status; and an empty poll claims nothing and emits nothing.
- `deps_test.go` extends the Phase 1 derived-allowlist technique to tracing: whatever `go.opentelemetry.io/otel`-prefixed packages entflow's own code actually imports get run through `go list -deps` and unioned into the allowed set, so the D-57 amendment self-maintains across version bumps exactly as the ent half already does. `TestTracingDependencyNarrowness` fails the build graph if any of the five modules the ambient-global tracer path would pull in appear, or if the SDK itself does — verified locally by temporarily importing the root `go.opentelemetry.io/otel` package, observing the failure, and reverting it.
- `worker.Options.TracerProvider` narrows from `any` (plan 02-02's deliberate placeholder) to the real `trace.TracerProvider`.

## Task Commits

1. **Task 1: Persist and restore a run's trace context across the resume boundary** - `d5f7136` (feat)
2. **Task 2: One root span per run, one child span per step** - `3e639c9` (feat)
3. **Task 3: Amend META-02's dependency gate — narrowly, and still derived rather than curated** - `9469e56` (feat)

## Files Created/Modified

- `worker/span.go` - the D-58 naming/attribute convention and the D-59 trace-context codec; `DefaultTracerProvider`/`ResolveTracerProvider`
- `worker/span_test.go` - round-trip, empty/malformed-decodes-to-no-parent, restored-parent-becomes-span-parent (against the real no-op default), span-name/root-span-name fixtures
- `observability_test.go` - root/child span counts and ordering across single-step, two-step, all-skipped, and empty-poll runs; attribute correctness and the input/result non-leakage prohibition; failing-step error status; cross-claim trace-ID continuity
- `worker/dbstep.go` - `claimTelemetry`, `startClaimTelemetry`/`finishSpans`, wired into `claimOnce`'s success path and `failStep`'s failure path; `claimOnce` gained a `flowName` parameter
- `worker/options.go` - `TracerProvider trace.TracerProvider` (was `any`)
- `worker/worker.go` - `ClaimOnce` now passes the flow's name into `claimOnce`
- `runstore.go` - `Fail` gains a `TraceContext` field
- `internal/testdata/entflowfixture/runstore.go` - `Fail` now persists `f.TraceContext`
- `deps_test.go` - `TestNoTransportDeps`'s allowlist is now the union of the ent-derived and tracing-derived closures; new `TestTracingDependencyNarrowness`
- `go.mod`/`go.sum` - `go.opentelemetry.io/otel/trace` promoted to a direct dependency at v1.45.0; `go.opentelemetry.io/otel/sdk` added (test-only); `go mod tidy` also removed several stale indirect checksums left over from plan 02-05's generator invocation (`olekukonko/*`, `spf13/cobra`, etc. — build-tooling noise, never in the actual build graph)

## Decisions Made

- **D-57 landed as a real dependency exception**, not just a documented intent: `worker/span.go` is the one file that spends it, importing exactly `go.opentelemetry.io/otel/trace` (plus its own dependency-free `attribute`/`codes`/`trace/noop` sub-packages) and never the root `go.opentelemetry.io/otel` convenience package.
- **D-57's measured footprint corrects RESEARCH.md Assumption A2**: at the pinned v1.45.0 tag, `go.opentelemetry.io/otel/attribute`'s internal xxhash wrapper imports the real external `github.com/cespare/xxhash/v2` module, not a vendored-internal copy as the master-branch research read claimed. This is documented as a correction, not a decision reversal — the module was already a test-only indirect dependency via testcontainers-go, so nothing new entered `go.sum`; it simply now also appears in the non-test build graph, and `deps_test.go`'s derived (not hand-curated) allowlist admits it correctly for exactly that reason.
- **D-58 resolved with an explicit "root span never outlives its creating claim" design**: a span cannot survive a process boundary, so the root span is created and ended within the first claim, and every later claim restores its identity as a remote parent for that claim's own child span — documented on `startClaimTelemetry`'s doc comment so a future reader expecting a long-lived root span finds the explanation immediately.
- **`firstClaim` is keyed off `run.TraceContext == ""`, not `run.CurrentStep == ""`** — see Deviations below for why the more obvious choice would have been a bug.
- **`worker.Options.TracerProvider` narrowed to the real type** even though the plan's own `files_modified` frontmatter didn't list `worker/options.go` — the environment's explicit guidance ("Narrowing that type is expected here if your decision permits it") and Task 1/2's own action text (consuming the real `trace.TracerProvider` type) both require it; recorded here as the plan's frontmatter being an incomplete file list, not a scope violation.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `EncodeTraceContext`/`DecodeTraceContext` dropped `TraceFlags`, silently disabling every span recorded after a run's first claim under a real SDK's default sampler**
- **Found during:** Task 2, writing `observability_test.go`'s two-step resume test against a real `sdktrace.NewTracerProvider` — the second claim's child span never appeared in the recorder's `Ended()` output.
- **Issue:** The original encode/decode format carried only the trace ID and span ID. A real OTel SDK's default `ParentBased` sampler consults a remote parent's `TraceFlags` (specifically the sampled bit) to decide whether a child span restored from that parent should itself be sampled. Since `trace.NewSpanContext` defaults `TraceFlags` to `0` (not sampled) when unset, every span started from `WithRestoredParent`'s output was silently created as a non-recording span — it never reached the recorder's `OnEnd`, and would never reach a real exporter in production either. This would have shipped a tracing feature that appears to work under the no-op default (which ignores sampling entirely) but silently stops recording anything past a run's first claim the moment an application wires a real SDK — exactly the kind of bug that is invisible until someone opens a trace for a crashed run and finds it empty.
- **Fix:** Extended the stored format to a third, delimited field carrying `sc.TraceFlags().String()` (2 hex chars), parsed back via `strconv.ParseUint` and passed through `trace.SpanContextConfig.TraceFlags` on decode. Doc comments on both `EncodeTraceContext` and `DecodeTraceContext` explain why the flags byte is load-bearing, not cosmetic, so a future editor doesn't "simplify" it away.
- **Files modified:** worker/span.go
- **Verification:** `observability_test.go#TestTwoStepRunChildSpansInOrderShareRootTrace` reproduced the failure before the fix (0 new spans after the second claim) and passes after it; the full `worker/span_test.go` round-trip suite still passes.
- **Committed in:** 3e639c9 (Task 2 commit)

**2. [Rule 2 - Missing Critical] `RunStore.Fail` had no `TraceContext` field, so a step failing on a run's very first claim would lose its newly-seeded root span identity**
- **Found during:** Task 2, wiring `failStep` to persist the root span's context alongside the success path
- **Issue:** `RunStore.Advance` (plan 02-02) already carried a `TraceContext` field, but `RunStore.Fail` did not. A DB step that fails on the run's first claim (`run.TraceContext == ""`) writes through `Fail`, not `Advance` — without a place to persist the root span it just created, a retryable failure on the first step would create a brand-new root span (and a brand-new trace ID) on every retry attempt, defeating D-59's entire purpose for exactly the runs most likely to need it (a step that fails and retries).
- **Fix:** Added `TraceContext string` to the `Fail` struct (runstore.go) and wired `internal/testdata/entflowfixture/runstore.go`'s `Fail` method to `SetTraceContext(f.TraceContext)`, mirroring `Advance`'s existing field exactly.
- **Files modified:** runstore.go, internal/testdata/entflowfixture/runstore.go, worker/dbstep.go
- **Verification:** full suite green; `TestFailingStepSpanRecordsErrorAndErrorStatus` exercises the failure path's span/trace-context write directly.
- **Committed in:** 3e639c9 (Task 2 commit)

---

**Total deviations:** 2 auto-fixed (1 Rule 1 — a real bug that would have silently broken tracing under any real SDK, found by testing against one instead of only the no-op default; 1 Rule 2 — a missing field that would have defeated D-59 for exactly the failure-and-retry case it exists to cover). Both are direct, necessary consequences of this plan's own change; no scope creep, no architectural changes.

## Issues Encountered

- `go mod tidy` (run after adding the SDK as a test-only dependency) promoted `go.opentelemetry.io/otel` (the root module) to a **direct** `require` line in `go.mod`, even though no file in this module literally writes `import "go.opentelemetry.io/otel"`. This is Go's module bookkeeping working correctly, not a violation of D-57: `go.opentelemetry.io/otel/attribute` and `go.opentelemetry.io/otel/codes` — both directly imported by `worker/span.go`/`worker/dbstep.go` — are packages *within* the `go.opentelemetry.io/otel` module (they ship from the same `go.mod` as the root package), so any direct import of them marks the whole module direct. `deps_test.go`'s `TestTracingDependencyNarrowness` is the check that actually matters here — it asserts the root package's own additional imports (go-logr, otel/metric, otel/propagation, otel/auto/sdk) never enter the graph — and it passes. Documented here so a future reader auditing `go.mod`'s direct/indirect split doesn't mistake the module-level marking for a package-level one.
- `go mod tidy` also cleaned up several stale indirect checksums plan 02-05's summary already flagged as generator-invocation noise (`olekukonko/tablewriter`, `spf13/cobra`, etc.) — removed as a side effect of this plan's `go get`, not a deliberate cleanup task.

## User Setup Required

None - no external service configuration required. An application that wants entflow's spans exported anywhere still needs to configure its own OTel SDK/exporter and pass the resulting `TracerProvider` through `worker.Options` — entflow ships no exporter and never will (per D-57's own boundary).

## Next Phase Readiness

- The observability requirement (OPS-05) that closes Phase 2's requirement list is fully proven: naming, attributes, cross-process continuity, the privacy prohibition, and the dependency boundary are all machine-checked, not just documented.
- `docs/dialects.md` and `worker/span.go`'s doc comments are now the two places a future contributor needs to read before touching either the claim-strategy or the tracing seam — both explicitly state what they do NOT do (no MySQL partial index; no long-lived root span) so a reader isn't left assuming otherwise.
- No blockers for plan 02-07 (worker lifecycle/shutdown) or 02-08 (whatever closes the phase) — `worker.Options.TracerProvider`'s real type is now load-bearing for any future plan that touches `Options`, same as every other now-real field.

---
*Phase: 02-durability*
*Completed: 2026-08-15*

## Self-Check: PASSED

All created files verified present on disk (worker/span.go, worker/span_test.go, observability_test.go). All referenced commit hashes (d5f7136, 3e639c9, 9469e56) verified present in `git log --oneline --all`.
