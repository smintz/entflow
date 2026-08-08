---
phase: 01-runtime-core
reviewed: 2026-08-08T00:00:00Z
depth: standard
files_reviewed: 34
files_reviewed_list:
  - flow.go
  - step.go
  - exec.go
  - steps.go
  - activity.go
  - stepoptions.go
  - transitions.go
  - registry.go
  - codec.go
  - json.go
  - result.go
  - errors.go
  - retry.go
  - describe.go
  - meta/meta.go
  - exec_test.go
  - steps_test.go
  - activity_test.go
  - registry_test.go
  - codec_test.go
  - result_test.go
  - meta_test.go
  - describe_test.go
  - extractability_test.go
  - callshapes_test.go
  - deps_test.go
  - transitions_graph_test.go
  - internal/testdata/compilefail/activity_tx.go
  - internal/testdata/ent/schema/order.go
  - internal/testdata/ent/schema/order_flows.go
  - internal/testdata/ent/generate.go
  - internal/testdata/entclient/client.go
  - go.mod
  - .github/workflows/ci.yml
findings:
  critical: 0
  warning: 6
  info: 3
  total: 9
status: issues_found
---

# Phase 01-runtime-core: Code Review Report

**Reviewed:** 2026-08-08T00:00:00Z
**Depth:** standard
**Files Reviewed:** 34
**Status:** issues_found

## Summary

Phase 1's runtime core (flow declaration, DB-step/Activity/Emit builders, the
DB-only executor, the dependency-injection registry, the result store, and
the metadata/`Describe()` projection) is well structured and its stated
invariants (D-08 no commit/rollback in `Exec`, D-13 refuse durable-only
flows, D-16 panic-to-error conversion preserving `errors.Is`/`errors.As`,
D-19 metadata carries no closures) are backed by targeted tests that
actually exercise the claimed behavior in most cases. No BLOCKER-level
correctness, security, or data-loss defects were found — no SQL/command
injection surface exists here (all DB access is delegated to
caller-supplied `*ent.Tx`), no secrets are hardcoded, and the panic/recover
and rollback bookkeeping in `Exec`/`RunInTx` were traced against their tests
and hold up.

The issues found are all WARNING or INFO grade: (1) a real asymmetry between
`Retry()`'s guard (only rejects `KindDB`, silently accepting `KindEmit`) and
its own doc comment ("retry policies apply to Activities only"); (2) the
same asymmetry pattern for `Transition()`, which is rejected on read-only DB
constructors (`Query`/`Check`) but not on `Activity`/`Emit`, which never
mutate a status field either; (3) `RetryPolicy`'s `Backoff()`-only
validation is bypassable because every field is exported; (4) a systemic Go
gotcha (`%T` on a nil-valued generic type parameter that happens to be
instantiated with an interface type prints `<nil>`, not the interface name)
that degrades several "descriptive error" guarantees the codebase otherwise
takes pains to uphold; (5) `WithSelfStatus` gets none of `WithCodec`'s eager
mismatch protection; and (6) one existing test
(`TestWhenSelfWasSkipsWhenMismatched`) asserts against an unrelated,
freshly-constructed context and therefore does not actually prove the
behavior its comment claims. None of these block Phase 1's stated scope
(DB-only execution, no worker/codegen), but several should be fixed before
Phase 6's cross-validator and Phase 4's Emit executor start relying on
`Meta()` as ground truth.

## Warnings

### WR-01: `%T` on a generic zero value silently prints `<nil>` for interface-typed parameters, defeating "descriptive error" guarantees

