---
phase: 01-runtime-core
fixed_at: 2026-08-08T14:10:55Z
review_path: .planning/phases/01-runtime-core/01-REVIEW.md
iteration: 1
findings_in_scope: 6
fixed: 6
skipped: 0
status: all_fixed
---

# Phase 01-runtime-core: Code Review Fix Report

**Fixed at:** 2026-08-08T14:10:55Z
**Source review:** .planning/phases/01-runtime-core/01-REVIEW.md
**Iteration:** 1

**Verification environment:** all gates below (`go build`, `go vet`, `go test -race`,
and the compile-fail fixture check) were run directly in the main checkout at
`/home/user/entflow` on branch `claude/gsd-progress-vkrft6` — no isolated
worktree was created for this run (the launching orchestrator's `git_context`
explicitly instructed committing directly to the current branch with no new
branch creation, which is the documented `workflow.use_worktrees: false`-style
opt-out path). These results are reproducible by running the same four
commands against `HEAD` on that branch.

**Summary:**
- Findings in scope: 6 (WR-01 through WR-06; fix_scope is `critical_warning`, so the three Info findings were out of scope and are recorded below as skipped-by-scope, not attempted)
- Fixed: 6
- Skipped: 0

## Fixed Issues

### WR-01: `%T` on a generic zero value silently prints `<nil>` for interface-typed parameters

**Files modified:** `registry.go`, `result.go`, `exec.go`, `steps.go`, `codec.go`, `registry_test.go`
**Commit:** `7ba3e58`
**Applied fix:** Replaced every `%T` formatting of a generic zero value (`var zero T`) with `reflect.TypeFor[T]()` (or the equivalent type parameter for the site), across all locations the review named: `registry.go`'s `TryUse`, `result.go`'s `Result[T]` type-mismatch error, `exec.go`'s `WithSelfStatus` tx/in mismatch errors, `steps.go`'s `dbAdapter`/`checkAdapter` tx/in mismatch errors, and `codec.go`'s `resolveCodec` mismatch panic. `reflect.TypeFor[T]()` reports the static type parameter directly and is immune to the nil-interface-to-`interface{}` erasure `%T` suffers from. Added `TestTryUseNotProvidedNamesInterfaceType` to `registry_test.go`, which asserts the error message names the interface type (`refunder`) and explicitly asserts it does NOT contain `"<nil>"` — this is the regression test the hint requested; it was verified to fail against the pre-fix code path (the `%T` formatting) before the fix and pass after.

### WR-02: `Retry()` only rejects DB steps, silently accepting Emit steps

**Files modified:** `retry.go`, `activity_test.go`
**Commit:** `3f469d4`
**Applied fix:** Changed `Retry`'s guard from `if s.kind == KindDB` to `if s.kind != KindActivity`, matching the doc comment's stated invariant ("retry policies apply to Activities only") exactly, and updated the panic message to name the actual step kind (`%s`, `s.kind`) rather than hardcoding "DB step". Added `TestRetryOnEmitStepPanics` (Emit-shaped twin of the existing `TestRetryOnDBStepPanics`) to close the untested gap the review identified.

### WR-03: `RetryPolicy`'s validation lives only in `Backoff()` and is trivially bypassed

**Files modified:** `retry.go`, `activity_test.go`
**Commit:** `d045eea`
**Applied fix:** Factored `Backoff`'s two invariant checks (`MaxAttempts >= 1`, `Initial <= Max`) into a shared `validateRetryPolicy(caller string, p RetryPolicy)` helper, called from both `Backoff` (unchanged behavior) and now also from `Retry` before storing the policy on the step. This closes the bypass the review demonstrated: constructing a `RetryPolicy{}` literal directly (skipping `Backoff`) and handing it to `Retry` is now rejected. Added `TestRetryRevalidatesBypassedPolicy`, which constructs exactly the invalid literal the review's Issue text used as its example (`RetryPolicy{MaxAttempts: 0, Initial: time.Hour, Max: time.Second}`) and asserts `Retry` panics on it.

### WR-04: `WithSelfStatus` gets none of `WithCodec`'s eager type-mismatch protection

