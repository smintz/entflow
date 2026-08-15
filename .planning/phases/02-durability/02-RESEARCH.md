# Phase 2: Durability - Research

**Researched:** 2026-08-15
**Domain:** Postgres-backed durable workflow worker on top of ent (raw-SQL claim query sharing an `*ent.Tx`, hand-written run entity/mixin, crash-simulation harness, OpenTelemetry span propagation across process boundaries)
**Confidence:** MEDIUM-HIGH (the single highest-value mechanical unknown — reaching raw SQL through the same transaction ent's generated client uses — is now HIGH confidence, verified directly against Phase 1's own generated code plus ent's upstream source; the crash-harness and dialect mechanics are MEDIUM, cross-checked web sources; some peripheral items, e.g. exact retryable-error sets per driver, are MEDIUM-LOW and flagged)

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

Decision IDs continue Phase 1's project-wide sequence (Phase 1 ended at D-22). `--auto` mode selected the recommended option for every decision below; the user constrained none of them directly, but they are LOCKED for this research pass — do not re-litigate, only research the mechanics of executing them.

**Run Entity and the Worker↔Application Seam**
- **D-23:** The run entity is hand-written in Phase 2, as `CancelOrderFlowRun` in `internal/testdata/ent/schema/`. entflow ships `entflow.RunMixin()` — an `ent.Mixin` carrying every framework-owned field (state, input, current step, attempt, last error, results, retry_after, trace context, timestamps) — so a user's hand-written run schema is a mixin, a step-derived state enum, and one edge. Phase 5's injected entity must emit exactly this shape. Reversibility: one-way.
- **D-24:** entflow core can never name the application's generated types, so the worker reaches run rows through an `entflow.RunStore` port — a small interface (claim, load, advance, fail, cancel, record-result) implemented by a hand-written per-flow adapter over the generated ent client. The adapter is hand-written in Phase 2 and generated in Phase 6. All adapter methods take the transaction as `any`, matching the type erasure Phase 1 already established for `Exec(ctx, tx any, in In)` and `WithSelfStatus`. Reversibility: costly.
- **D-25:** An `entflow.Engine`, constructed once at boot, binds the flow registry, the per-flow `RunStore`s, and the DI registry. Starting a run is a package-level generic function — `entflow.Start[In](ctx, eng, f, in) (*entflow.Run, error)` — mirroring `RunInTx[In, T]`'s shape. DUR-01's literal `flow.Start(ctx, in)` is NOT achievable in Phase 2 without an ambient global, which D-15 already rejected; Phase 6's codegen restores the design-doc ergonomics. This deviation must be recorded in the phase summary. Reversibility: costly.
- **D-26:** The lineage edge (DUR-02) is declared, never inferred: a new `entflow.WithOwnerRef[In, ID]` `FlowOption` supplies `func(In) (ID, error)`, type-erased exactly like `WithSelfStatus`. The `RunStore` adapter uses it to set the edge to the owning aggregate row.

**Run State Model**
- **D-27:** The run `state` enum is derived from the flow's step graph, per design doc §3.5: `pending`, `running`, `done`, `cancelled`, plus one `failed:<step>` value per step. The literal colon form is achieved with ent's `field.Enum(...).NamedValues(...)`, decoupling the Go constant (`StateFailedRefund`) from the stored value (`failed:refund`). Reversibility: one-way.
- **D-28:** Run row columns (all on `RunMixin`): `state`, `input` (codec bytes), `current_step`, `attempt`, `last_error`, `results` (JSON map, step name → raw result), `retry_after` (nullable), `trace_context`, `created_at`, `updated_at`, `started_at`, `finished_at`. Plus the schema-declared edge from D-26.
- **D-29:** `retry_after` is the single column gating claimability by time. It is also the natural future seat for TIME-01's `wake_at`, but Phase 2 ships no timer semantics.

**Claim Loop**
- **D-30:** The claim transaction *is* the step transaction. One transaction per claim: `SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1` → hydrate the run → execute exactly one DB step's closure → write the step's effect and the run's progress pointer → commit. This is the load-bearing decision of the phase. Reversibility: one-way.
- **D-31:** One run per claim transaction (`LIMIT 1`), never a batch. Concurrency comes from N independent worker goroutines each running their own claim transaction. Default `Concurrency: 4`, configurable; forced to 1 on SQLite (D-36).
- **D-32:** Claim predicate: `state IN ('pending','running') AND (retry_after IS NULL OR retry_after <= now()) ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`. `done`, `cancelled`, and every `failed:<step>` value are terminal and structurally unclaimable.
- **D-33:** Poll interval defaults to 1s with ±25% jitter (mandatory, not optional). An in-process nudge channel lets `Start` wake a co-located worker immediately. Cross-process wakeup (`LISTEN`/`NOTIFY`, `tx.OnCommit`) is deferred to Phase 4.
- **D-34:** The fixture run schema declares a partial index on claimable states using `entsql.IndexWhere`, documented as Postgres-specific required operational guidance. Row pruning/archival is not built in Phase 2.

**Dialect Support Matrix (DUR-08 — resolves the STATE.md Phase 2 blocker)**
- **D-35:** Matrix ratified as: PostgreSQL 12+ — first-class, only dialect the crash-simulation release gate certifies (`FOR UPDATE SKIP LOCKED`). MySQL 8.0.1+ — compatible, uncertified. SQLite — development/test only, single worker. Anything else (including Gremlin) — refused at startup.
- **D-36:** A `ClaimStrategy` interface, selected by dialect at worker construction, isolates the one non-portable query. Postgres/MySQL share the `FOR UPDATE SKIP LOCKED` strategy. SQLite's strategy is a plain `SELECT ... ORDER BY id LIMIT 1` inside a write ("immediate") transaction; `worker.New` returns a named error if `Concurrency > 1` on SQLite.
- **D-37:** Dialect validation happens at `worker.New`, not lazily at first claim.
- **D-38:** A claim-strategy conformance suite (no double-claim; a rolled-back claim leaves the row claimable) runs against all three dialects. The crash-simulation harness runs against Postgres only and says so in its own output.

**Result Persistence and Self Threading**
- **D-39:** Step results persist in the run row's `results` JSON column, written in the same transaction as the step's effect and progress pointer. `entflow.Result[T](ctx, "step")` keeps its Phase 1 signature exactly (D-11).
- **D-40:** Results always round-trip through JSON, even within a single uninterrupted worker pass. A value from `Result[T]` is inert data, not a live ent entity. Reversibility: costly.
- **D-41:** The positionally-threaded `self` entity is re-read from the database at the start of each step's transaction via a declared `entflow.WithSelfLoader` `FlowOption` (sibling of `WithSelfStatus`), never rehydrated from persisted JSON.
- **D-42:** `SelfWas` keeps D-10's entry-snapshot semantics, now persisted on the run row rather than living in memory.

**Cancellation, Privacy, and the Workflow Marker**
- **D-43:** Cancellation (OPS-04) is a state transition, not a signal: a conditional update to `cancelled` permitted only from `pending`/`running`. The step-advance write is additionally guarded (`WHERE state = 'running' AND current_step = ?`) so a completing step can never resurrect a cancelled run.
- **D-44:** entflow ships an unexported context marker type set only inside the worker's transaction-opening path, with `entflow.IsWorkflow(ctx) bool` as the only public surface. A test asserts no exported API can set it.
- **D-45:** entflow core ships no reusable privacy rules in Phase 2. OPS-02 is proven by the fixture run schema declaring an ordinary `Policy()`, and the worker obtains its own viewer from an app-supplied `worker.Options.Context func(context.Context) context.Context`.
- **D-46:** OPS-01 requires no entflow code at all — a run is an ordinary ent entity.

**Worker Packaging, Topology, and Lifecycle**
- **D-47:** The worker lives at `entflow/worker`, a subpackage of the root module (not a satellite). API: `worker.New(eng, worker.Options{...})`, `w.Run(ctx) error`, `w.Shutdown(ctx) error`.
- **D-48:** Both DUR-09 topologies are the same object: in-process is `go w.Run(ctx)`; the dedicated binary is a ~20-line `main`. `internal/testdata/crashworker/` is that worked example and simultaneously tier 2's kill target.
- **D-49:** Graceful shutdown (DUR-10): stop claiming, let in-flight step transactions commit, then exit; past a drain deadline, cancel the step contexts so their transactions roll back. Framing for docs: "shutdown is a crash we happened to be polite about."
- **D-50:** The connection-pool trap is addressed by documentation plus an option: `worker.Options` accepts its own `*ent.Client`.

**DB-Step Retry**
- **D-51:** An attempt counter, `retry_after` backoff scheduling, and a conservative built-in policy (3 attempts, exponential 100ms→2s, jittered) applied only to retryable driver errors — serialization failure, deadlock, connection loss. Every other error fails the run to `failed:<step>` immediately. The declared `Retry(Backoff(...))` policy governs Activities in Phase 3 only.

**Crash-Simulation Harness (TEST-01, TEST-02 — the release gate)**
- **D-52:** Two tiers, named in the harness's own output. Tier 1 — logical crash points, in-process, deterministic, via an `internal/crashpoint` registry that is a single nil check in production builds. Tier 2 — real process kill, a subprocess `crashworker` binary `SIGKILL`ed at a controlled point.
- **D-53:** Tier 2 synchronization is DB-state-driven plus a sentinel file at the target crash point — never a sleep.
- **D-54:** Both tiers run against real Postgres via `testcontainers-go` (one container per package, fresh schema per test). Docker-unavailable skips loudly; `ENTFLOW_REQUIRE_CRASHSIM=1` turns skips into failures for the release-gate CI job.
- **D-55:** Assertions are absence claims, not just terminal-state equality: (effect present ∧ progress present) or (effect absent ∧ progress absent) — never one without the other; plus a side-effect counter asserting no duplicated effect.
- **D-56:** The crash-point matrix is enumerated from the flow's step graph, not hand-listed.

**Observability (OPS-05)**
- **D-57:** entflow core takes a direct dependency on `go.opentelemetry.io/otel/trace` (API only — never the SDK), and D-04's META-02 dependency test gains a narrow, named exception for the `go.opentelemetry.io/otel*` API packages. This is the one decision in this phase most worth a second look — see "Load-Bearing Correction to D-57" below for the exact footprint. Reversibility: costly.
- **D-58:** One root span per run, one child span per step named `workflow.<Flow>.<step>`. IDs never appear in span names; `entflow.run_id`, `entflow.flow`, `entflow.step`, `entflow.attempt`, `entflow.state` are attributes. The `TracerProvider` defaults to the global no-op.
- **D-59:** The run's trace context is persisted on the run row (D-28's `trace_context`) and restored as the parent/link at each claim, because a run's steps may execute in different processes across a crash or re-claim.