**File:** `registry.go:76` (pattern repeated at `result.go:74`, `exec.go:40,45`, `steps.go:72,77,92,97`, `codec.go:69`)
**Issue:** `TryUse[T]` (and several other generic error paths) format the "wanted type" using `fmt.Errorf("%w: %T", ErrNotProvided, zero)` where `zero` is `var zero T`. When `T` is instantiated with an interface type (a documented, tested use case — see `registry_test.go`'s `TestProvideInterfaceType`, which registers under the `refunder` interface), `zero` is a nil value of that interface type. Converting a nil interface value into the `interface{}` that `fmt`'s variadic parameter requires loses all static type information — `%T` prints `<nil>`, not the interface's name. Confirmed by direct repro:
```go
type Fooer interface{ Foo() }
func zeroOf[T any]() T { var z T; return z }
fmt.Printf("%T\n", zeroOf[Fooer]())  // prints "<nil>"
```
So `entflow.Use[refunder](ctx)` on an unprovided registry produces `"entflow: type not provided: <nil>"` instead of naming `refunder` — exactly the debuggability the codebase's own comments call out as load-bearing (`dbAdapter`'s comment: "reports both the actual and wanted types via %T on failure"). The same pattern affects `Result[T]` when `T` is an interface, and `dbAdapter`/`checkAdapter`/`WithSelfStatus` when `TX` is instantiated as the `entflow.Tx` interface itself rather than a concrete `*ent.Tx`.
**Fix:**
```go
// registry.go
return zero, fmt.Errorf("%w: %s", ErrNotProvided, reflect.TypeFor[T]())
```
`reflect.TypeFor[T]()` reports the static type parameter directly and is immune to the nil-interface-to-interface{} erasure `%T` suffers from. Apply the same substitution wherever a generic zero value's type name is reported in an error message.

### WR-02: `Retry()` only rejects DB steps, silently accepting Emit steps despite its own doc comment

**File:** `retry.go:38-46`
**Issue:** `Retry`'s doc comment states "Retry attaches a retry policy to an Activity step... retry policies apply to Activities only" and the code guards against misuse with `if s.kind == KindDB { panic(...) }`. This only rejects `KindDB`; it does not reject `KindEmit`. Declaring `entflow.Emit(f, "topic", entflow.Retry(entflow.Backoff(5, time.Second, time.Minute)))` compiles and silently records a `RetryPolicy` on an Emit step — data that `Meta()`/`Describe()` will faithfully report as though it means something, even though nothing in the codebase (now or per the design doc) consumes an Emit step's retry policy the way an Activity's is meant to be consumed. No test (`activity_test.go`'s `TestRetryOnDBStepPanics` only covers the DB case) catches this, confirming the gap is untested as well as unenforced.
**Fix:**
```go
func Retry(p RetryPolicy) StepOption {
	return func(s *step) {
		if s.kind != KindActivity {
			panic(fmt.Errorf("entflow: step %q: Retry is not valid on a %s step; retry policies apply to Activities only", s.name, s.kind))
		}
		rp := p
		s.retry = &rp
	}
}
```

### WR-03: `RetryPolicy`'s validation lives only in `Backoff()` and is trivially bypassed

**File:** `retry.go:8-30`
**Issue:** `Backoff(maxAttempts, initial, max)` is the documented, sanctioned constructor and validates `maxAttempts >= 1` and `initial <= max`, panicking otherwise. But `RetryPolicy`'s fields (`MaxAttempts`, `Initial`, `Max`) are all exported (necessarily, since `meta.RetryMeta` and `Describe()` read them), so nothing stops a caller from constructing `entflow.RetryPolicy{MaxAttempts: 0, Initial: time.Hour, Max: time.Second}` directly and handing it to `Retry(...)`, which stores it with no re-validation. The invariant "every `RetryPolicy` attached to a step has `MaxAttempts >= 1` and `Initial <= Max`" therefore does not actually hold universally — only for callers who go through `Backoff()`.
**Fix:** Re-validate inside `Retry()` before storing (or add an internal-only validity check called from both `Backoff()` and `Retry()`), e.g.:
```go
func Retry(p RetryPolicy) StepOption {
	return func(s *step) {
		if s.kind != KindActivity {
			panic(...)
		}
		if p.MaxAttempts < 1 {
			panic(fmt.Errorf("entflow: step %q: Retry: maxAttempts must be at least 1, got %d", s.name, p.MaxAttempts))
		}
		if p.Initial > p.Max {
			panic(fmt.Errorf("entflow: step %q: Retry: initial (%s) must not exceed max (%s)", s.name, p.Initial, p.Max))
		}
		rp := p
		s.retry = &rp
	}
}
```

### WR-04: `WithSelfStatus` gets none of `WithCodec`'s eager type-mismatch protection

