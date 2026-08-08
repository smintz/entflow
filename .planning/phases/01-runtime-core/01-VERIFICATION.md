---
phase: 01-runtime-core
verified: 2026-08-08T12:57:28Z
status: passed
score: 5/5 must-haves verified
behavior_unverified: 0
overrides_applied: 0
---

# Phase 1: Runtime Core Verification Report

**Phase Goal:** A developer can declare and execute a DB-only multi-step flow entirely by hand —
no run-row persistence, no codegen — proving the core builder API and step-kind safety rules
work before anything else is built on top of them.

**Verified:** 2026-08-08T12:57:28Z
**Status:** passed
**Re-verification:** No — initial verification

## Goal Achievement

### Observable Truths (ROADMAP Success Criteria)

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | A developer can declare a flow in a schema file via `Flows()` with a typed input (`entflow.New[In]("Name")`), chain DB steps (`UpdateSelf`, `CreateSelf`, `Query`, `Check`, etc.) whose closures receive `*ent.Tx`, and see the flow execute end-to-end inside a single transaction with no run row | ✓ VERIFIED | `internal/testdata/ent/schema/order_flows.go` declares `Order.Flows()` via `entflow.New[*CancelOrderRequest]`. All 7 DB-step constructors exist in `steps.go` (`Step`, `CreateSelf`, `UpdateSelf`, `Create`, `Update`, `Query`, `Check`), each closure typed `func(ctx, TX, In) (Ent, error)`. `exec_test.go#TestRunInTxCommitsAndIsReadable` builds a plain DB-only flow, runs it via `RunInTx`, and reads the mutation back through the *non-transactional* client — proving single-transaction, no-run-row execution end to end. Ran locally: `go test -run TestRunInTxCommitsAndIsReadable -v` → PASS. |
| 2 | A developer can declare Activity steps whose closures do not receive `*ent.Tx` — passing `*ent.Tx` into an Activity closure is a compile error, not a runtime check | ✓ VERIFIED | `activity.go`'s `Activity[In, Ent, Out any](f, name, fn func(ctx, self Ent, att Attempt) (JSON[Out], error), ...)` has no transaction type parameter or closure parameter. Ran `go build ./internal/testdata/compilefail/` locally: **exits 1**, output: `in call to entflow.Activity, type func(ctx context.Context, tx fakeTx, self fakeEnt, att entflow.Attempt) (...) does not match func(ctx context.Context, self Ent, att entflow.Attempt) (...)` — names `Activity` and reports a signature mismatch, exactly as required. `go build ./...` (bare wildcard) still exits 0 with the fixture present, confirming the `testdata` path-segment exclusion. |
| 3 | A developer can register a client at startup with `entflow.Provide` and retrieve it typed inside a step body with `entflow.Use[T](ctx)`, and can supply any Go type as flow input by implementing `entflow.Codec[In]` (a JSON codec ships in core; no protobuf dependency required) | ✓ VERIFIED | `registry.go`: `NewRegistry`, `Provide[T]`, `WithRegistry`, `Use[T]`, `TryUse[T]`, keyed by `reflect.TypeFor[T]()`, single `context.WithValue` call. `codec.go`: `Codec[In]` interface (parameterized on `In`, not requiring `In` to implement anything), `JSONCodec[In]` default, `WithCodec[In]` override. `registry_test.go`/`codec_test.go` cover round-trip, independence, panic/error paths, and a codec test using an input struct with no entflow methods. `go test -run 'TestProvide|TestUse|TestTryUse|TestCodec|TestJSONCodec|TestWithCodec' -v` → all PASS locally. `deps_test.go#TestNoTransportDeps` (run locally, PASS) proves no protobuf/transport dependency entered the non-test build graph. |
| 4 | A developer can call `Describe()` on a flow and see its full step topology (name, kind, deps, transition claims, emit topics, retry policy) printed as data without any step closure ever executing; the same topology data is exposed through a dedicated `meta` package that never imports transport or protobuf machinery | ✓ VERIFIED | `describe.go#Describe()` renders `Meta()` (never internal steps) covering every fact listed; matches committed `testdata/describe.golden` (verified: all three fixture steps — `cancel`/db, `refund`/activity, `order.cancelled`/emit — rendered with transition/emit/conditions/retry). `meta/meta.go` is stdlib-only (`import "time"` only); `meta_test.go#TestMetaPackageDependenciesAreStdlibOnly` and `deps_test.go` both assert this and pass locally. `extractability_test.go#TestNoClosureInMeta` (reflective walk, no `reflect.Func` field in any of the 4 meta types) and `TestClosuresNeverInvoked` (atomic counter stays 0 across `FlowsOf`/`Meta`/`Describe`) both PASS locally. |
| 5 | A developer can declare a status field's legal transitions with `entflow.Transitions(map[string][]string{...})` and read a prior step's typed result inside a later step via `entflow.Result[T](ctx, "step")` | ✓ VERIFIED | `transitions.go#Transitions`/`TransitionsAnnotation` declared on `internal/testdata/ent/schema/order.go`'s `status` enum field. `transitions_graph_test.go#TestTransitionsGraph` loads the fixture via `entc.LoadGraph`, decodes the annotation off the loaded graph via ent's own `Annotations[Name()] → json.Marshal → json.Unmarshal` idiom, and asserts the decoded map exactly matches the declared map — PASS locally (1.05s). `result.go#Result[T]` implemented with 3 distinct sentinel errors and comma-ok assertions; `exec_test.go#TestResultAccessibleAcrossSteps` proves a later step reads an earlier step's typed result during a real `Exec` run — PASS locally. |