### Claude's Discretion

`--auto` mode selected the recommended option for every decision above; the user constrained none of them. Areas where the planner retains latitude:
- The exact method set and naming of the `RunStore` port, provided it stays tx-erased (D-24) and expresses claim / hydrate / advance / fail / cancel / record-result.
- Internal layout within `entflow/worker` (one file vs. several) — ARCHITECTURE.md's suggested `claim.go` / `dbstep.go` / `loop.go` split is a recommendation, not a contract.
- Whether `Engine` is a struct or an interface, and whether per-flow `RunStore`s are registered on it individually or as a map.
- Concrete backoff constants in D-51, and the exact retryable-error classification per driver.
- Test organization for the harness (table-driven vs. generated subtests), as long as D-56's graph-derived enumeration holds.
- Whether the fixture's side-effect counter (D-55) is a dedicated table or a column on `Order`.

### Deferred Ideas (OUT OF SCOPE)

- Activity lease / heartbeat (`claimed_by`, `lease_expires_at`) — ACT-04, Phase 3.
- Manual retry of a failed run — OPS-03, Phase 3.
- Outbox rows, the relay, `tx.OnCommit` nudging, NATS, flow chaining — OUT-01…OUT-05, Phase 4. `ClaimStrategy` (D-36) must stay general enough to point at a different table.
- `LISTEN`/`NOTIFY` cross-process wakeup — revisit alongside Phase 4's `tx.OnCommit` work.
- Terminal-row pruning / archival — D-34's partial index is the Phase 2 mitigation only.
- River backend adapter — SCAL-01, v2.
- Durable timers / `entflow.Sleep` — TIME-01, v2. D-29's `retry_after` is its eventual seat.
- Generated `main` helpers for the worker binary — Phase 6.
- Static analysis flagging `entflow.Use[T]` inside DB-step closures — v0.5/codegen.
- `entflowtest` package — DX-01, v2.
- Provider idempotency-key TTL / stale-retry warnings — Phase 3 (ACT-06).
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|-------------------|
| DUR-01 | `flow.Start(ctx, in)` persists a run row in `state=pending` | Confirmed unreachable literally (D-25 deviation); `entflow.Start[In](ctx, eng, f, in)` mechanics researched below (RunStore/Engine section) |
| DUR-02 | Run row persists state, input, current step, attempt, last error, timestamps, owner edge | Mixin field mechanics + `WithOwnerRef` erasure pattern (mirrors `WithSelfStatus`, already in Phase 1 `exec.go`) |
| DUR-03 | Step effect + progress pointer commit in the same transaction | Raw-SQL-through-same-tx mechanics (`sql/execquery` feature) — the phase's #1 researched unknown, resolved HIGH confidence below |
| DUR-04 | Worker claims with `FOR UPDATE SKIP LOCKED` | Same as DUR-03: `tx.ExecContext`/`tx.QueryContext` on the `sql/execquery`-generated client, verified against Phase 1's actual generated `client.go`/`tx.go` |
| DUR-05 | Crash-resume to same terminal state | Crash-harness tier mechanics (testcontainers-go, subprocess SIGKILL, sentinel-file sync) researched below |
| DUR-06 | Re-claim at each step boundary, no lease | Already structurally guaranteed by D-30 + claim predicate (D-32); no new mechanism needed, confirmed against ARCHITECTURE.md Anti-Pattern 2 |
| DUR-07 | `state=done` / `failed:<step>` | `NamedValues` colon mechanics verified directly against ent source (HIGH confidence) |
| DUR-08 | Dialect support matrix + startup error | `ClaimStrategy` dispatch at `worker.New`; SQLite `_txlock=immediate` DSN mechanics researched below |
| DUR-09 | In-process or dedicated worker binary | Both are the same `worker.New`/`w.Run(ctx)` object; no additional API surface beyond `os/exec`-testable `main` |
| DUR-10 | Graceful shutdown, no abandoned claim | `context.Context` cancellation + drain-deadline pattern; no new external API |
| OPS-01 | Query runs as ordinary ent entities | Zero new mechanism — ordinary generated query, verified conceptually against ent's standard edge-query generation |
| OPS-02 | `Policy()` governs view/cancel/retry | `ent/privacy` `MutationRuleFunc`/`QueryRule`/`Allow`/`Deny`/`Skip` verified directly against ent source below |
| OPS-04 | Cancel an in-flight run | Conditional `UPDATE ... WHERE state IN (...)` via generated `UpdateOneID(...).Where(...)`, standard ent predicate usage |
| OPS-05 | One span per run, child span per step, attributes | `go.opentelemetry.io/otel/trace` + `propagation.TraceContext` mechanics, including the exact dependency footprint (load-bearing correction below) |
| TEST-01 | Crash-simulation harness, absence-claim assertions | D-55 mechanics: effect+progress paired assertion, side-effect counter |
| TEST-02 | Two explicit tiers | testcontainers-go + subprocess SIGKILL mechanics researched below |
</phase_requirements>

## Summary

This phase's hardest mechanical problem — get a raw `FOR UPDATE SKIP LOCKED` claim query to execute on the *same* Postgres transaction the generated ent client then uses to hydrate and mutate the claimed run — has a confirmed, verified answer: ent's `sql/execquery` feature flag (`entc/gen`, stable since v0.11, still labeled **Experimental** in its own `Stage` field as of the current source) generates `ExecContext`/`QueryContext` methods directly on the generated `*Client` and `*Tx` types. Reading Phase 1's own generated `internal/testdata/ent/client.go` and `tx.go` this session confirms the negative as well as the positive: **without** this feature flag, `config.driver` is unexported and there is no way for entflow's worker code to reach the raw connection a `*ent.Tx` wraps. The plan must therefore add `--feature sql/execquery` to `internal/testdata/ent/generate.go`'s `go:generate` line and regenerate before any claim-loop code can be written. Once regenerated, the pattern is: `tx, _ := client.Tx(ctx)` → `tx.QueryContext(ctx, "SELECT id FROM ... FOR UPDATE SKIP LOCKED LIMIT 1")` → `tx.CancelOrderFlowRun.Get(ctx, id)` (ordinary generated query, same open transaction, no re-lock needed) → step closure → `tx.CancelOrderFlowRun.UpdateOneID(id).SetState(...).Save(ctx)` → `tx.Commit()`.

The colon in `failed:<step>` (D-27) is unproblematic: `field.Enum(...).NamedValues(N, V, ...)` only requires the Go-side name `N` to be a valid Go identifier (checked with `token.IsIdentifier`); the stored value `V` has no character restriction beyond non-empty and unique, confirmed directly in ent's `entc/gen/type.go` validation code. Ent's default Postgres codegen for enum fields is a plain string column with a CHECK constraint, not a native Postgres `ENUM` type, so there is no `ALTER TYPE ... ADD VALUE` concern either.

The observability decision (D-57) needs a mechanical correction the planner must see before writing tasks: `go.opentelemetry.io/otel/trace` **by itself** — as confirmed by reading its actual non-test source imports — pulls in only `go.opentelemetry.io/otel/attribute`, `go.opentelemetry.io/otel/codes`, `go.opentelemetry.io/otel/trace/embedded`, and a *vendored internal* xxhash (no external module), which resolves entirely inside stdlib once expanded. But reaching for the *ambient global* convenience API (`otel.Tracer(...)`, `otel.GetTracerProvider()`) requires importing the **root** `go.opentelemetry.io/otel` package, which additionally pulls `go.opentelemetry.io/otel/internal/global`, `github.com/go-logr/logr`, `github.com/go-logr/stdr`, `go.opentelemetry.io/otel/metric`, `go.opentelemetry.io/otel/propagation`, and `go.opentelemetry.io/auto/sdk` — a materially larger dependency graph than "just the trace API." D-58's "defaults to the global no-op" is achievable with **zero** extra footprint by defaulting `worker.Options.TracerProvider` internally to `go.opentelemetry.io/otel/trace/noop.NewTracerProvider()` (part of the `trace` module itself) rather than reaching for `otel.GetTracerProvider()`. This is a real fork in the road for D-04's dependency-allowlist amendment and should be flagged to the user/discuss-phase if not already settled.