**File:** `exec.go:34-50` (compare `codec.go:52-72`)
**Issue:** `WithCodec[In any](c Codec[In]) FlowOption` is checked eagerly: `resolveCodec` type-asserts `cfg.codec.(Codec[In])` inside `New[In]` and panics immediately, at the flow's declaration site, if the codec was built for the wrong `In`. `WithSelfStatus[In, TX any](fn ...) FlowOption`, by contrast, has its own independent type parameters `In`/`TX` that are inferred purely from the closure literal passed to it — nothing ties them to the `In` of the `*FlowOf[In]` the option will eventually be applied to (that link doesn't exist until `New[In]` calls the option against a `*flowConfig`, which is not generic). A caller who accidentally supplies a `WithSelfStatus` reader typed for the wrong input (e.g. copy-pasted from a different flow) gets no compile error and no construction-time panic — the mismatch first surfaces as a generic `"tx has type %T, want %T"` / `"in has type %T, want %T"` runtime error, and only on executions that actually reach a `SelfWas` condition (`needsSelfStatus` gates the read). This is weaker than the guarantee `WithCodec` provides and inconsistent with D-17's "catch mismatches early" philosophy for the sibling case that already exists in this file (comment at exec.go:29 explicitly calls out that this "follows the same erasure pattern as the step constructors" but does not actually get the same eager check codec.go does).
**Fix:** At minimum, document the asymmetry explicitly (so a future reader doesn't assume `WithSelfStatus` is validated like `WithCodec`); better, have `New[In]` sanity-check `cfg.selfStatus` by invoking it once against a zero `In`/best-effort `tx` is not viable (no tx exists yet), but the flow's `In` type is known at `New[In]`'s call site — consider threading an explicit type token similarly to how `resolveCodec` does, e.g. storing an `expectedInType reflect.Type` alongside `cfg.selfStatus` and checking it against `reflect.TypeFor[In]()` inside `New`.

### WR-05: `Transition()` is rejected on read-only DB steps but not on Activity/Emit steps

**File:** `steps.go:54-56` (guard), `activity.go:18-43` and `activity.go:54-75` (no guard)
**Issue:** `addDBStep` panics if a `Query` or `Check` step declares `Transition(...)`, with the stated rationale "a read-only step cannot legally claim a status transition." `Activity` and `Emit` steps also never mutate the database directly (Activities call out to an external system via `Use[T]`; Emits are outbox writes) — by the same rationale, a `Transition(...)` claim on either is equally nonsensical, yet neither constructor applies any such guard; `opts` are simply looped over with `opt(s)` and `s.transition` is set unconditionally. A schema author can write `entflow.Emit(f, "order.cancelled", entflow.Transition("cancelled"))` and it will compile, execute (once Emit becomes executable in Phase 4) with no complaint, and appear in `Meta()`/`Describe()` as a legitimate transition claim that Phase 6's bidirectional cross-validator (SM-03/SM-04) will have to specifically guard against, since Phase 1 does not.
**Fix:** Apply the same declaration-time panic in `Activity`/`Emit` that `addDBStep` applies to `Query`/`Check`, or centralize the check (e.g. a shared `rejectTransitionUnless(s, allowedConstructors...)` helper) so all four non-mutating-DB-step-claim cases (`Query`, `Check`, `Activity`, `Emit`) are covered by one mechanism instead of two independently-maintained ones.

### WR-06: `TestWhenSelfWasSkipsWhenMismatched` does not actually verify the behavior its comment claims

**File:** `exec_test.go:196-214`
**Issue:** The test's comment states: "proves `When(SelfWas(...))` skips the step, and records no result for it, when the snapshot doesn't match," and the final assertion is:
```go
_, resultErr := entflow.Result[int](context.Background(), "guarded")
require.Error(t, resultErr, "a skipped step must record no result")
```
`Exec`'s per-execution result store is derived internally (`ctx, rs := withResults(ctx)`) and is never returned to the caller — `Exec`'s signature is `(ctx, tx, in) error`. The `context.Background()` passed to `entflow.Result` here is a brand-new context that was never involved in the `Exec` call at all, so it carries no result store regardless of what `Exec` did internally. This assertion returns `ErrNoResultStore` unconditionally — it would pass identically even if the framework had a bug that *did* record a result for a skipped step, because the test never inspects the actual store `Exec` used. This gives false confidence in the "skipped step records no result" invariant; nothing in the current suite actually verifies it from outside `Exec`.
**Fix:** Capture the live `ctx` from inside a step closure that runs *after* the guarded step (as `TestResultAccessibleAcrossSteps` already does correctly for cross-step `Result[T]` reads), then assert against that captured `ctx`:
```go
var ran bool
var checkCtx context.Context
f := entflow.New[int]("Guarded", entflow.WithSelfStatus(...))
entflow.UpdateSelf(f, "guarded", func(ctx context.Context, tx *txCounter, in int) (int, error) {
	ran = true
	return in, nil
}, entflow.When(entflow.SelfWas("paid")))
entflow.UpdateSelf(f, "observer", func(ctx context.Context, tx *txCounter, in int) (int, error) {
	checkCtx = ctx
	return in, nil
}, entflow.After("guarded"))

err := f.Exec(context.Background(), &txCounter{}, 1)
require.NoError(t, err)
require.False(t, ran)
_, resultErr := entflow.Result[int](checkCtx, "guarded")
require.Error(t, resultErr)
```

## Info

### IN-01: `After(...)` does not deduplicate repeated dependency names

**File:** `stepoptions.go:12-16`
**Issue:** `After(names ...string)` unconditionally appends to `s.dependsOn`. Calling `After("a")` twice on the same step (or `After("a", "a")`) accumulates `["a", "a"]`. `topoOrder`'s cycle detection tolerates this harmlessly (a `black`-state re-visit short-circuits), but `Meta()`'s `DependsOn` and `Describe()`'s `depends_on:` line will show the duplicate, which is unexpected input for Phase 5's AST-based extraction and any future consumer that assumes `DependsOn` is a set.
**Fix:** Deduplicate in `After` or in `stepMetaOf` before returning `DependsOn`.

### IN-02: The compile-fail fixture proves an arity mismatch, not specifically "a transaction parameter is rejected"

**File:** `internal/testdata/compilefail/activity_tx.go:23-28`, exercised by `activity_test.go:113-120`
**Issue:** The fixture's closure is `func(ctx, tx fakeTx, self fakeEnt, att entflow.Attempt) (...)` — four parameters — against `Activity`'s fixed three-parameter shape `func(ctx, self Ent, att Attempt) (...)`. This fails to compile because of parameter-count mismatch, which the Go compiler reports as "does not match" regardless of what the inserted parameter's type is; any extraneous parameter of any type would fail identically. The underlying CORE-04 guarantee is genuinely structural regardless (`Activity`'s closure shape is fixed-arity, so no tx can be smuggled in without changing arity), but the fixture and its comment claim to specifically demonstrate a transaction-handle exclusion, when what it actually demonstrates is a generic arity/type mismatch. This is a minor precision gap in an otherwise sound negative test, not a defect in the guarantee itself.
**Fix:** Optional — no action required for correctness, but consider a second fixture (or a note in the comment) that makes explicit that the guarantee comes from `Activity`'s fixed parameter count rather than from type-level exclusion of anything "tx-shaped."

### IN-03: `RunInTx`'s escaped-panic recover mislabels a panic from `tx.Commit()` as "panic escaped Exec"

**File:** `exec.go:241-264`
**Issue:** The `defer`/`recover` block set up in `RunInTx` spans the entire function body — including the final `return tx.Commit()` — not just the call to `f.Exec(...)`. If `tx.Commit()` itself panics (an unusual but possible case for a caller-supplied `Tx` implementation), the same recover fires and formats the error as `"entflow: panic escaped Exec: %v ..."`, which is misleading since the panic did not originate inside `Exec`. This is a diagnostics-only nit; behavior (rollback attempt + re-panic) is still safe.
**Fix:** Narrow the deferred recover to wrap only the `f.Exec(...)` call (e.g. via a helper function), or adjust the message to say "panic escaped RunInTx" rather than naming `Exec` specifically.

---

_Reviewed: 2026-08-08T00:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
