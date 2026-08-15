# Phase 2: Durability - Pattern Map

**Mapped:** 2026-08-15
**Files analyzed:** 17 (new/modified)
**Analogs found:** 17 / 17 (all resolve to Phase 1 in-repo analogs; no external/no-analog files)

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|---|---|---|---|---|
| `run.go` (`entflow.RunMixin()`) | model (ent.Mixin) | CRUD | `internal/testdata/ent/schema/order.go` (`Order` schema, `Fields()`) | role-match (schema pattern, not a mixin itself — no existing mixin in repo) |
| `runstore.go` (`entflow.RunStore` port) | service (port interface, tx-erased) | CRUD | `exec.go`'s `Tx`/`TxOpener[T]` interfaces + `WithSelfStatus`'s erasure | exact (erasure pattern is identical; no existing interface-port file, but the shape is copy-exact) |
| `engine.go` (`entflow.Engine`, `entflow.Start[In]`) | service (package-level generic constructor) | request-response | `exec.go`'s `RunInTx[In, T]` | exact |
| `ownerref.go` (`entflow.WithOwnerRef[In, ID]`) | utility (FlowOption, type-erased) | transform | `exec.go`'s `WithSelfStatus[In, TX]` | exact |
| `selfloader.go` (`entflow.WithSelfLoader`) | utility (FlowOption, type-erased) | CRUD (re-read) | `exec.go`'s `WithSelfStatus[In, TX]` | exact |
| `workflowmarker.go` (marker type + `IsWorkflow(ctx)`) | utility (context key/guard) | request-response | `result.go`'s `resultsKey{}` + `withResults`/`Result[T]` context-key pattern | exact |
| `worker/claim.go` (`ClaimStrategy` + PG/MySQL/SQLite impls) | service (raw-SQL claim query) | CRUD (batch-of-one claim) | none in-repo — **genuinely new**; nearest structural analog is `exec.go`'s `RunInTx` (tx-open/commit/rollback discipline) plus RESEARCH.md's verified `sql/execquery` mechanism against `internal/testdata/ent/client.go`/`tx.go` | no analog (new artifact) |
| `worker/dbstep.go` (claim-execute-advance, one step per tx) | service (orchestrator) | CRUD/transform | `exec.go`'s `Exec`/`runStep`/`topoOrder` (resumable single-step entry point built from these) | role-match (this *is* `Exec`'s loop, refactored to a step-*k* entry point) |
| `worker/loop.go` (poll+jitter, nudge channel, shutdown) | service (long-running loop) | event-driven | none in-repo — new; nearest analog is `RunInTx`'s commit/rollback/panic discipline for the per-iteration tx | no analog (new artifact) |
| `worker/span.go` (persist/restore trace context) | utility (transform) | transform | none in-repo — new; nearest analog is `codec.go`'s `Marshal`/`Unmarshal` round-trip discipline (same "opaque bytes column, error never embeds raw bytes" pattern) | role-match |
| `worker/worker.go` (`worker.New`, `Options`, dialect validation) | config/service (constructor + validation-at-construction) | request-response | `flow.go`'s `New[In]` (construction-time validation + panic-on-mismatch pattern, e.g. `WithSelfStatus` type check) | role-match |
| `internal/crashpoint/` (tier-1 logical crash-point registry) | utility (nil-check registry) | event-driven | none in-repo — new; nearest analog is `errors.go`'s sentinel-error style (`ErrRequiresDurableRun`) for "a single named check that's a no-op by default" | no analog (new artifact) |
| `internal/testdata/ent/schema/cancelorderflowrun.go` (hand-written run entity) | model (ent schema) | CRUD | `internal/testdata/ent/schema/order.go` + `order_flows.go` | exact |
| `internal/testdata/crashworker/` (~20-line main, tier-2 kill target) | config (binary entrypoint) | request-response | none in-repo — new; RESEARCH.md's D-48 explicitly says "no generated main helper," hand-written | no analog (new artifact) |
| `worker_test.go` / `worker/*_test.go` | test | request-response | `exec_test.go`, `result_test.go` (unexported-seam test style, `package entflow`) | exact |
| `crashsim_test.go` (tier 1 + tier 2 harness) | test | event-driven | `deps_test.go` (external-tool-invocation test style, `package entflow_test`, `exec.Command`) | role-match |
| `internal/testdata/ent/generate.go` (add `--feature sql/execquery`) | config | n/a | itself (one-line modification) | exact (modification, not new file) |

## Pattern Assignments

### `run.go` — `entflow.RunMixin()`