The crash-simulation harness's two tiers (D-52/D-53) map onto well-established, if manual, Go patterns: `go build` the `crashworker` binary once in `TestMain`, launch it with `exec.Command`, synchronize via a sentinel file the worker writes at the injected `internal/crashpoint` blocking point (never a sleep), then `cmd.Process.Kill()` (SIGKILL) and assert DB state from the parent test process using a fresh connection. `testcontainers-go/modules/postgres` v0.44.0's current API is `postgres.Run(ctx, image, opts...)` (`RunContainer` is deprecated); Docker-unavailable detection should **not** rely on `testcontainers.SkipIfProviderIsNotHealthy` alone — it has a documented open bug (testcontainers-go#2859) causing a panic instead of a skip on some CI runners, which is exactly the kind of accidental "loud failure became a silent CI red herring" trap D-54's `ENTFLOW_REQUIRE_CRASHSIM=1` gate is designed to avoid; a custom Docker-ping check with an explicit skip message is safer.

**Primary recommendation:** Regenerate `internal/testdata/ent` with `--feature sql/execquery` as the first task of this phase (everything else — the claim loop, the `RunStore` adapter, the crash harness — depends on it existing); build the run entity as `entflow.RunMixin()` (an `ent.Mixin` per ent's standard `Fields/Edges/Indexes/Hooks/Policy/Annotations` interface) embedded into a hand-written `CancelOrderFlowRun` schema that adds only the one owner edge; default the tracer to `trace/noop`, not the ambient global, to keep D-57's dependency exception genuinely narrow.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Run persistence (state, input, progress) | Database / Storage | — | The run row itself; an ordinary ent entity, no bespoke storage layer |
| Claim query (`FOR UPDATE SKIP LOCKED`) | API / Backend (worker process) | Database / Storage | Executed by the worker via raw SQL, but the locking semantics are entirely a Postgres-tier property; the worker only issues the statement |
| Step execution (DB-step closures) | API / Backend | Database / Storage | Runs inside the worker process, but every effect is a DB mutation via the tx client — no other tier touches it |
| Ops query surface ("this order's runs") | API / Backend | — | Ordinary ent query through the generated client; no new endpoint or service |
| Privacy / cancellation authorization | API / Backend | Database / Storage | `Policy()` on the run entity evaluated by ent's mutation/query interceptor pipeline inside the same process that opens the tx |
| Observability (spans) | API / Backend (worker) | — | Spans are emitted from the worker process; the run row is merely where cross-process trace-context state is persisted, not itself part of the tracing tier |
| Worker topology (in-process vs. dedicated) | API / Backend | — | Deployment decision, not an architectural tier split — same `worker.New`/`Run` object either way |
| Crash-simulation harness | Database / Storage (real Postgres via testcontainers) | API / Backend (subprocess worker binary) | Tier-2 needs a *real* backend process pair (worker binary + Postgres container) to prove the release-gate claim; tier-1 stays entirely in-process |

## Standard Stack

### Core (already pinned by Phase 1's go.mod — verified current)

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `entgo.io/ent` | v0.14.6 (pinned in `go.mod`) | ORM + codegen this phase extends | Already the project's platform; unchanged this phase |
| `modernc.org/sqlite` | v1.56.0 (pinned in `go.mod`, verified `[VERIFIED: proxy.golang.org/modernc.org/sqlite/@latest → v1.56.0, 2026-08-03]`) | CGo-free SQLite driver, dev/test dialect (D-35/D-36) | Phase 1 already depends on it for tests; Phase 2 needs its `_txlock=immediate` DSN parameter for the SQLite claim strategy |

### New for this phase

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/testcontainers/testcontainers-go` | v0.44.0 `[VERIFIED: proxy.golang.org/github.com/testcontainers/testcontainers-go/@latest → v0.44.0, 2026-08-07]` | Real-Postgres crash-simulation harness (D-54) | Community-converged default per STACK.md; confirmed current on the Go module proxy this session |
| `github.com/testcontainers/testcontainers-go/modules/postgres` | v0.44.0 (same tag, `modules/postgres/v0.44.0`, `[VERIFIED: proxy.golang.org]`) | Purpose-built Postgres container module | Must be version-locked to the core module (same repo, same tag pattern `modules/postgres/vX.Y.Z`) |
| `go.opentelemetry.io/otel/trace` | v1.45.0 `[VERIFIED: proxy.golang.org/go.opentelemetry.io/otel/trace/@latest → v1.45.0, 2026-08-03]` | Tracing API only, per D-57 | Confirmed via direct source read to have **zero external-module transitive imports** in its non-test `.go` files — see Load-Bearing Correction below |
| `github.com/jackc/pgx/v5` (stdlib mode) | v5.10.0 `[VERIFIED: proxy.golang.org/github.com/jackc/pgx/v5/@latest → v5.10.0, 2026-06-03]` | Postgres driver for the crash harness and for any real-Postgres dev setup | Confirmed current; STACK.md's "v5.x latest" is now concretely v5.10.0 |

### Optional / discretionary

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `go.opentelemetry.io/otel/trace/noop` | part of the `trace` module, no separate version | Zero-cost default `TracerProvider` | Recommended default for `worker.Options.TracerProvider` instead of the ambient global (see Load-Bearing Correction) |
| `go.opentelemetry.io/otel/propagation` | part of the root `go.opentelemetry.io/otel` module (NOT the `trace` submodule) `[CITED: github.com/open-telemetry/opentelemetry-go/blob/main/trace/go.mod]` | Serializing trace context to a string for D-59's `trace_context` column | Pulls the root `otel` module's dependency graph (see below) if used directly — the planner may instead hand-roll a two-field carrier (`traceID`, `spanID` as hex strings) using only `trace.TraceID.String()`/`trace.SpanID.String()`/`trace.TraceIDFromHex()`/`trace.SpanIDFromHex()`, all of which live in the dependency-free `trace` package itself, avoiding `propagation` entirely |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `sql/execquery` feature-flag-generated `ExecContext`/`QueryContext` | Capturing the raw `*sql.DB`/`pgxpool.Pool` separately outside ent and running the claim query on a connection ent never sees | Breaks D-30's single-transaction invariant outright — the claim and the subsequent ent mutations would be on *different* connections/transactions, defeating the phase's core guarantee. Not viable; discussed only to rule it out explicitly. |
| `otel/trace/noop` as the internal default | `otel.GetTracerProvider()` ambient global | Ambient global auto-picks-up an app's already-configured SDK with zero wiring in `worker.Options`, at the cost of `go-logr`/`stdr`/`metric`/`propagation`/`auto/sdk` becoming part of entflow's non-test dependency graph — a materially wider D-04 exception than "the trace API only" |
| Hand-rolled trace/span-ID hex carrier | `propagation.TraceContext` (W3C `traceparent` format) | `propagation.TraceContext` is the standards-based format other tools can parse, but it lives in the root `otel` module, not `trace`; a hand-rolled two-field carrier stores the same information with zero extra imports but isn't W3C-interoperable if an external system ever needs to parse `trace_context` directly |

**Installation (Go modules — no `npm install` for this project):**
```bash
go get github.com/testcontainers/testcontainers-go@v0.44.0
go get github.com/testcontainers/testcontainers-go/modules/postgres@v0.44.0
go get go.opentelemetry.io/otel/trace@v1.45.0
go get github.com/jackc/pgx/v5@v5.10.0
```

**Version verification:** All four new dependencies were checked against the Go module proxy (`https://proxy.golang.org/<module>/@latest`) directly in this research session — the authoritative source for what actually resolves via `go get`, not a training-data guess. All four are current as of 2026-08-15 and match (or update) STACK.md's project-level research.

## Package Legitimacy Audit

The `package-legitimacy check` seam supports only `npm`/`pypi`/`crates` ecosystems; this project is Go, so verification was performed directly against the Go module proxy (the ecosystem-correct authoritative registry) per the protocol's Step 2 fallback.

| Package | Registry | Age / Maintainer | Source Repo | Verdict | Disposition |
|---------|----------|-------------------|--------------|---------|-------------|
| `github.com/testcontainers/testcontainers-go` | Go proxy | Testcontainers org, multi-year, weekly-cadence releases | github.com/testcontainers/testcontainers-go | OK | Approved |
| `github.com/testcontainers/testcontainers-go/modules/postgres` | Go proxy | Same repo/org as above | github.com/testcontainers/testcontainers-go | OK | Approved |
| `go.opentelemetry.io/otel/trace` | Go proxy | CNCF OpenTelemetry org, industry-standard | github.com/open-telemetry/opentelemetry-go | OK | Approved |
| `github.com/jackc/pgx/v5` | Go proxy | jackc, long-standing de-facto standard Postgres driver | github.com/jackc/pgx | OK | Approved |
| `modernc.org/sqlite` (already a dependency) | Go proxy | cznic (modernc.org), long-standing | gitlab.com/cznic/sqlite | OK | Approved (no change) |

**Packages removed due to SLOP verdict:** none.
**Packages flagged as suspicious [SUS]:** none — all four new dependencies are long-established, widely-adopted, single-maintainer-organization Go modules already named in project-level STACK.md research; the module-proxy check this session confirms none of them are new/hallucinated/slopsquatted.

## Architecture Patterns

### System Architecture Diagram

```
[Caller: RPC handler / cron / CLI / test]
       │ entflow.Start(ctx, eng, f, in)          (D-25's package-level function)
       ▼
┌───────────────────────────────────────────────────────────┐
│ TX 1 (RunStore adapter, generated ent client)                │
│  INSERT CancelOrderFlowRun: state=pending,                    │
│    input=Codec.Marshal(in), owner edge set via WithOwnerRef   │
└───────────────────────────────────────────────────────────┘
       │ commit
       ▼
[worker.Run(ctx) poll loop — any process, in-process or dedicated binary]
       │ tx, _ := client.Tx(ctx)                  (sql/execquery-enabled client)
       │ tx.QueryContext(ctx, claimSQL)            → row id (FOR UPDATE SKIP LOCKED)
       ▼
┌───────────────────────────────────────────────────────────┐
│ TX 2 (same *ent.Tx as the claim query above)                 │
│  run, _ := tx.CancelOrderFlowRun.Get(ctx, id)   (already locked, no re-lock) │
│  self, _ := WithSelfLoader(ctx, tx, run)         (D-41: live re-read)        │
│  result, err := stepClosure(ctx, tx, self)                                  │
│  tx.CancelOrderFlowRun.UpdateOneID(id).                                     │
│    SetState(next).SetCurrentStep(...).SetResults(...).Save(ctx)             │
│  span := tracer.Start(extractedParentCtx, "workflow.CancelOrder.cancel")     │
└───────────────────────────────────────────────────────────┘
       │ commit  (step effect + progress pointer atomic — D-30)
       ▼
[Next poll cycle re-claims the SAME run row for the next step — no lease]
       │ ... repeats until state ∈ {done, cancelled, failed:<step>}
       ▼
[Operator / ops dashboard]
       │ client.CancelOrderFlowRun.Query().Where(order.HasFlowRunsWith(...))  (OPS-01, ordinary ent query, Policy() enforced)
```

### Recommended Project Structure

Following ARCHITECTURE.md's suggested layout, refined against what this research confirmed is mechanically necessary:

```
entflow/
├── run.go                    # entflow.RunMixin() — the ent.Mixin, D-23
├── runstore.go                # entflow.RunStore port interface, D-24
├── engine.go                  # entflow.Engine, entflow.Start[In], D-25
├── ownerref.go                 # entflow.WithOwnerRef[In,ID], D-26
├── selfloader.go                # entflow.WithSelfLoader, D-41
├── workflowmarker.go             # unexported marker type + IsWorkflow(ctx), D-44
├── worker/
│   ├── claim.go                # ClaimStrategy interface + Postgres/MySQL/SQLite impls, D-36
│   ├── dbstep.go                # per-step claim-execute-advance transaction, D-30
│   ├── loop.go                  # poll interval + jitter, nudge channel, graceful shutdown, D-33/D-49
│   ├── span.go                   # trace-context persist/restore across claims, D-59
│   └── worker.go                  # worker.New/Options, dialect validation at construction, D-37
├── internal/
│   └── crashpoint/                 # tier-1 logical crash-point registry, D-52
├── internal/testdata/
│   ├── ent/
│   │   ├── generate.go              # MUST gain --feature sql/execquery
│   │   └── schema/
│   │       └── cancelorderflowrun.go  # hand-written run entity embedding RunMixin(), D-23
│   └── crashworker/                    # ~20-line main, tier-2 kill target, D-48
└── worker_test.go / crashsim_test.go   # tier-1 (fast) and tier-2 (testcontainers + SIGKILL) suites, TEST-01/02
```

### Pattern 1: Reaching the same transaction ent uses (`sql/execquery`) — the phase's load-bearing mechanism

**What:** Ent's generated `*Client`/`*Tx` types do **not**, by default, expose any way to run raw SQL on the connection/transaction they wrap. This was confirmed by reading Phase 1's own generated code this session:

`[VERIFIED: internal/testdata/ent/client.go:41-57]`
```go
type (
	// config is the configuration for the client and its builder.
	config struct {
		// driver used for executing database requests.
		driver dialect.Driver
		...
	}
	Option func(*config)
)
```
`config.driver` is unexported — nothing in `entflow` (a different package) can reach it through the plain generated API.

`[VERIFIED: internal/testdata/ent/tx.go:12-24]`
```go
// Tx is a transactional client that is created by calling Client.Tx().
type Tx struct {
	config
	// Order is the client for interacting with the Order builders.
	Order *OrderClient
	...
}
```
`*ent.Tx` embeds the same unexported `config`, so the pattern repeats for transactions.

`[VERIFIED: internal/testdata/ent/client.go:116-133]`
```go
func (c *Client) Tx(ctx context.Context) (*Tx, error) {
	if _, ok := c.driver.(*txDriver); ok {
		return nil, ErrTxStarted
	}
	tx, err := newTx(ctx, c.driver)
	...
	cfg := c.config
	cfg.driver = tx
	return &Tx{ctx: ctx, config: cfg, Order: NewOrderClient(cfg)}, nil
}
```
This confirms `Client.Tx(ctx)` wraps the **same** `c.driver` connection into the returned `*Tx`'s `config.driver` — so anything that *can* reach `driver` from either side reaches the identical transaction.

The unlock is ent's `sql/execquery` feature flag (`entc/gen`, PR #2447, present since v0.11 — `[CITED: github.com/ent/ent/pull/2447/files]`, `Stage: Experimental` per the feature's own doc comment `[CITED: github.com/ent/ent/blob/master/entc/gen/feature.go]`). When code is generated with `--feature sql/execquery`, both the generated `*Client` and `*Tx` gain:

```go
func (c *Client) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
func (c *Client) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
// identical pair on *Tx
```
`[CITED: github.com/ent/ent/pull/2447/files — quoted method signatures]`. These delegate to the same `dialect.Driver`/`dialect.Tx` the generated builders use — i.e. `tx.QueryContext(...)` executes on the *exact* open Postgres transaction `tx.CancelOrderFlowRun.Get(...)` and `tx.CancelOrderFlowRun.UpdateOneID(...).Save(...)` will also use, satisfying D-30's single-transaction invariant exactly.

**Enabling it:** the current `go:generate` line

`[VERIFIED: internal/testdata/ent/generate.go:3]`
```go
//go:generate go run -mod=mod entgo.io/ent/cmd/ent generate ./schema
```
must become

```go
//go:generate go run -mod=mod entgo.io/ent/cmd/ent generate --feature sql/execquery ./schema
```
(`--feature` is a `StringSliceVarP` flag registered in `cmd/internal/base/base.go` — `[CITED: github.com/ent/ent/blob/master/cmd/internal/base/base.go]`, accepts a comma-separated or repeated list, e.g. `--feature sql/execquery,intercept` if more features are needed later.)

**Caveat to carry into the plan:** ent's own docs and source label this feature **Experimental** (its `Stage` field, not a "Stable" feature). Statements executed via `ExecContext`/`QueryContext` bypass ent's hooks, privacy, and validators entirely `[CITED: entgo.io feature-flags doc, cross-checked via WebSearch]` — which is *exactly* the property the claim query needs (it must not fire Policy() checks meant for the run row's normal CRUD path), but it means every other raw-SQL use of this escape hatch anywhere else in the codebase would silently skip privacy too. Scope its use narrowly to `worker/claim.go`'s claim statement only; do not let it become a general-purpose raw-SQL utility.

**When to use:** Exactly the claim query, and nowhere else in Phase 2's code.

### Pattern 2: `field.Enum(...).NamedValues(...)` for the colon-bearing `failed:<step>` state

**What:** Confirmed directly from ent's field builder source:

`[CITED: github.com/ent/ent/blob/master/schema/field/field.go — NamedValues, lines ~1046-1067]`
```go
// NamedValues adds the given name, value pairs to the enum value.
// The "name" defines the Go identifier of the enum, and the value
// defines the actual value in the database.
func (b *enumBuilder) NamedValues(namevalue ...string) *enumBuilder {
	if len(namevalue)%2 == 1 {
		b.desc.Err = fmt.Errorf("Enum.NamedValues: odd argument count")
		return b
	}
	for i := 0; i < len(namevalue); i += 2 {
		b.desc.Enums = append(b.desc.Enums, struct{ N, V string }{N: namevalue[i], V: namevalue[i+1]})
	}
	return b
}
```

And the codegen-time validation that actually enforces character restrictions — confirmed in `entc/gen/type.go`:

`[CITED: github.com/ent/ent/blob/master/entc/gen/type.go — Field.enums, lines ~1855-1880]`
```go
func (f Field) enums(lf *load.Field) ([]Enum, error) {
	...
	for i := range lf.Enums {
		switch name, value := f.EnumName(lf.Enums[i].N), lf.Enums[i].V; {
		case value == "":
			return nil, fmt.Errorf("%q field value cannot be empty", f.Name)
		case values[value]:
			return nil, fmt.Errorf("duplicate values %q for enum field %q", value, f.Name)
		case !token.IsIdentifier(name) && !f.HasGoType():
			return nil, fmt.Errorf("enum %q does not have a valid Go identifier (%q)", value, name)
		default:
			values[value] = true
			enums = append(enums, Enum{Name: name, Value: value})
		}
	}
	...
}
```

The `token.IsIdentifier` check runs against `name` (the Go constant identifier, derived from `N` via `f.EnumName`), **never** against `value` (`V`, the stored string). `V`'s only constraints are non-empty and unique among the field's declared values. A colon in `V` is unconstrained by ent's own validation.

```go
field.Enum("state").
	NamedValues(
		"Pending", "pending",
		"Running", "running",
		"Done", "done",
		"Cancelled", "cancelled",
		"FailedRefund", "failed:refund",
		"FailedCancel", "failed:cancel",
	)
```

**DB-side confirmation:** by default, ent's Postgres codegen emits enum fields as a plain string/`VARCHAR` column with a `CHECK (state IN (...))` constraint — **not** a native Postgres `CREATE TYPE ... AS ENUM` — unless the schema explicitly opts into native enum types `[CITED: entgo.io/docs/migration/enum-types, via WebSearch cross-check]`. This means there is no `ALTER TYPE ... ADD VALUE` concern when a new `failed:<step>` value is added as the flow's step graph grows, and Postgres CHECK-constraint string comparison has no character restriction that would reject a colon.

**When to use:** Exactly the run row's `state` field, per D-27.

### Pattern 3: `ent.Mixin` for `entflow.RunMixin()`

**What:** Confirmed directly from ent's mixin source — the full interface surface a library-shipped mixin can populate:

`[CITED: github.com/ent/ent/blob/master/schema/mixin/mixin.go]`
```go
// Schema is the default implementation for the ent.Mixin interface.
type Schema struct{}

func (Schema) Fields() []ent.Field { return nil }
func (Schema) Edges() []ent.Edge { return nil }
func (Schema) Indexes() []ent.Index { return nil }
func (Schema) Hooks() []ent.Hook { return nil }
func (Schema) Interceptors() []ent.Interceptor { return nil }
func (Schema) Policy() ent.Policy { return nil }
func (Schema) Annotations() []schema.Annotation { return nil }

var _ ent.Mixin = (*Schema)(nil)
```

`entflow.RunMixin()` should embed `mixin.Schema` and override `Fields()` (the D-28 column list) and `Indexes()` (D-34's `entsql.IndexWhere` partial index — the mixin CAN declare this itself, since it is entirely about fields the mixin already owns, e.g. `index.Fields("state", "retry_after").Annotations(entsql.IndexWhere("state IN ('pending','running')"))`). `RunMixin()` must **not** attempt to declare `Edges()` — the owner-aggregate edge (D-26) is app-specific (points at `Order` or whatever the concrete flow's owner is) and cannot be known by a library-supplied mixin; this matches D-23's own framing ("a mixin, a step-derived state enum, and one edge" — the edge is the *concrete* schema's job, not the mixin's).

**Caveat found in this research pass, worth flagging to the planner:** `entsql.IndexWhere` documentation explicitly scopes partial-index support to **SQLite and PostgreSQL only** `[CITED: github.com/ent/ent/blob/master/dialect/entsql/annotation.go — IndexWhere doc comment: "IndexWhere allows configuring partial indexes in SQLite and PostgreSQL"]`. MySQL has no partial-index feature at all. If the mixin's `Indexes()` unconditionally declares the `entsql.IndexWhere` annotation, MySQL migration will either silently ignore it or (depending on ent's migration path) need a MySQL-specific index without the `WHERE` clause. D-35 already frames MySQL as "compatible, uncertified" — this is a concrete instance of that uncertification the dialect-matrix doc should call out by name, not just gesture at generically.

**When to use:** `entflow.RunMixin()`, embedded into every hand-written (Phase 2) and later generated (Phase 6) run schema.

### Pattern 4: `ent/privacy` for `Policy()` on the run entity

**What:** Confirmed directly from ent's privacy package source:

`[CITED: github.com/ent/ent/blob/master/privacy/privacy.go]`
```go
var (
	Allow = errors.New("ent/privacy: allow rule")
	Deny  = errors.New("ent/privacy: deny rule")
	Skip  = errors.New("ent/privacy: skip rule")
)

type (
	QueryRule interface {
		EvalQuery(context.Context, ent.Query) error
	}
	MutationRule interface {
		EvalMutation(context.Context, ent.Mutation) error
	}
	QueryMutationRule interface {
		QueryRule
		MutationRule
	}
)

type MutationRuleFunc func(context.Context, ent.Mutation) error
func (f MutationRuleFunc) EvalMutation(ctx context.Context, m ent.Mutation) error { return f(ctx, m) }
```

The fixture's `Policy()` (D-45) is an ordinary `privacy.Policy{Mutation: privacy.MutationPolicy{...}, Query: privacy.QueryPolicy{...}}` built from `MutationRuleFunc`/`QueryRule` values that check the context (viewer + `entflow.IsWorkflow(ctx)` for the cancel-guard, per D-44) and return `privacy.Allow`/`privacy.Deny`/`privacy.Skip`. This is standard ent usage — no entflow-specific mechanism needed beyond the workflow-marker context key (D-44) and the app-supplied `worker.Options.Context` viewer injection (D-45).

**When to use:** The fixture run schema's `Policy()` method, proving OPS-02.

### Pattern 5: Persisting/restoring trace context across the resume boundary (D-59)

**What:** OpenTelemetry's `SpanContext` carries `TraceID`, `SpanID`, `TraceFlags`, `TraceState` — all serializable via the `trace` package's own `.String()`/`FromHex()` methods, with **no** dependency on `propagation` or the root `otel` module:

```go
sc := span.SpanContext()
traceID := sc.TraceID().String()   // trace.TraceID.String(), in the trace package itself
spanID := sc.SpanID().String()     // trace.SpanID.String()
```
Restoring: `trace.TraceIDFromHex(s)` / `trace.SpanIDFromHex(s)` construct a `SpanContext` via `trace.NewSpanContext(trace.SpanContextConfig{TraceID: ..., SpanID: ..., Remote: true})`, then `ctx = trace.ContextWithRemoteSpanContext(ctx, sc)` before calling `tracer.Start(ctx, "workflow.CancelOrder.refund")` — the new span picks up the persisted trace as its parent, producing correct cross-process span linkage even though a different worker process executes the next step (exactly the gap PITFALLS' Integration Gotchas table calls out for the Activity protocol; D-59 applies the same fix to every DB-step re-claim, not just Activities).

The alternative — `propagation.TraceContext{}.Inject/Extract` against a `TextMapCarrier` (the W3C `traceparent` wire format) — is the standards-based approach and should be preferred **if** `go.opentelemetry.io/otel/propagation` is acceptable in the dependency graph; it lives in the root `otel` module (confirmed via `trace/go.mod`'s own `require go.opentelemetry.io/otel v1.45.0` line), not the dependency-free `trace` submodule, so pulling it in reopens the same footprint question as the ambient-global tracer (see Load-Bearing Correction below). The hand-rolled two-field (`traceID`, `spanID`) approach keeps D-57's exception to exactly the `trace` package's own closure.

**When to use:** `worker/span.go`'s persist-at-commit / restore-at-claim pair, D-59.

### Load-Bearing Correction to D-57: the dependency footprint is NOT flat

This is the one place in this phase's research that surfaces new information materially affecting a locked decision's *implementation*, not the decision itself. D-57 says "entflow core takes a direct dependency on `go.opentelemetry.io/otel/trace` (API only — never the SDK)" and frames this as a narrow exception. Confirmed by reading the actual non-test `import` blocks of `go.opentelemetry.io/otel/trace`'s source files this session:

`[CITED: github.com/open-telemetry/opentelemetry-go — trace/trace.go, trace/context.go, trace/config.go, trace/provider.go, trace/noop.go, attribute/kv.go, attribute/set.go, attribute/value.go, codes/codes.go — all non-test import blocks read directly]`

The full transitive package closure of `go.opentelemetry.io/otel/trace` (plus its own `noop` subpackage, useful as the zero-cost default) is:
- stdlib: `context`, `encoding/json`, `encoding/base64`, `errors`, `fmt`, `math`, `reflect`, `slices`, `sort`, `strconv`, `strings`, `unicode/utf8`, `cmp`, `time`
- `go.opentelemetry.io/otel/attribute`
- `go.opentelemetry.io/otel/attribute/internal/xxhash` (a **vendored, internal** copy — not the external `github.com/cespare/xxhash/v2` module)
- `go.opentelemetry.io/otel/codes`
- `go.opentelemetry.io/otel/trace/embedded`
- `go.opentelemetry.io/otel/trace/noop` (if used for the default `TracerProvider`)

**Zero external modules.** This is genuinely as narrow as D-57 claims — *if* entflow only imports `go.opentelemetry.io/otel/trace` (and its `noop` subpackage) directly and never the root `go.opentelemetry.io/otel` package.

But the root `go.opentelemetry.io/otel` package — which is what `otel.Tracer(name)` / `otel.GetTracerProvider()` (the "ambient global" convenience functions D-58's phrase "defaults to the global no-op" naturally evokes) live in — additionally imports:

`[CITED: github.com/open-telemetry/opentelemetry-go — trace.go, handler.go, internal_logging.go, internal/global/trace.go, internal/global/state.go, internal/global/internal_logging.go — all non-test import blocks read directly]`
- `go.opentelemetry.io/otel/internal/global`
- `github.com/go-logr/logr`
- `github.com/go-logr/stdr`
- `go.opentelemetry.io/auto/sdk`
- `go.opentelemetry.io/otel/metric`
- `go.opentelemetry.io/otel/propagation`

**Recommendation for the plan:** default `worker.Options.TracerProvider` to `go.opentelemetry.io/otel/trace/noop.NewTracerProvider()` internally (zero footprint, matches D-58's "pays nothing when nothing is wired" intent exactly) and require an app that wants entflow's spans to flow into its already-configured OTel SDK to pass its `TracerProvider` explicitly through `worker.Options` — rather than entflow reaching for `otel.GetTracerProvider()` itself. This keeps D-04's dependency-allowlist amendment to the five/six packages enumerated above, all of which resolve to stdlib underneath. If the team instead wants the "auto-discovers the app's global SDK" ergonomics of `otel.GetTracerProvider()`, the D-04 exception must explicitly also allowlist `go-logr/logr`, `go-logr/stdr`, `otel/metric`, `otel/propagation`, and `otel/auto/sdk` — a materially different, wider commitment than "the trace API only." **This is exactly the kind of place D-57 itself flagged as "the one decision in this phase most worth a second look" — the planner should make this footprint choice explicit rather than let it be an accidental consequence of which OTel entry point a task happens to import.**

### Pattern 6: SQLite `_txlock=immediate` claim strategy (D-36)

**What:** `modernc.org/sqlite`'s DSN accepts a `_txlock` parameter with values `deferred` (default), `immediate`, or `exclusive` `[CITED: pkg.go.dev/modernc.org/sqlite, cross-checked against mattn/go-sqlite3 issue #189/#400 for the general SQLite driver convention, and gitlab.com/cznic/sqlite issue #127]`. Setting `_txlock=immediate` in the DSN makes **every** transaction opened through that connection issue `BEGIN IMMEDIATE` rather than `BEGIN DEFERRED` — a write lock is acquired the instant the transaction starts, rather than lazily on the first write statement, which is exactly what D-36's SQLite claim strategy needs: the plain `SELECT ... ORDER BY id LIMIT 1` claim query must itself be covered by the write lock (not just the subsequent `UPDATE`), so that a second concurrent claimer blocks (or gets `SQLITE_BUSY`) rather than reading the same "claimable" row before the first claimer's transaction commits.

```go
db, err := sql.Open("sqlite", "file:test.db?_txlock=immediate")
```

**Caveat found in this research:** the DSN-level `_txlock=immediate` setting applies to **every** transaction opened on that connection/DB handle, including read-only queries — there is no per-transaction override at the DSN level `[CITED: gitlab.com/cznic/sqlite issue #127 — "tx_lock mode set regardless of read or write tx"]`. For SQLite in Phase 2, this is fine (D-36 already forces `Concurrency=1`, so there is no read/write transaction mix to optimize), but it should be documented as a known constraint if a future phase ever wants a mixed read/write SQLite pool.

**How the DSN reaches ent:** since ent consumes any `*sql.DB` via `sql.Open(driverName, dataSourceName)` / `entsql.OpenDB` (confirmed against `internal/testdata/ent/client.go:100-111`'s `Open(driverName, dataSourceName string, ...)` function, which calls `sql.Open(driverName, dataSourceName)` internally, `[VERIFIED: internal/testdata/ent/client.go:97-111]`), the `_txlock=immediate` parameter is simply part of the `dataSourceName` string the worker's SQLite setup path passes to `ent.Open("sqlite3", "file:test.db?_txlock=immediate")` (or the driver-specific equivalent) — no special ent-side API needed.

**When to use:** `worker/claim.go`'s SQLite `ClaimStrategy` implementation, D-36.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Raw SQL on the same transaction ent's generated client uses | A parallel `*sql.DB`/`pgxpool.Pool` reference captured outside ent, hoping to correlate it with the right connection | `sql/execquery`-generated `ExecContext`/`QueryContext` on `*ent.Tx` | The only way to guarantee same-transaction execution without ent-internal reflection hacks; verified this session against Phase 1's actual generated code |
| Cross-process trace continuation | A bespoke correlation-ID scheme reinvented from scratch | `trace.SpanContext` + `trace.TraceIDFromHex`/`SpanIDFromHex` (or `propagation.TraceContext` if the wider dependency is accepted) | OTel's `SpanContext` is already exactly "trace ID + span ID + flags," serializable with zero extra logic |
| Postgres-vs-SQLite claim query divergence | A single hand-rolled query string with dialect-conditional SQL fragments spliced in ad hoc | The `ClaimStrategy` interface (D-36), one implementation per dialect family | Keeps the one genuinely non-portable statement isolated and testable in a dedicated conformance suite (D-38), rather than string-templated SQL scattered through the worker loop |
| Docker-availability detection for the crash harness | Relying solely on `testcontainers.SkipIfProviderIsNotHealthy` | A custom `docker ps`/socket-ping check before invoking testcontainers, with an explicit `t.Skip("Docker unavailable: ...")` | `SkipIfProviderIsNotHealthy` has a documented open bug causing a panic instead of a skip on some CI runners (testcontainers-go#2859) — a panic instead of a skip is exactly the "silent gate bypass" D-54's `ENTFLOW_REQUIRE_CRASHSIM=1` exists to prevent |
| Retryable-error classification | A single cross-driver "is this retryable" heuristic based on error message string matching | Per-driver typed error inspection: `*pgconn.PgError` with `.Code` in `{"40001", "40P01"}` for Postgres/pgx; `errors.Is(err, driver.ErrBadConn)` for the driver-agnostic connection-loss case (a `database/sql` stdlib sentinel, works across all three dialects); a MySQL-specific `*mysql.MySQLError` check only if/when MySQL is exercised | String-matching driver error messages is fragile across driver versions; the typed-error approach is what pgx's own issue tracker and the stdlib `database/sql` docs recommend |

**Key insight:** every mechanism this phase needs either already exists inside ent's own (experimental but stable-since-2022) feature surface, or inside OpenTelemetry's dependency-free `trace` API — the temptation to hand-roll usually means the wrong entry point was chosen (e.g. reaching for `pgxpool.Pool` directly instead of `sql/execquery`, or `otel.GetTracerProvider()` instead of `trace/noop`), not that no library mechanism exists.

## Common Pitfalls

*(PITFALLS.md's Pitfall 1 — zombie runs — Pitfall 7 — dialect divergence — and Pitfall 9 — false-confidence crash harnesses — plus its Anti-Pattern 2 and Security Mistakes table are directly load-bearing for this phase and are assumed read; the pitfalls below are net-new findings from this research pass, not restatements.)*

### Pitfall: `sql/execquery` is still labeled Experimental in ent's own source

**What goes wrong:** A future ent minor release could change or restrict this feature's behavior without the same backward-compatibility bar ent applies to "Stable" features, since it is explicitly self-labeled `Stage: Experimental` in `entc/gen/feature.go` `[CITED: github.com/ent/ent/blob/master/entc/gen/feature.go]`.
**Why it happens:** The feature has existed since v0.11 (2022) with no sign of removal or deprecation, so it reads as de-facto stable in practice — but ent has not promoted its label.
**How to avoid:** Pin the exact ent version (already D-03's practice) and re-verify this feature's generated method signatures on every ent minor bump, the same discipline STACK.md already prescribes for the whole `entc.Extension` surface generally.
**Warning signs:** A `go generate` after an ent version bump that silently changes `ExecContext`/`QueryContext`'s signature or removes them.

### Pitfall: `testcontainers.SkipIfProviderIsNotHealthy` can panic instead of skip

**What goes wrong:** On some CI runners (documented: macOS GitHub Actions) `SkipIfProviderIsNotHealthy` panics rather than cleanly skipping when Docker is unavailable `[CITED: github.com/testcontainers/testcontainers-go/issues/2859]`.
**Why it happens:** An open, acknowledged bug in testcontainers-go as of this research date.
**How to avoid:** Implement a small custom Docker-availability probe (e.g. attempt to connect to the Docker socket / run a trivial `docker.NewClientWithOpts` ping) and `t.Skip(...)` explicitly on failure, rather than depending on the library's own skip helper for the release-gate-critical "loud skip" behavior D-54 requires.
**Warning signs:** CI runs that show a panicking test process instead of a skipped test in exactly the environments where Docker is expected to be absent (e.g. some hosted macOS runners).

### Pitfall: `entsql.IndexWhere` is not portable to MySQL

**What goes wrong:** If `entflow.RunMixin()`'s `Indexes()` unconditionally attaches `entsql.IndexWhere(...)`, a MySQL-targeted migration either drops the WHERE clause silently or errors, depending on ent's exact migration-diff behavior for an annotation the target dialect doesn't support.
**Why it happens:** `entsql.IndexWhere`'s own doc comment scopes support to "SQLite and PostgreSQL" only `[CITED: github.com/ent/ent/blob/master/dialect/entsql/annotation.go]` — MySQL has no partial-index feature at the SQL level at all.
**How to avoid:** Document this explicitly in the dialect support matrix (D-35) as a named MySQL limitation ("no partial index — claim query performance degrades faster as terminal rows accumulate"), not just imply it via the general "MySQL is uncertified" framing.
**Warning signs:** A MySQL migration test that either silently produces a full (non-partial) index or fails outright when this annotation is present.

## Code Examples

### The claim query, executed on the same transaction ent's generated client uses

```go
// Source: mechanism verified against Phase 1's internal/testdata/ent/client.go
// and tx.go (this session) + ent's sql/execquery feature (PR #2447).
tx, err := client.Tx(ctx)
if err != nil {
	return err
}
defer tx.Rollback() // no-op after a successful Commit

rows, err := tx.QueryContext(ctx,
	`SELECT id FROM cancel_order_flow_runs
	 WHERE state IN ('pending','running')
	   AND (retry_after IS NULL OR retry_after <= now())
	 ORDER BY id
	 LIMIT 1
	 FOR UPDATE SKIP LOCKED`)
if err != nil {
	return err
}
var id int
if rows.Next() {
	if err := rows.Scan(&id); err != nil {
		rows.Close()
		return err
	}
}
rows.Close()
if id == 0 {
	return tx.Commit() // nothing claimable this cycle
}

run, err := tx.CancelOrderFlowRun.Get(ctx, id) // same tx — already locked, no re-lock
if err != nil {
	return err
}
// ... execute the step closure, then advance state in the same tx ...
_, err = tx.CancelOrderFlowRun.UpdateOneID(id).
	SetState(cancelorderflowrun.StateDone).
	Save(ctx)
if err != nil {
	return err
}
return tx.Commit()
```

### `entflow.RunMixin()` sketch

```go
// Source: ent.Mixin interface confirmed against schema/mixin/mixin.go.
type RunMixin struct{ mixin.Schema }

func (RunMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("state").NamedValues(
			"Pending", "pending",
			"Running", "running",
			"Done", "done",
			"Cancelled", "cancelled",
			// per-step failed:<step> values added by the concrete schema's own step list
		),
		field.Bytes("input"),
		field.String("current_step").Optional(),
		field.Int("attempt").Default(0),
		field.String("last_error").Optional(),
		field.JSON("results", map[string]json.RawMessage{}).Optional(),
		field.Time("retry_after").Optional().Nillable(),
		field.String("trace_context").Optional(), // D-59: hex traceID:spanID or W3C traceparent
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
		field.Time("started_at").Optional().Nillable(),
		field.Time("finished_at").Optional().Nillable(),
	}
}

func (RunMixin) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("state", "retry_after").
			Annotations(entsql.IndexWhere("state IN ('pending','running')")), // Postgres/SQLite only — see Pitfall above
	}
}
```

### Restoring a persisted trace context as the parent of a new step span

```go
// Source: mechanism confirmed against trace.go/context.go non-test source.
sc := trace.NewSpanContext(trace.SpanContextConfig{
	TraceID: mustTraceIDFromHex(run.TraceContext[:32]),
	SpanID:  mustSpanIDFromHex(run.TraceContext[33:49]),
	Remote:  true,
})
ctx = trace.ContextWithRemoteSpanContext(ctx, sc)
ctx, span := tracer.Start(ctx, "workflow.CancelOrder.refund")
defer span.End()
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|---------------|--------|
| `testcontainers.RunContainer(...)` for the Postgres module | `postgres.Run(ctx, image, opts...)` | Deprecated in recent testcontainers-go releases, still present but flagged for removal | Use `Run`, not `RunContainer`, in all new code this phase writes |
| Native Postgres `ENUM` types via ent's `SchemaType` override | Plain string column + CHECK constraint (ent's default) | N/A — this has always been ent's default; the "native enum" path is opt-in | No action needed; the default is already what D-27 wants |

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `sql/execquery`'s generated method names/signatures on the currently-pinned exact ent v0.14.6 tag match what was read from the `master` branch source this session (methods confirmed via a PR from 2022 plus a `master`-branch feature-list read, not the exact v0.14.6 tag diff) | Pattern 1 | Low — this feature has been stable since v0.11 with no changelog entry indicating a signature change; still, the plan's first task should regenerate and inspect the actual output rather than trust this research blindly |
| A2 | `go.opentelemetry.io/otel/trace`'s transitive import closure (attribute/codes/embedded/vendored-xxhash, zero external modules) matches what will actually resolve for the exact pinned `v1.45.0` tag (read from `main` branch source, not the tag diff) | Load-Bearing Correction to D-57 | Low-Medium — package import structure is unlikely to change between a recent tag and `main`, but the plan's first OTel-integration task should run `go list -deps` against the real pinned version to confirm before the D-04 test is written |
| A3 | MySQL's lack of partial-index support is uniformly true across MySQL 8.0.1+ (the D-35 floor) — sourced from `entsql.IndexWhere`'s doc comment, not independently verified against MySQL's own documentation this session | Pattern 3 pitfall | Low — MySQL partial indexes are a well-known, long-standing gap in MySQL generally (functional indexes exist but not `WHERE`-filtered indexes), low risk of being wrong |
| A4 | The exact retryable Postgres SQLSTATE set for D-51 (`40001` serialization_failure, `40P01` deadlock_detected) is complete for the driver-error classification this phase needs — connection-loss detection via `driver.ErrBadConn` was verified as a stdlib mechanism, but the *exhaustive* set of "retryable" codes was not independently re-derived beyond these two well-known codes | D-51 mechanics, Don't Hand-Roll table | Medium — an incomplete retryable set means some transient errors fail the run immediately instead of retrying (safe but suboptimal — never causes a correctness bug, since D-51 already treats non-retryable errors as an immediate fail) |

**If this table is empty:** N/A — see entries above.

## Open Questions

1. **Should `worker.Options.TracerProvider` default to `trace/noop` or reach for `otel.GetTracerProvider()`?**
   - What we know: the two choices have a materially different D-04 dependency-allowlist footprint (see Load-Bearing Correction above).
   - What's unclear: whether the project wants the "auto-discovers the app's already-configured SDK" ergonomics badly enough to accept the wider exception.
   - Recommendation: default to `trace/noop` internally (zero footprint, matches D-58's "pays nothing when nothing is wired" framing literally); let an app opt in to its own SDK by passing `TracerProvider` explicitly through `worker.Options`. Surface this as an explicit planner decision, not an implicit one.

2. **Does the mixin's `Indexes()` need dialect-conditional logic, or does the concrete schema own the MySQL fallback?**
   - What we know: `entsql.IndexWhere` doesn't apply to MySQL.
   - What's unclear: whether Phase 2's fixture even needs to prove a MySQL-compatible index variant, given MySQL is "compatible, uncertified" and the crash harness never runs against it.
   - Recommendation: ship the Postgres/SQLite partial index as-is in the mixin for Phase 2 (matches D-34's "Postgres-specific required operational guidance" framing); document the MySQL gap in the dialect matrix doc rather than building a MySQL-specific index variant with no test coverage behind it.

3. **Exact retryable-error sets per driver (D-51) beyond the two well-known Postgres codes.**
   - What we know: `40001`/`40P01` for Postgres via `*pgconn.PgError`, `driver.ErrBadConn` for connection loss (stdlib, driver-agnostic).
   - What's unclear: whether additional Postgres SQLSTATE codes (e.g. connection-related `08xxx` codes) should also be classified retryable, and the exact MySQL error-number set if MySQL is exercised in Phase 2's tests at all.
   - Recommendation: start with the two well-known codes plus `driver.ErrBadConn`; this is explicitly listed as Claude's Discretion in CONTEXT.md ("the exact retryable-error classification per driver"), so the planner has latitude to size this conservatively for Phase 2 and extend later.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Docker | testcontainers-go crash harness (both tiers) | Unknown in this research session (no Docker probe run — this was a read-only research pass, no `docker info` executed) | — | Custom Docker-ping + loud `t.Skip`, not `SkipIfProviderIsNotHealthy` alone (see Pitfall above); `ENTFLOW_REQUIRE_CRASHSIM=1` turns the skip into a CI failure per D-54 |
| Go toolchain | Everything | Not present in this research sandbox (`go` not on `PATH`) — irrelevant to the execution environment where this phase will actually be planned/built, noted only because it constrained this research session's ability to run `go list -deps` directly | go 1.25 (per `go.mod`) | The plan's first tasks should run `go list -deps` for real against the pinned `go.mod` versions to confirm A1/A2 above in an environment where Go is actually installed |
| PostgreSQL (real server, via testcontainers) | Crash-simulation harness, D-54 | Not probed this session (containerized, provisioned by testcontainers-go at test time, not a persistent environment dependency) | 12+ (target 14+, per STACK.md) | Loud skip if Docker/container provisioning fails |

**Missing dependencies with no fallback:** none — every dependency above has a documented fallback (skip with a loud message, or a follow-up verification task in an environment with Go installed).

**Missing dependencies with fallback:** Docker (skip), Go toolchain (this research session only — not a concern for the actual execution environment).

## Security Domain

`security_enforcement` is enabled (`.planning/config.json` → `workflow.security_enforcement: true`, `security_asvs_level: 1`).

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-------------------|
| V2 Authentication | No | Out of scope — entflow does not authenticate; it receives an already-authenticated context from the host application |
| V3 Session Management | No | Same as above |
| V4 Access Control | Yes | `ent/privacy` `Policy()` on the run entity (D-45), enforced by ent's own mutation/query interceptor pipeline — verified mechanism, Pattern 4 above |
| V5 Input Validation | Yes | The run row's `input` field is opaque codec bytes (Phase 1's `Codec[In]`, already locked); no new validation surface this phase introduces beyond what Phase 1 proved |
| V6 Cryptography | No | No new cryptographic surface in this phase (idempotency keys are Phase 3) |

### Known Threat Patterns for this stack

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|-----------------------|
| Privilege escalation via a forgeable workflow-marker context value | Elevation of Privilege | D-44: unexported marker type, set only inside the worker's tx-opening path; `IsWorkflow(ctx) bool` is the only public surface; a test asserts no exported API (including test helpers shipped in the public module) can set it — already a locked decision, PITFALLS' Security Mistakes table names this exact pattern |
| `sql/execquery`'s raw claim statement bypassing privacy/hooks being reused elsewhere as a general escape hatch | Tampering / Elevation of Privilege | Scope the `ExecContext`/`QueryContext` usage to exactly `worker/claim.go`'s claim SQL (a fixed, parameterized statement with no user-supplied SQL fragments); do not expose a general raw-SQL passthrough anywhere in entflow's public API |
| Serialized run `input`/`results` JSON containing PII/payment data surfaced via an ops dashboard or `Describe()`-adjacent tooling with no field-level redaction | Information Disclosure | Already named in PITFALLS' Security Mistakes table; Phase 2's `Policy()` governs row-level access but does not redact field contents — document this boundary explicitly, consistent with Phase 1's `codec.go` already never embedding raw bytes in error messages `[VERIFIED: codec.go:38-40 — "On error the message names the target type via %T but never embeds the raw input bytes"]` |
| SQL injection in the raw claim query | Tampering | The claim query in this research has zero user-supplied string interpolation — the `WHERE` clause is a fixed literal, and any parameterized values (e.g. `LIMIT $2`) go through `QueryContext`'s `args ...any`, which uses placeholder binding, not string concatenation. Verify this discipline holds when the plan writes the actual query. |

## Sources

### Primary (HIGH confidence — in-repo, read via Read tool this session)
- `internal/testdata/ent/client.go` — `config` struct, `NewClient`, `Open`, `(*Client).Tx`, `(*Client).BeginTx`, `(*Client).Debug` — confirms `config.driver` is unexported and the exact mechanism `Client.Tx` uses to wrap the same driver into a new `*Tx`
- `internal/testdata/ent/tx.go` — `Tx` struct, `Commit`/`Rollback`/`(*Tx).Client()`, `txDriver` — confirms the transaction-wrapping mechanism `sql/execquery` methods would delegate through
- `internal/testdata/ent/generate.go` — the exact `go:generate` line that must gain `--feature sql/execquery`
- `exec.go`, `flow.go`, `step.go`, `stepoptions.go`, `result.go`, `codec.go`, `errors.go`, `meta/meta.go`, `activity.go`, `retry.go`, `json.go`, `registry.go`, `steps.go`, `transitions.go` — Phase 1's full executor/builder surface, read to ground D-24/D-26/D-39/D-41's erasure-pattern reuse and D-30's per-step refactor of `Exec`
- `internal/testdata/ent/schema/order.go`, `order_flows.go` — the fixture entity and `CancelOrder` flow this phase's worked example extends

### Secondary (MEDIUM confidence — official upstream source, read directly this session via WebFetch/curl against raw.githubusercontent.com and pkg.go.dev)
- `github.com/ent/ent/blob/master/schema/field/field.go` — `NamedValues` builder
- `github.com/ent/ent/blob/master/entc/gen/type.go` — `Field.enums` validation (the `token.IsIdentifier` check that permits colons in stored values)
- `github.com/ent/ent/blob/master/schema/mixin/mixin.go` — `ent.Mixin` interface surface (`Fields`/`Edges`/`Indexes`/`Hooks`/`Interceptors`/`Policy`/`Annotations`)
- `github.com/ent/ent/blob/master/dialect/entsql/annotation.go` — `IndexWhere` (SQLite/Postgres-only scope)
- `github.com/ent/ent/blob/master/privacy/privacy.go` — `Allow`/`Deny`/`Skip`, `QueryRule`/`MutationRule`/`QueryMutationRule`, `MutationRuleFunc`
- `github.com/ent/ent/blob/master/dialect/sql/driver.go` — `sql.OpenDB`/`sql.Open` (the `entgo.io/ent/dialect/sql` package conventionally aliased `entsql` by many ent users, distinct from `entgo.io/ent/dialect/entsql`)
- `github.com/ent/ent/blob/master/cmd/internal/base/base.go` — `--feature` CLI flag registration
- `github.com/ent/ent/pull/2447/files` — `sql/execquery` feature's generated `ExecContext`/`QueryContext` method signatures
- `github.com/ent/ent/issues/4295` — confirms there is no supported way to extract a raw `pgx.Tx`/`*sql.Tx` handle from `*ent.Tx` *without* the `sql/execquery` feature — corroborates the negative finding from the in-repo read
- `github.com/open-telemetry/opentelemetry-go` — `go.mod` (root and `trace/go.mod`), `trace/trace.go`, `trace/context.go`, `trace/config.go`, `trace/provider.go`, `trace/noop.go`, `trace/noop/noop.go`, `attribute/kv.go`, `attribute/set.go`, `attribute/value.go`, `codes/codes.go`, `trace.go`, `handler.go`, `internal_logging.go`, `internal/global/trace.go`, `internal/global/state.go`, `internal/global/internal_logging.go` — all read directly for the dependency-footprint analysis
- `proxy.golang.org/<module>/@latest` — version/date verification for `testcontainers-go`, `testcontainers-go/modules/postgres`, `pgx/v5`, `otel/trace`, `modernc.org/sqlite` (authoritative registry, not a training-data guess)

### Tertiary (LOW-MEDIUM confidence — WebSearch only, cross-checked across 2+ results where noted)
- `sql/execquery`'s "Experimental" stage label and its "bypasses hooks/privacy/validators" caveat — WebSearch-sourced summary of `entgo.io/docs/feature-flags/` (the entgo.io domain itself was blocked by the network egress proxy this session, so this specific doc page could not be read directly — cross-checked against the PR #2447 source read instead, which independently confirms the method existence and signatures)
- `_txlock=immediate` SQLite DSN semantics — WebSearch, cross-checked against `gitlab.com/cznic/sqlite` issue #127 (modernc.org/sqlite's own issue tracker) and `mattn/go-sqlite3` issues #189/#400 for the general driver convention
- pgx/MySQL retryable-error SQLSTATE/error-number codes — WebSearch, well-known Postgres documentation facts (40001/40P01) and MySQL error-number facts (1213/1205), not independently re-derived against the pgx/go-sql-driver source this session
- `testcontainers.SkipIfProviderIsNotHealthy` panic bug — WebSearch, single GitHub issue (#2859) as the source

## Metadata

**Confidence breakdown:**
- Standard stack (dependency versions): HIGH — every new package version was checked directly against the Go module proxy this session, not training data
- Architecture (claim-query mechanics): HIGH — verified against Phase 1's actual generated code plus ent's own PR source, the phase's single highest-priority unknown
- Architecture (mixin/enum/privacy mechanics): MEDIUM-HIGH — verified against ent's official source directly, but read from `master` branch rather than the exact pinned `v0.14.6` tag (flagged as A1 in Assumptions Log)
- Observability footprint analysis: HIGH — read every relevant non-test source file's import block directly this session
- Crash-harness mechanics (testcontainers, SIGKILL, sentinel sync): MEDIUM — standard, well-established Go patterns, cross-checked but not built/run in this research session
- Retryable-error classification: MEDIUM-LOW — two well-known Postgres codes confirmed, not an exhaustive derivation (explicitly Claude's Discretion per CONTEXT.md)
- Pitfalls: MEDIUM-HIGH — two net-new pitfalls (testcontainers skip bug, MySQL partial-index gap) are each single-source but concrete and independently checkable; PITFALLS.md's existing three (zombie runs, dialect divergence, false-confidence harnesses) remain the primary reference

**Research date:** 2026-08-15
**Valid until:** 30 days for the ent-source-derived mechanics (stable API surface, low churn risk); 7-14 days for exact dependency versions (testcontainers-go/otel/pgx all ship frequently) — re-verify versions immediately before the plan's dependency-installation task executes if more than ~2 weeks have elapsed since this research.
