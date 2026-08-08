---
phase: 01-runtime-core
plan: 02
subsystem: runtime
tags: [go, generics, di, codec, error-handling]

# Dependency graph
requires:
  - phase: 01-01
    provides: "Flow/FlowOf[In]/New[In]/FlowsOf builder surface, StepKind, Exec seam"
provides:
  - "entflow.Registry / NewRegistry / Provide[T] / WithRegistry / Use[T] / TryUse[T] — scoped DI"
  - "entflow.Codec[In] / JSONCodec[In] / WithCodec[In] — input serialization contract, wired through New[In]"
  - "entflow.Result[T](ctx, step) — cross-step typed result accessor backed by an unexported per-execution store"
  - "entflow.StepError / ErrRequiresDurableRun — the failure type Plan 03's executor will wrap every step error in"
affects: [01-03, 01-04, phase-02-durability, phase-03-activities]

actuals:
  tokens: 5900
  tasks: 3
  commits: 3

tech-stack:
  added: []
  patterns:
    - "Single context key per subsystem (registryKey, resultsKey), typed lookup happens inside the stored value (a *Registry or *resultStore) — never one context.WithValue call per concrete type. registry.go and result.go each call context.WithValue exactly once."
    - "Every stored-value type assertion uses the comma-ok form (v, ok := ...) and returns a wrapped sentinel error on mismatch rather than a bare panicking assertion — applies to TryUse[T]'s reflect.Type lookup and Result[T]'s step lookup alike."
    - "Panic values crossing a recoverable boundary (Use[T]'s panic) are always error values, never bare strings, so a future recover() site can errors.Is/As them without string matching."
    - "flowConfig stores option-supplied values (the codec) as `any` even though flowConfig itself is not generic; the consuming generic function (resolveCodec[In]) does the type-safe assertion back, which is how a non-generic options struct composes with a generic constructor."

key-files:
  created:
    - registry.go
    - registry_test.go
    - codec.go
    - codec_test.go
    - result.go
    - errors.go
    - result_test.go
  modified:
    - flow.go

key-decisions:
  - "codec_test.go and result_test.go are internal (package entflow) rather than entflow_test, since they exercise unexported seams (flowConfig, codecOf, withResults/putResult) that Plan 03's executor also needs — this was the pragmatic call rather than adding exported test-only shims to the public API surface."

patterns-established:
  - "Context-carried generic accessor: one context key type, one stored value, typed lookups inside it — reused identically for Registry (Use/TryUse) and resultStore (Result)."
  - "Sentinel errors wrapped with %w plus %T/%q detail so errors.Is discrimination and human-readable diagnosis coexist in the same error value."

requirements-completed: [CORE-07, CORE-08, CORE-10]

coverage:
  - id: D1
    description: "A developer registers a client at startup with entflow.Provide(reg, client) and retrieves it typed inside a step body with entflow.Use[T](ctx); each registry from NewRegistry() is independent"
    requirement: "CORE-07"
    verification:
      - kind: unit
        ref: "registry_test.go#TestProvideUseRoundTrip"
        status: pass
      - kind: unit
        ref: "registry_test.go#TestRegistriesAreIndependent"
        status: pass
      - kind: unit
        ref: "registry_test.go#TestProvideInterfaceType"
        status: pass
    human_judgment: false
  - id: D2
    description: "entflow.Use[T] panics with an error value when no provider is registered, and entflow.TryUse[T] returns that same error instead; concurrent Provide/TryUse is race-free"
    verification:
      - kind: unit
        ref: "registry_test.go#TestUsePanicsWithNoRegistry"
        status: pass
      - kind: unit
        ref: "registry_test.go#TestUsePanicsWithNotProvided"
        status: pass
      - kind: unit
        ref: "registry_test.go#TestRegistryConcurrentAccess (go test -race)"
        status: pass
    human_judgment: false
  - id: D3
    description: "Any Go type works as flow input via entflow.Codec[In], with a JSON codec as the default and WithCodec overriding it at construction; the round-trip is proven by test"
    requirement: "CORE-08"
    verification:
      - kind: unit
        ref: "codec_test.go#TestJSONCodecRoundTripStruct"
        status: pass
      - kind: unit
        ref: "codec_test.go#TestJSONCodecRoundTripPointer"
        status: pass
      - kind: unit
        ref: "codec_test.go#TestJSONCodecIsDefault"
        status: pass
      - kind: unit
        ref: "codec_test.go#TestWithCodecUsesCustomCodec"
        status: pass
    human_judgment: false
  - id: D4
    description: "entflow.Result[T](ctx, step) returns (T, error) and never panics, with distinct sentinel errors for no-store, unknown-step, and type-mismatch"
    requirement: "CORE-10"
    verification:
      - kind: unit
        ref: "result_test.go#TestResultNoStore"
        status: pass
      - kind: unit
        ref: "result_test.go#TestResultUnknownStep"
        status: pass
      - kind: unit
        ref: "result_test.go#TestResultTypeMismatch"
        status: pass
      - kind: unit
        ref: "result_test.go#TestResultSentinelsAreDistinct"
        status: pass
      - kind: unit
        ref: "result_test.go#TestResultNeverPanicsOnNilStoredValue"
        status: pass
    human_judgment: false
  - id: D5
    description: "A step failure surfaces as *entflow.StepError carrying the step name and kind, wrapping the cause so errors.Is/errors.As reach through it"
    verification:
      - kind: unit
        ref: "result_test.go#TestStepErrorUnwrap"
        status: pass
      - kind: unit
        ref: "result_test.go#TestStepErrorAsRecoversFields"
        status: pass
    human_judgment: false
  - id: D6
    description: "go build ./..., go vet ./..., and go test ./... -race all exit 0, with no new non-stdlib/non-ent dependency in the non-test build graph, and New's published signature from Plan 01 is unchanged"
    verification:
      - kind: unit
        ref: "go build ./... && go vet ./... && go test ./... -race"
        status: pass
      - kind: unit
        ref: "go list -deps github.com/smintz/entflow (stdlib + entgo.io/ent/schema only)"
        status: pass
    human_judgment: false