**Score:** 5/5 truths verified (0 present, behavior-unverified)

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `go.mod` | Module `github.com/smintz/entflow`, go 1.25, exact `entgo.io/ent v0.14.6` pin | ✓ VERIFIED | Confirmed exact pin, no floating version. |
| `flow.go` | `Flow` interface, `FlowOf[In]`, `New[In]`, `FlowsOf`, `Meta()` | ✓ VERIFIED | All present; `Flow` interface extended with `Meta()`/`Describe()` per Plan 04. |
| `step.go` | Step declaration-data/closure separation | ✓ VERIFIED | `run` field isolated from data fields; structural half of D-19 extractability proof. |
| `steps.go` | All 7 DB-step constructors over `addDBStep` | ✓ VERIFIED | Verified by direct read; single shared mechanism confirmed. |
| `activity.go` | `Activity`, `Emit` — declarable, not executable | ✓ VERIFIED | Confirmed signature omits TX type param; `run` panics defensively if ever invoked (unreachable — `Exec` refuses first). |
| `exec.go` | `Exec`, `RunInTx`, `WithSelfStatus`, `Tx`/`TxOpener` | ✓ VERIFIED | Six-phase executor confirmed by direct read; exactly 2 `recover()` sites. |
| `registry.go` | Scoped DI: `Provide`, `Use`, `TryUse` | ✓ VERIFIED | Confirmed by direct read; single `context.WithValue` call. |
| `codec.go` | `Codec[In]`, `JSONCodec[In]`, `WithCodec` | ✓ VERIFIED | Confirmed. |
| `result.go` | `Result[T]`, 3 sentinel errors | ✓ VERIFIED | Confirmed. |
| `errors.go` | `StepError`, `ErrRequiresDurableRun` | ✓ VERIFIED | `Unwrap()` present; confirmed via test `TestStepErrorAsRecoversFields`. |
| `meta/meta.go` | Stdlib-only `FlowMeta`/`StepMeta`/`ConditionMeta`/`RetryMeta` | ✓ VERIFIED | Confirmed `import "time"` only. |
| `describe.go` | `Describe()` rendering `Meta()` | ✓ VERIFIED | Confirmed; golden-file match. |
| `transitions.go` | `Transitions`, `TransitionsAnnotation` | ✓ VERIFIED | Confirmed `Name()` returns `"EntflowTransitions"`. |
| `internal/testdata/compilefail/activity_tx.go` | CORE-04 compile-fail fixture | ✓ VERIFIED | `go build` on this path exits 1 with expected message. |
| `internal/testdata/ent/schema/order.go`, `order_flows.go` | Fixture schema + full worked-example flow | ✓ VERIFIED | Matches entflow.md §3.1 in full (cancel/refund/emit). |
| `internal/testdata/entclient/client.go` | In-memory SQLite test client | ✓ VERIFIED | Driver name `"sqlite"` confirmed; blank-imports `ent/runtime` for the hook workaround. |
| `testdata/describe.golden`, `testdata/callshapes.golden` | Committed golden snapshots | ✓ VERIFIED | Both present and matched by their respective tests. |
| `.github/workflows/ci.yml` | CI running build/vet/test on push and PR | ✓ VERIFIED | Confirmed: build → vet → test -race, `GOFLAGS: -mod=readonly`. |

### Key Link Verification