**Analog:** `internal/testdata/ent/schema/order.go` (schema shape) + RESEARCH.md's verified `ent.Mixin` interface (`schema/mixin/mixin.go`, cited directly — no in-repo mixin exists yet, this is genuinely the first one).

**Imports pattern** (mirror `order.go` lines 1-8):
```go
import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/mixin"
)
```

**Core pattern** — embed `mixin.Schema`, override `Fields()`/`Indexes()` only, per D-23/D-28 and RESEARCH.md's Pattern 3 sketch (`entflow.RunMixin()`, lines ~603-638 of 02-RESEARCH.md). Enum stored values use `NamedValues` for the `failed:<step>` colon form (D-27), following the same `field.Enum(...).Values(...)` declaration style already used in `order.go` lines 24-33, but with `NamedValues` instead of `Values` so the Go identifier (`FailedRefund`) decouples from the stored value (`failed:refund`).

**Do not declare `Edges()`** on the mixin — D-23/D-26 make the owner edge the concrete schema's job (`WithOwnerRef`-driven), exactly as `order.go`'s `Order` schema owns its own fields with no mixin today.

**Partial index caveat (Postgres/SQLite only, not MySQL)** — see Shared Patterns below.

---

### `internal/testdata/ent/schema/cancelorderflowrun.go` — hand-written run entity

**Analog:** `internal/testdata/ent/schema/order.go` + `order_flows.go`

**Core pattern** (mirror `order.go` lines 15-34): embed `entflow.RunMixin()` via `Mixin()`, add one owner edge to `Order`, and — critically — copy the `Hooks()` no-op passthrough at `order.go` lines 36-58 verbatim (adapted to the run schema) if the run schema's package also needs the `ent`↔`schema` import-cycle avoidance. Verify at implementation time whether the run schema needs its own `Hooks()`/`Policy()` no-op or whether `Order`'s existing one already forces the split for the whole package.

**Policy() pattern** (OPS-02, D-45) — new code, but follows RESEARCH.md's Pattern 4 (`privacy.Policy{Mutation: ..., Query: ...}` built from `MutationRuleFunc`), checking `entflow.IsWorkflow(ctx)` (from `workflowmarker.go`) for the cancel-guard. No existing `Policy()` in the fixture to copy from — this is the first.

---

### `runstore.go` — `entflow.RunStore` port

**Analog:** `exec.go`'s `Tx`/`TxOpener[T]` interfaces (lines 11-24) for the tx-erasure shape, and `WithSelfStatus` (lines 26-56) for the type-erased-closure-over-`any` pattern applied to a whole method set instead of one function.

**Core pattern** — a plain Go interface with methods taking `tx any` (D-24), mirroring how `Exec(ctx context.Context, tx any, in In)` (exec.go line 81) already erases the transaction type:
```go
type RunStore interface {
	Claim(ctx context.Context, tx any) (runID any, ok bool, err error)
	Load(ctx context.Context, tx any, runID any) (*Run, error)
	Advance(ctx context.Context, tx any, runID any, next StateUpdate) error
	Fail(ctx context.Context, tx any, runID any, step string, cause error) error
	Cancel(ctx context.Context, tx any, runID any) error
	RecordResult(ctx context.Context, tx any, runID any, step string, v any) error
}
```
Exact method names are Claude's Discretion per CONTEXT.md; the tx-erasure and per-flow-adapter shape is locked (D-24).

---

### `engine.go` — `entflow.Engine`, `entflow.Start[In]`

**Analog:** `exec.go`'s `RunInTx[In, T Tx]` (lines 240-270) — same package-level-generic-function-not-method reasoning (Go methods cannot declare type params, per `flow.go` line 55-57's comment).

**Imports pattern** (mirror `exec.go` lines 1-9): `context`, `fmt`, plus whatever `Engine`'s struct needs.

**Core pattern** — `Start[In]` opens a transaction via the `Engine`'s bound `TxOpener`/`RunStore` for flow `f`, inserts a `pending` run (mirrors `RunInTx`'s tx-open → work → commit/rollback discipline exactly, lines 247-270), and returns `*entflow.Run`. Copy `RunInTx`'s two-recover-site discipline (the panic-escape recover at lines 253-260 is a **separate** site from any per-step recover, exactly as `runStep`'s recover (lines 155-178) is separate from `RunInTx`'s) if `Start` ever calls into step execution — likely not needed since `Start` only inserts the row.

**Error handling pattern** (mirror `RunInTx` lines 248-251): wrap transaction-open failures with `fmt.Errorf("entflow: opening transaction: %w", err)`.

---