duration: 7min
completed: 2026-08-08
status: complete
---

# Phase 1 Plan 2: Runtime Services Summary

**Scoped DI registry (Provide/Use/TryUse), a JSON-default Codec[In] wired through New, and a panic-free Result[T] cross-step accessor backed by StepError — the three runtime-service contracts Plan 03's executor consumes.**

## Performance

- **Duration:** ~7 min (task execution only; excludes upfront context reading)
- **Started:** 2026-08-08T11:28:24Z
- **Completed:** 2026-08-08T11:34:30Z
- **Tasks:** 3
- **Files modified:** 8 (7 created, 1 modified)

## Accomplishments

- Implemented the scoped DI registry (`registry.go`): `NewRegistry`, `Provide[T]`, `WithRegistry`, `Use[T]`, `TryUse[T]`, keyed by `reflect.TypeFor[T]()` so interface-typed registration (`Provide[Refunder](reg, concrete)`) resolves correctly, with a single `context.WithValue` call and race-free concurrent access proven under `-race`
- `Use[T]` panics with an `error` value (never a string) on `ErrNoRegistry`/`ErrNotProvided`, so a future `recover()` site can `errors.Is`/`errors.As` the recovered value without string matching (D-16)
- Implemented `Codec[In]` (`codec.go`): the interface is parameterized on the input type rather than requiring `In` to implement anything, `JSONCodec[In]` ships as the default, and `WithCodec[In]` overrides it at construction — a codec supplied for a mismatched input type panics at `New`'s call site with a descriptive error naming both types
- Wired the codec resolution through `flow.go`: `New[In]` now applies `FlowOption`s to an internal `flowConfig` and resolves the codec via `resolveCodec[In]`, storing it on `FlowOf[In]` behind an unexported `codecOf()` accessor — `New`'s published signature is unchanged from Plan 01
- Implemented `Result[T]` (`result.go`) backed by an unexported per-execution `resultStore` carried on context via a single context key, with `withResults`/`putResult` as the seam Plan 03's executor will call — every stored-value assertion uses the comma-ok form, so a step-name typo or a type mismatch returns a distinct sentinel error (`ErrNoResultStore`, `ErrUnknownStep`, `ErrResultTypeMismatch`) instead of panicking
- Implemented `StepError` (`errors.go`) with `Step`, `Kind`, `Err`, `Stack` fields, an `Error()` that renders the step/kind/cause without inlining `Stack`, and an `Unwrap()` that lets `errors.Is`/`errors.As` reach through to the underlying cause (including ent's own error types in later plans) — `ErrRequiresDurableRun` is declared alongside it per D-13
- Verified via `go list -deps github.com/smintz/entflow` that the non-test dependency graph still contains only stdlib plus `entgo.io/ent/schema` (from Plan 01's transitions annotation) — no new module dependency was introduced
- Re-ran the full suite (`go build ./...`, `go vet ./...`, `go test ./... -race`) including Plan 01's `TestCancelOrderFlow`, confirming the ent cycle-breaking fixture from 01-01 is still intact

## Task Commits

1. **Task 1: Scoped DI registry — Provide, WithRegistry, Use, TryUse** — `8816d79` (feat)
2. **Task 2: Codec[In] contract, the core JSON codec, and the WithCodec construction option** — `9558bca` (feat)
3. **Task 3: Result[T] cross-step accessor and the StepError failure type** — `ec52109` (feat)

**Plan metadata:** pending (this commit)

## Files Created/Modified

- `registry.go` — `Registry`, `NewRegistry`, `Provide[T]`, `WithRegistry`, `Use[T]`, `TryUse[T]`, `ErrNoRegistry`, `ErrNotProvided`
- `registry_test.go` — round-trip, independence, missing-registry/missing-type panics and errors, interface-typed provisioning, concurrent access under `-race`
- `codec.go` — `Codec[In]`, `JSONCodec[In]`, `WithCodec[In]`, unexported `resolveCodec[In]`
- `codec_test.go` (internal `package entflow`) — round-trip for struct/pointer inputs, malformed-input error handling without byte leakage, default vs. custom codec resolution, mismatched-codec panic
- `flow.go` — `flowConfig` now carries `codec any`; `New[In]` resolves it via `resolveCodec[In]` and stores it on `FlowOf[In]`; added unexported `codecOf()` accessor
- `result.go` — `Result[T]`, unexported `resultStore`/`withResults`/`putResult`, `ErrNoResultStore`, `ErrUnknownStep`, `ErrResultTypeMismatch`
- `errors.go` — `StepError` (`Step`, `Kind`, `Err`, `Stack`), `Error()`, `Unwrap()`, `ErrRequiresDurableRun`
- `result_test.go` (internal `package entflow`) — no-store/unknown-step/type-mismatch/typed-hit paths, sentinel distinctness, nil-value safety, `StepError` unwrap/`errors.As`

## Decisions Made

- **codec_test.go and result_test.go use internal `package entflow`** rather than the external `entflow_test` package used by `exec_test.go`/`registry_test.go`. Both files needed to exercise unexported seams (`flowConfig`, `codecOf()`, `withResults`/`putResult`) that Plan 03's executor also depends on — adding exported test-only shims to the public API purely to keep the tests external would have widened the public surface for no consumer benefit, so the internal-test-package route was chosen instead.
- No architectural deviations (Rule 4) were needed — the plan's `<interfaces>` section and 01-RESEARCH.md's verified reference implementations for the registry and `Result[T]` shapes were followed essentially as written.

## Deviations from Plan

None — plan executed exactly as written. The one deliberate departure from the plan's file-organization implication (codec_test.go/result_test.go as internal tests rather than `_test` package) is documented above under Decisions Made rather than as a deviation, since it does not change any exported behavior or contract.

## Issues Encountered

- The acceptance criterion `grep -c 'context.WithValue' registry.go` (and the equivalent for `result.go`) counts matching **lines**, not occurrences — an early draft of `registry.go`'s doc comments mentioned "context.WithValue" twice in prose in addition to the one real call, making the count 3. Reworded the comments to describe the mechanism without repeating the literal function name, bringing the count to exactly 1 while keeping the explanation intact. No functional change; caught before committing.
- Two test names (`TestNewDefaultsToJSONCodec`, `TestNewWithCodecUsesCustomCodec`) did not match the plan's own `<verify>` regex (`TestCodec|TestJSONCodec|TestWithCodec`) on first pass — renamed to `TestJSONCodecIsDefault` / `TestWithCodecUsesCustomCodec` so the plan's stated verification command actually exercises them. No functional change.

## User Setup Required

None — no external service configuration required.

## Next Phase Readiness

- `Registry`/`Use`/`TryUse`, `Codec[In]`/`WithCodec`, `Result[T]`, and `StepError`/`ErrRequiresDurableRun` are all locked and independently tested; Plan 03's executor can consume all four without further design decisions, exactly as this plan's objective intended.
- The panic-to-error contract Plan 03 depends on (D-16: `Use[T]`'s panic value is always an `error`) is proven by test (`TestUsePanicsWithNotProvided` asserts `errors.Is` on the recovered value), so Plan 03's `recover()`-based executor can rely on it without re-deriving the guarantee.
- `withResults`/`putResult` are deliberately unexported and untested from outside the package boundary except via the internal test file — Plan 03 is expected to call them directly (same package) when wiring `Exec`'s per-execution result store.
- No blockers for Plan 03.

---
*Phase: 01-runtime-core*
*Completed: 2026-08-08*

## Self-Check: PASSED

All created/modified files verified present on disk (registry.go, registry_test.go, codec.go, codec_test.go, result.go, errors.go, result_test.go, flow.go, this SUMMARY.md). All three task commits (`8816d79`, `9558bca`, `ec52109`) verified present in `git log --oneline --all`.