| From | To | Via | Status | Details |
|------|-----|-----|--------|---------|
| `order_flows.go` | `flow.go`/`steps.go` | `entflow.New`/`entflow.UpdateSelf` | ✓ WIRED | Confirmed by direct read and passing test. |
| `exec_test.go` | `exec.go` | `.Exec(ctx, tx, in)` / `RunInTx` | ✓ WIRED | Multiple tests exercise both seams against a real SQLite-backed `*ent.Tx`. |
| `exec.go` | `step.go` | `s.run(ctx, tx, in)` type-erased adapter | ✓ WIRED | Confirmed; comma-ok assertions throughout. |
| `flow.go` | `meta/meta.go` | `Meta() meta.FlowMeta` | ✓ WIRED | Confirmed one-directional (meta never imports entflow). |
| `describe.go` | `meta/meta.go` | Renders `Meta()`, never internal steps | ✓ WIRED | Confirmed by direct read — no map iteration, no closure access. |
| `deps_test.go` | `go.mod` | `go list -deps ./...` vs. derived allowlist | ✓ WIRED | Ran locally — PASS; allowlist is programmatically derived, not hand-curated (verified in source). |

### Data-Flow Trace (Level 4)

Not applicable in the traditional sense (no UI/API rendering pipeline in Phase 1). The equivalent
check here is "does `Describe()`/`Meta()` output originate from real declaration data, not a
static stub" — confirmed: `Meta()` projects live `step` struct fields (`stepMetaOf`), and the
golden-file drift test (`TestCallShapesDetectsDrift`) proves the rendering is not a hardcoded
string that would pass regardless of the underlying flow's shape.

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| Full build | `GOFLAGS=-mod=readonly go build ./...` | exit 0 | ✓ PASS |
| Full vet | `GOFLAGS=-mod=readonly go vet ./...` | exit 0 | ✓ PASS |
| Full test suite (race) | `GOFLAGS=-mod=readonly go test ./... -race -count=1` | exit 0, `ok github.com/smintz/entflow 4.248s` | ✓ PASS |
| CORE-04 compile-fail proof | `go build ./internal/testdata/compilefail/` | exit 1, names `Activity` + `does not match` | ✓ PASS |
| No `ariga.io/atlas` in non-test graph | `go list -deps ./... \| grep -c ariga.io/atlas` | `0` | ✓ PASS |
| No `testdata` segment in non-test graph | `go list -deps ./... \| grep -c testdata` | `0` | ✓ PASS |
| Named invariant tests | `go test -run 'TestNoTransportDeps\|TestTransitionsGraph\|TestExtractability\|TestNoClosureInMeta\|TestClosuresNeverInvoked\|TestCallShapes' -v` | all PASS | ✓ PASS |
| Total test count | `go test ./... -v \| grep -c '^--- PASS'` / `'^--- FAIL'` | 73 PASS / 0 FAIL | ✓ PASS |

All spot-checks executed directly by this verifier (not sourced from SUMMARY.md claims).

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|-------------|-------------|--------|----------|
| CORE-01 | 01-01 | Declare a flow via `Flows()` with typed input | ✓ SATISFIED | `order_flows.go`, `flow.go#New` |
| CORE-02 | 01-01 | Step closures compile-checked against `In` | ✓ SATISFIED | Generic constructors infer `In`/`TX`/`Ent` |
| CORE-03 | 01-03 | All 7 DB-step constructors | ✓ SATISFIED | `steps.go`, `steps_test.go#TestDBStepAllSevenConstructors` |
| CORE-04 | 01-03 | Activity closures cannot receive `*ent.Tx` (compile error) | ✓ SATISFIED | `activity.go`, compile-fail fixture verified non-zero exit |
| CORE-05 | 01-03 | `After(...)`/`When(...)`/`SelfWas(...)` | ✓ SATISFIED | `stepoptions.go`, `exec_test.go` ordering/condition tests |
| CORE-06 | 01-01 | DB-only flow, single tx, no run row | ✓ SATISFIED | `TestRunInTxCommitsAndIsReadable` |
| CORE-07 | 01-02 | `Provide`/`Use[T]` DI | ✓ SATISFIED | `registry.go`, `registry_test.go` |
| CORE-08 | 01-02 | Any Go type as input via `Codec[In]`, JSON default | ✓ SATISFIED | `codec.go`, `codec_test.go` |
| CORE-09 | 01-03 | Fixed retry parameter set | ✓ SATISFIED | `retry.go` — exactly 3 fields, validated |
| CORE-10 | 01-02/03 | `Result[T](ctx, "step")` | ✓ SATISFIED | `result.go`, `TestResultAccessibleAcrossSteps` |
| CORE-11 | 01-04 | Codegen-needed facts readable without evaluating closures | ✓ SATISFIED | `extractability_test.go` (structural + behavioral) |
| CORE-12 | 01-04 | `Describe()` dry-run surface | ✓ SATISFIED | `describe.go`, golden-file match |
| SM-01 | 01-01/04 | `Transitions(map[string][]string{...})` annotation | ✓ SATISFIED | `transitions.go`, `TestTransitionsGraph` |
| META-01 | 01-04 | `meta` package, stdlib-only | ✓ SATISFIED | `meta/meta.go`, `TestMetaPackageDependenciesAreStdlibOnly` |
| META-02 | 01-04 | Production dep graph is `ent`-only, machine-checked | ✓ SATISFIED | `deps_test.go#TestNoTransportDeps` — programmatically derived allowlist |