**Files modified:** `flow.go`, `exec.go`, `exec_test.go`
**Commit:** `a8c951d`
**Applied fix:** Followed the review's suggested approach: `flowConfig` now carries a `selfStatusInType reflect.Type` field, set by `WithSelfStatus[In, TX]` alongside the type-erased adapter it already stores. `New[In]` now compares this recorded type against `reflect.TypeFor[In]()` after applying all options and panics immediately — at the flow's declaration site — if they don't match, giving `WithSelfStatus` the same eager, construction-time mismatch protection `WithCodec`/`resolveCodec` already had. Added `TestNewPanicsOnSelfStatusInTypeMismatch`, which builds a `WithSelfStatus` reader typed for `string` input and passes it to `New[int]`, asserting `New` panics (recovered as an error naming `WithSelfStatus`) rather than deferring the failure to a later `SelfWas`-gated execution.

### WR-05: `Transition()` is rejected on read-only DB steps but not on Activity/Emit steps

**Files modified:** `steps.go`, `activity.go`, `stepoptions.go`, `activity_test.go`
**Commit:** `bf6c805`
**Applied fix:** Centralized the Transition-rejection logic (previously inline in `addDBStep`, applied only to `Query`/`Check`) into a shared `rejectIllegalTransition(s *step, name, constructor string)` helper in `steps.go`. `addDBStep` now calls it for `Query`/`Check` (behavior-preserving), and `activity.go`'s `Activity` and `Emit` constructors now call it too, closing the gap the review identified: all four non-mutating-DB-step-claim cases (`Query`, `Check`, `Activity`, `Emit`) are now covered by one mechanism instead of two independently-maintained checks. Updated `Transition`'s doc comment in `stepoptions.go` to describe the expanded, centralized rejection. Added `TestTransitionOnActivityPanics` and `TestTransitionOnEmitPanics`, using the exact examples from the review's Issue text (`Transition("refunded")` on an Activity, `Transition("cancelled")` on an Emit).

### WR-06: `TestWhenSelfWasSkipsWhenMismatched` does not actually verify the behavior its comment claims

**Files modified:** `exec_test.go`
**Commit:** `803c8f7`
**Applied fix:** Rewrote the test to capture a live `ctx` from inside an `observer` step declared `After("guarded")`, then assert `Result[int](observerCtx, "guarded")` errors — exactly the pattern `TestResultAccessibleAcrossSteps` already uses correctly for the cross-step-read case, and the pattern the review's own Fix section specified. The previous version asserted against a freshly-constructed `context.Background()` that was never involved in the `Exec` call, so it passed vacuously regardless of the actual behavior. Per the guardrail's explicit instruction not to weaken the assertion into a no-op, I verified the new test is load-bearing: I temporarily reintroduced the bug it targets (recording a result for a skipped step) directly in `exec.go`, confirmed the rewritten test fails against that broken behavior (`Error: An error is expected but got nil`), then reverted the temporary change (never committed) and confirmed the full suite passes again against the real, correct implementation. Also strengthened the assertion to check `errors.Is(resultErr, entflow.ErrUnknownStep)`, not just "any error."

## Skipped Issues (out of scope by fix_scope)

`fix_scope` for this run is `critical_warning`; the following Info-tier findings from `01-REVIEW.md` were not in scope and were not attempted. They remain open for a future `--fix all` pass or manual follow-up.

### IN-01: `After(...)` does not deduplicate repeated dependency names

**File:** `stepoptions.go:12-16`
**Reason:** Out of scope — `fix_scope` is `critical_warning`; Info findings are excluded.
**Original issue:** `After(names ...string)` unconditionally appends to `s.dependsOn`, so calling `After("a")` twice accumulates duplicates that `Meta()`'s `DependsOn` and `Describe()`'s output surface verbatim.

### IN-02: The compile-fail fixture proves an arity mismatch, not specifically "a transaction parameter is rejected"

**File:** `internal/testdata/compilefail/activity_tx.go:23-28`
**Reason:** Out of scope — `fix_scope` is `critical_warning`; Info findings are excluded.
**Original issue:** The fixture's 4-parameter closure fails to compile because of arity mismatch generically, not because Go specifically detected a transaction-shaped type being smuggled in; the underlying CORE-04 guarantee is sound, but the fixture's precision claim is broader than what it demonstrates.

### IN-03: `RunInTx`'s escaped-panic recover mislabels a panic from `tx.Commit()` as "panic escaped Exec"

**File:** `exec.go:241-264`
**Reason:** Out of scope — `fix_scope` is `critical_warning`; Info findings are excluded.
**Original issue:** The deferred recover in `RunInTx` spans the whole function body including `tx.Commit()`, so a panic from `Commit` itself is mislabeled as having escaped `Exec`. Diagnostics-only; behavior (rollback attempt + re-panic) is unaffected.

---

_Fixed: 2026-08-08T14:10:55Z_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_