### `ownerref.go` — `entflow.WithOwnerRef[In, ID]`

**Analog:** `exec.go`'s `WithSelfStatus[In, TX]` (lines 26-56) — copy this pattern exactly, per RESEARCH.md's explicit instruction ("D-26 and D-41 copy this pattern exactly; do not invent a second one").

**Core pattern** (near-verbatim adaptation of `WithSelfStatus`):
```go
func WithOwnerRef[In, ID any](fn func(In) (ID, error)) FlowOption {
	return func(cfg *flowConfig) {
		cfg.ownerRefInType = reflect.TypeFor[In]()
		cfg.ownerRef = func(in any) (any, error) {
			typedIn, ok := in.(In)
			if !ok {
				return nil, fmt.Errorf("entflow: WithOwnerRef: in has type %T, want %s", in, reflect.TypeFor[In]())
			}
			return fn(typedIn)
		}
	}
}
```
Same declaration-time mismatch check belongs in `New[In]` (mirror `flow.go` lines 82-84's `cfg.selfStatus != nil && cfg.selfStatusInType != reflect.TypeFor[In]()` panic).

**Comment style** — copy `WithSelfStatus`'s doc-comment structure (why explicit-not-inferred; "codegen must never infer facts by inspecting closure bodies") verbatim in spirit — this is the same invariant (D-26 says exactly this).

---

### `selfloader.go` — `entflow.WithSelfLoader`

**Analog:** `exec.go`'s `WithSelfStatus[In, TX]` (lines 26-56) — sibling pattern per D-41's own wording ("a sibling of `WithSelfStatus`").

**Core pattern** — same erasure shape as `WithOwnerRef` above, but the closure signature returns the live self entity: `func(ctx context.Context, tx TX, id ID) (Self, error)`, invoked fresh inside `worker/dbstep.go`'s per-step transaction (never cached/JSON-round-tripped, per D-41/D-40's asymmetry — document this contrast explicitly, since `result.go`'s `Result[T]` (see below) is the opposite: always-JSON).

---

### `workflowmarker.go` — unexported marker + `IsWorkflow(ctx)`

**Analog:** `result.go`'s `resultsKey{}` context-key pattern (lines 24-27, 40-43) — an unexported empty-struct key type, set via an unexported setter, read via a narrow public accessor.

**Core pattern** (mirror `result.go`'s `resultsKey{}` / `withResults` shape):
```go
type workflowMarkerKey struct{}

// setWorkflow is unexported — only worker/loop.go's transaction-opening path may call it.
func setWorkflow(ctx context.Context) context.Context {
	return context.WithValue(ctx, workflowMarkerKey{}, true)
}

// IsWorkflow is the ONLY public surface for this marker (D-44).
func IsWorkflow(ctx context.Context) bool {
	v, _ := ctx.Value(workflowMarkerKey{}).(bool)
	return v
}
```
**Test requirement (D-44):** a test asserting no exported API can set the marker — mirror `deps_test.go`'s style of a machine-checked invariant (e.g. reflect over the package's exported symbol set, or `go/types`-based scan) rather than a hand-maintained list.

---

### `worker/claim.go` — `ClaimStrategy` + dialect implementations

**No in-repo analog** — this is the phase's genuinely new artifact. Build directly from RESEARCH.md's verified Code Example (lines 556-601 of 02-RESEARCH.md), which is itself grounded in `internal/testdata/ent/client.go:41-133` and `tx.go:12-24` (read this session, confirming `config.driver` is unexported and `Client.Tx` wraps the same driver into the returned `*Tx`).

**Core pattern** (from RESEARCH.md, verified against Phase 1's actual generated code):
```go
tx, err := client.Tx(ctx)
...
rows, err := tx.QueryContext(ctx, `SELECT id FROM ... FOR UPDATE SKIP LOCKED LIMIT 1`)
...
run, err := tx.CancelOrderFlowRun.Get(ctx, id) // same tx — already locked
...
_, err = tx.CancelOrderFlowRun.UpdateOneID(id).SetState(...).Save(ctx)
...
return tx.Commit()
```
**Prerequisite:** `internal/testdata/ent/generate.go` must gain `--feature sql/execquery` before this file can be written (RESEARCH.md Pattern 1) — this is the phase's first task, not an afterthought.

**Error handling pattern** — mirror `RunInTx`'s tx-open error wrap (`exec.go` line 250: `fmt.Errorf("entflow: opening transaction: %w", err)`) for consistency with the rest of the codebase's error-message prefix convention (`"entflow: <context>: %w"`, used throughout `exec.go`, `codec.go`, `flow.go`).

---

### `worker/dbstep.go` — claim-execute-advance (resumable single-step `Exec`)

**Analog:** `exec.go`'s `Exec` (lines 81-120), `runStep` (lines 146-178), `topoOrder` (lines 180-238) — this file is structurally `Exec`'s loop body refactored into a step-*k* entry point.

**Core pattern to copy verbatim:**
- `runStep`'s recover boundary (lines 155-178) — unchanged, reused as-is for the single step the worker executes per claim.
- `topoOrder` (lines 180-238) — unchanged, used to answer "what is the next step after `current_step`."
- `evalConditions`/`needsSelfStatus` (lines 122-144) — reused for condition evaluation against the persisted `SelfWas` snapshot (D-42).

**Integration point:** replace `Exec`'s all-steps `for _, s := range order { ... }` loop (lines 107-118) with "find the step at `order[indexOf(run.CurrentStep)+1]`, run exactly that one, then persist the new `current_step`/`results`/`state` in the same tx" — same `runStep`/`putResult` calls, single iteration.

---

### `worker/loop.go` — poll interval, jitter, nudge channel, graceful shutdown

**No in-repo analog** — new. Nearest structural pattern is `RunInTx`'s commit/rollback/panic discipline (`exec.go` lines 247-270), applied once per poll iteration instead of once per call.

**Core pattern (from RESEARCH.md D-33/D-49):** a `time.Ticker`-driven loop with `±25%` jitter (mandatory), a `chan struct{}` nudge channel for in-process wakeup, and `context.Context` cancellation for shutdown — "stop claiming, let in-flight commit, then exit; past a deadline, cancel step contexts so their transactions roll back" (D-49). No existing loop code in the repo to copy from; follow the `RunInTx` recover-on-panic discipline for each iteration's transaction.

---

### `worker/span.go` — persist/restore trace context (D-59)

**Analog:** `codec.go`'s `Marshal`/`Unmarshal` round-trip discipline (lines 24-47) — same "serialize to opaque column bytes, error never embeds raw content" pattern (`codec.go` lines 38-40's comment: "never embeds the raw input bytes").

**Core pattern** (from RESEARCH.md lines 642-652, verified against `trace/trace.go`/`context.go`):
```go
sc := trace.NewSpanContext(trace.SpanContextConfig{
	TraceID: mustTraceIDFromHex(...),
	SpanID:  mustSpanIDFromHex(...),
	Remote:  true,
})
ctx = trace.ContextWithRemoteSpanContext(ctx, sc)
ctx, span := tracer.Start(ctx, "workflow.CancelOrder.refund")
defer span.End()
```
Use the hand-rolled hex two-field carrier (`trace.TraceID.String()`/`SpanID.String()`), **not** `propagation.TraceContext` — RESEARCH.md's Load-Bearing Correction to D-57 is explicit that pulling `propagation` reopens the root-`otel`-module dependency footprint the `trace`-only exception is meant to avoid.

---

### `worker/worker.go` — `worker.New`, `Options`, dialect validation

**Analog:** `flow.go`'s `New[In]` (lines 77-91) — construction-time validation-and-panic/error pattern (the `WithSelfStatus` type mismatch check at lines 82-84 is the model for `worker.New`'s dialect-mismatch check, D-37: fail at construction, not lazily at first claim).