No orphaned requirements: all 15 IDs listed in the phase's requirement set (CORE-01..12, SM-01,
META-01, META-02) appear in exactly one plan's `requirements:` frontmatter field, and REQUIREMENTS.md's
traceability table marks all 15 as "Phase 1 / Complete" — consistent with the phase directory.

### Anti-Patterns Found

None. Searched all hand-written `.go` files (excluding generated `internal/testdata/ent/` output)
for `TBD|FIXME|XXX|TODO|HACK|PLACEHOLDER` and for "not yet implemented"/"coming soon" phrasing —
zero matches. `Activity`/`Emit`'s `run` closures do panic if invoked directly, but this is a
documented, deliberate defensive guard (unreachable because `Exec` refuses durable-only flows
before executing anything) rather than an unfinished stub — confirmed by direct code reading and
by `TestRequiresDurableRunCancelOrderFlow`'s zero-invocation assertion.

### Known Workaround (Not a Defect — Carried Forward as a Risk Note)

Per the scope note for this verification: the fixture `Order` schema declares a no-op passthrough
`Hooks()` method, and `internal/testdata/entclient/client.go` blank-imports the generated
`ent/runtime` package. This exists to force ent's codegen to route schema-stitching through a
separate `ent/runtime` package (avoiding a genuine `schema → ent → schema` import cycle that
arises because the custom `Flows()` method — unknown to ent's own codegen — imports generated
types). This is documented in 01-01-SUMMARY.md's Deviations section and in code comments on both
files. It is not flagged as a defect per the scope note, but is worth carrying forward: Phase 6's
real transitions-enforcing hook (SM-02) is expected to naturally supersede this workaround, and
Phase 5/6 planning should be aware that any future entflow-generated schema file will need either
a real hook of its own or an explicit accommodation for this same lever.

### Human Verification Required

None. Every must-have truth was verified by direct code reading and by re-running the project's
own automated test suite in this session (not by trusting SUMMARY.md's reported results).

### Gaps Summary

No gaps found. All 4 plans' `must_haves` (truths, artifacts, key_links) and all 5 ROADMAP success
criteria are independently verified against the actual codebase:

- `go build ./...`, `go vet ./...`, and `go test ./... -race -count=1` all exit 0 (verified live).
- `go build ./internal/testdata/compilefail/` exits non-zero naming `Activity` and a signature
  mismatch (verified live) — CORE-04 is a machine-checked compile error, not prose.
- The full worked-example flow (`cancel` DB step, `refund` Activity, `order.cancelled` Emit)
  matches entflow.md §3.1 and is declarable/describable; `Exec` refuses it with
  `ErrRequiresDurableRun` before invoking any closure (verified live).
- A separate DB-only flow (`CommitProof`) proves the roadmap's literal "single transaction, no
  run row" claim end-to-end via `RunInTx` + a non-transactional re-read (verified live).
- `meta` package is stdlib-only; the root package's non-test dependency graph is `ent`-only,
  both enforced by tests that were run live in this session, not merely claimed in SUMMARY.md.
- 73 tests pass, 0 fail, no debt markers, no stub patterns in hand-written source.

Phase 1's goal — a developer can declare and execute a DB-only multi-step flow entirely by hand,
proving the core builder API and step-kind safety rules — is achieved and ready for Phase 2 to
build on.

---
*Verified: 2026-08-08T12:57:28Z*
*Verifier: Claude (gsd-verifier)*