**Core pattern** (mirror `New[In]`'s shape): apply functional options, validate eagerly, return a descriptive error naming the offending value (mirror the error text style of `flow.go` line 83: `fmt.Errorf("entflow: New[%s](%q): WithSelfStatus supplied ... which does not match", ...)`) — for `worker.New`, name the dialect and point at the dialect-matrix doc (D-37), and return a named error (not silently serialize) if `Concurrency > 1` on SQLite (D-36).

---

### `worker_test.go`, `worker/*_test.go`

**Analog:** `exec_test.go`, `result_test.go` — `package entflow` (unexported-seam access) is used wherever the test needs to reach an unexported field/function (e.g. `resultStore`, `step.run`); `package entflow_test` (mirror `deps_test.go`) is used for black-box/external-process tests (mirrors the crash harness's subprocess-invocation style).

---

### `crashsim_test.go` (tier 1 + tier 2)

**Analog:** `deps_test.go`'s `exec.Command`/`t.Skip`-style external-tool invocation (lines 93-102's `runGoListDeps` helper) is the closest existing pattern for tier 2's `exec.Command`-launched `crashworker` subprocess plus `cmd.Process.Kill()`. No in-repo testcontainers usage exists yet — this is new, but the "helper function wraps `exec.Command`, `require.NoError` with combined output on failure" shape should be copied from `runGoListDeps`.

**Do not** rely on `testcontainers.SkipIfProviderIsNotHealthy` alone (RESEARCH.md Pitfall, testcontainers-go#2859) — write a custom Docker-ping probe with an explicit `t.Skip(...)`, following `deps_test.go`'s general style of explicit, descriptive `require`/skip messages.

---

## Shared Patterns

### Type erasure at the entflow↔generated-code boundary
**Source:** `exec.go`'s `WithSelfStatus[In, TX]` (lines 26-56), `Tx`/`TxOpener[T]` (lines 11-24)
**Apply to:** `runstore.go` (`RunStore` port), `ownerref.go` (`WithOwnerRef`), `selfloader.go` (`WithSelfLoader`), `worker/claim.go` (tx as `any` throughout)
```go
// The recurring shape: capture a typed closure at declaration time, store an
// any-erased adapter, resolve back with a comma-ok assertion at call time —
// never a bare assertion that panics.
typedTx, ok := tx.(TX)
if !ok {
	return "", fmt.Errorf("entflow: WithSelfStatus: tx has type %T, want %s", tx, reflect.TypeFor[TX]())
}
```

### Package-level generic functions, never methods, for a second type parameter
**Source:** `exec.go`'s `RunInTx[In any, T Tx]` (line 247); `flow.go` line 55-57's comment explaining why
**Apply to:** `entflow.Start[In]` (`engine.go`), any RunStore helper needing both `In` and an ID/entity type parameter

### Error message prefix convention
**Source:** used consistently across `exec.go`, `codec.go`, `flow.go`, `result.go`
**Apply to:** every new file
```go
fmt.Errorf("entflow: <short context>: %w", err)
```

### Recover-boundary discipline: one recover site per concern, never merged
**Source:** `exec.go`'s `runStep` (step-closure panic → `*StepError`) vs. `RunInTx` (bookkeeping panic around commit/rollback) — two **separate** `defer`/`recover` sites, documented as deliberately not collapsed (lines 240-246's comment)
**Apply to:** `worker/dbstep.go` (reuse `runStep`'s recover as-is) and `worker/loop.go` (needs its own separate recover around each iteration's tx commit/rollback, mirroring `RunInTx`'s, not `runStep`'s)

### Doc-comment style: explain *why*, cite the decision ID, name the invariant it protects
**Source:** every Phase 1 file (e.g. `exec.go` lines 26-40, `flow.go` lines 30-46)
**Apply to:** all new files — comments should cite the relevant D-## decision from CONTEXT.md, not just describe the code

### Machine-checked invariants over documented ones
**Source:** `deps_test.go` (`TestNoTransportDeps`, computed allowlist, not hand-curated)
**Apply to:** D-44's "no exported API can set the workflow marker" test, D-38's claim-strategy conformance suite

## No Analog Found

Files with no close match in the codebase — build directly from RESEARCH.md's verified mechanics, cited above per file:

| File | Role | Data Flow | Reason |
|---|---|---|---|
| `worker/claim.go` | service | CRUD (claim) | First raw-SQL-through-ent-tx code in the repo; RESEARCH.md's Code Example (verified against `client.go`/`tx.go`) is the concrete template, not an in-repo analog |
| `worker/loop.go` | service | event-driven | First long-running poll loop in the repo; `RunInTx`'s tx discipline is the nearest partial pattern |
| `worker/span.go` | utility | transform | First OTel integration in the repo; `codec.go`'s round-trip discipline is the nearest partial pattern |
| `internal/crashpoint/` | utility | event-driven | First crash-injection registry in the repo; `errors.go`'s sentinel-error style is the nearest partial pattern |
| `internal/testdata/crashworker/` | config | request-response | First standalone `main` binary in the repo; explicitly hand-written per D-48, no generated-`main` precedent exists yet (that's Phase 6) |

## Metadata

**Analog search scope:** repository root (`*.go`), `internal/testdata/ent/schema/`, `internal/testdata/ent/` generated client (`client.go`, `tx.go`, `generate.go`)
**Files scanned:** `exec.go`, `flow.go`, `result.go`, `codec.go`, `step.go`, `errors.go`, `deps_test.go`, `internal/testdata/ent/schema/order.go`, `internal/testdata/ent/schema/order_flows.go`, `internal/testdata/ent/generate.go` (plus RESEARCH.md/CONTEXT.md's own verified reads of `client.go`/`tx.go` cited inline)
**Pattern extraction date:** 2026-08-15
</content>
