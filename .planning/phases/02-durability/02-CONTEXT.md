# Phase 2: Durability - Context

**Gathered:** 2026-08-15
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 2 makes Phase 1's hand-usable flow **durable**: a persisted run row per execution, a worker that claims runs with `SELECT ... FOR UPDATE SKIP LOCKED` and advances them one step per claim, crash-resume proven against a real Postgres, and runs exposed as ordinary privacy-governed ent entities an operator can query and cancel. This is also where the deterministic crash-simulation harness — the project's release gate — is born, in both of its tiers.

The phase is proven when a worker killed at **any** step boundary (logically, and by real `SIGKILL` against a real subprocess) is resumed by a different worker to the same terminal state, with no step effect ever committed without its progress record.

**Explicitly NOT in this phase:**
- The Activity three-beat protocol, idempotency keys, leases, and declared `Retry(Backoff(...))` execution semantics — Phase 3. Phase 2 keeps `ErrRequiresDurableRun`'s *spirit*: a flow containing Activity or Emit steps still refuses to run durably until its phase lands.
- Outbox rows, the relay, `tx.OnCommit` nudging, NATS, flow chaining — Phase 4.
- All code generation, including the injected run entity — Phases 5-6. Phase 2's run entity is **hand-written**, exactly as the design doc's v0.2 prescribes, and its shape is the contract Phase 5 must later reproduce.
- Manual retry of a failed run (OPS-03) — Phase 3.
- Durable timers / `Sleep` — v2 (TIME-01).

</domain>

<decisions>
## Implementation Decisions

Decision IDs continue Phase 1's project-wide sequence (Phase 1 ended at D-22).

### Run Entity and the Worker↔Application Seam

- **D-23:** The run entity is **hand-written** in Phase 2, as `CancelOrderFlowRun` in `internal/testdata/ent/schema/`. entflow ships `entflow.RunMixin()` — an `ent.Mixin` carrying every framework-owned field (state, input, current step, attempt, last error, results, retry_after, trace context, timestamps) — so a user's hand-written run schema is a mixin, a step-derived state enum, and one edge. Phase 5's injected entity must emit **exactly this shape**; the mixin is what makes hand-written and generated run entities the same artifact rather than two divergent ones. — **Reversibility:** one-way — these column names become the persisted database schema and, from Phase 5 onward, the shape codegen is contractually obliged to emit; changing them later is a data migration for every adopter.

- **D-24:** entflow core can never name the application's generated types, so the worker reaches run rows through an **`entflow.RunStore` port** — a small interface (claim, load, advance, fail, cancel, record-result) implemented by a hand-written per-flow adapter over the generated ent client. The adapter is hand-written in Phase 2 and generated in Phase 6. All adapter methods take the transaction as `any`, matching the type erasure Phase 1 already established for `Exec(ctx, tx any, in In)` and `WithSelfStatus` — no new erasure mechanism is invented. — **Reversibility:** costly — the port's method set is what Phase 6's codegen templates target and what every hand-written adapter implements.

- **D-25:** An **`entflow.Engine`**, constructed once at boot, binds the flow registry, the per-flow `RunStore`s, and the DI registry. Starting a run is a package-level generic function — `entflow.Start[In](ctx, eng, f, in) (*entflow.Run, error)` — mirroring the shape Phase 1 already chose for `RunInTx[In, T]`, because Go methods cannot declare their own type parameters. DUR-01's literal `flow.Start(ctx, in)` is therefore **not** achievable in Phase 2 without an ambient global, which D-15 already rejected; Phase 6's codegen restores the design-doc ergonomics as a generated typed facade (`client.CancelOrder.Start(ctx, in)`). Record this deviation in the phase summary — it is a deliberate, documented gap, not an oversight. — **Reversibility:** costly — `Start`'s shape appears in every caller (RPC handler, cron, CLI, test).

- **D-26:** The lineage edge (DUR-02) is declared, never inferred: a new **`entflow.WithOwnerRef[In, ID]`** `FlowOption` supplies `func(In) (ID, error)`, type-erased exactly like `WithSelfStatus`. The `RunStore` adapter uses it to set the edge to the owning aggregate row. This preserves the project invariant that codegen never infers facts by inspecting closure bodies (ARCHITECTURE.md Anti-Pattern 3).

### Run State Model

- **D-27:** The run `state` enum is genuinely **derived from the flow's step graph**, per design doc §3.5: `pending`, `running`, `done`, `cancelled`, plus one `failed:<step>` value per step. The literal colon form is achieved with ent's `field.Enum(...).NamedValues(...)`, which decouples the Go constant (`StateFailedRefund`) from the stored value (`failed:refund`) — so DUR-07's `failed:<step>` is literally what an operator sees in a SQL query, and Phase 5's "annotation derived from the step graph" claim has real content rather than being a fixed five-value enum. — **Reversibility:** one-way — the persisted enum values and the transitions annotation derived from them are a database migration and a published ops contract.

- **D-28:** Run row columns (all on `RunMixin`): `state`, `input` (codec bytes), `current_step`, `attempt`, `last_error`, `results` (JSON map, step name → raw result), `retry_after` (nullable), `trace_context`, `created_at`, `updated_at`, `started_at`, `finished_at`. Plus the schema-declared edge from D-26.

- **D-29:** `retry_after` is the single column gating claimability by time. It is also the natural future seat for TIME-01's `wake_at`, but Phase 2 ships **no** timer semantics — the column exists solely to keep a backing-off run from spinning the claim loop.

### Claim Loop

- **D-30:** **The claim transaction *is* the step transaction.** One transaction per claim: `SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1` → hydrate the run → execute exactly one DB step's closure → write the step's effect and the run's progress pointer → commit. This is the load-bearing decision of the phase: DUR-03 (effect and progress commit together), DUR-05 (a crash rolls the whole thing back, leaving the row claimable), and the absence of any lease all fall out of it structurally rather than being enforced by discipline. It is also why ARCHITECTURE.md's Anti-Pattern 2 ("no lease across the flow") is correct for Phase 2 and why PITFALLS Pitfall 1's zombie-run gap does not open until Phase 3's Activities split the claim from the execution. — **Reversibility:** one-way — every crash-safety claim the project makes, and the entire harness, is written against this invariant.

- **D-31:** One run per claim transaction (`LIMIT 1`), never a batch — a batch would hold row locks across the execution of unrelated runs' steps. Concurrency comes from N independent worker goroutines each running their own claim transaction. Default `Concurrency: 4`, configurable; forced to 1 on SQLite (D-36).

- **D-32:** Claim predicate: `state IN ('pending','running') AND (retry_after IS NULL OR retry_after <= now()) ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`. `done`, `cancelled`, and every `failed:<step>` value are terminal and structurally unclaimable — cancellation (D-42) therefore needs no separate stop signal.

- **D-33:** Poll interval defaults to **1s with ±25% jitter** (jitter is not optional — it is the documented mitigation for PITFALLS' poll-storm trap). An in-process nudge channel lets `Start` wake a co-located worker immediately. Cross-process wakeup (`LISTEN`/`NOTIFY`, `tx.OnCommit`) is **deferred**: OUT-04 brings `tx.OnCommit` in Phase 4, and polling remains the correctness backstop in every topology.

- **D-34:** The fixture run schema declares a **partial index on claimable states** using `entsql.IndexWhere`, and the dialect matrix doc states plainly that this is Postgres-specific and is required operational guidance, not an optimization — PITFALLS names unpruned terminal rows as River's dominant real-world failure mode. Row pruning/archival itself is **not** built in Phase 2 (it is an ops concern with no requirement behind it); the index is what keeps the claim query flat without it.

### Dialect Support Matrix (DUR-08 — resolves the STATE.md Phase 2 blocker)

- **D-35:** The matrix is ratified as:
  - **PostgreSQL 12+ — first-class.** The only dialect the crash-simulation release gate certifies. `FOR UPDATE SKIP LOCKED`.
  - **MySQL 8.0.1+ — compatible, uncertified.** The claim query is valid and the strategy is implemented, but the release gate does not run against it; docs say "works, not certified at scale" rather than implying parity.
  - **SQLite — development and test only, single worker.** No row-level locking, no `SKIP LOCKED`. Supported so ent's own quickstart audience is not met with an opaque SQL error, never presented as a production multi-worker option.
  - **Anything else (including Gremlin) — refused at startup.**

- **D-36:** A **`ClaimStrategy`** interface, selected by dialect at worker construction, isolates the one query that cannot be written portably. Postgres/MySQL share the `FOR UPDATE SKIP LOCKED` strategy. SQLite's strategy is a plain `SELECT ... ORDER BY id LIMIT 1` issued inside a write ("immediate") transaction — SQLite's whole-database write serialization provides exactly the mutual exclusion `SKIP LOCKED` provides elsewhere, so D-30's invariant still holds — and `worker.New` returns a named error if `Concurrency > 1` on SQLite rather than silently serializing.

- **D-37:** Dialect validation happens at **`worker.New`**, not lazily at first claim, and the error names the dialect and points at the matrix document. A silent or deferred failure here is the exact trap PITFALLS Pitfall 7 describes.

- **D-38:** A small **claim-strategy conformance suite** (concurrent claimers must never double-claim a row; a rolled-back claim must leave the row claimable) runs against all three dialects. The crash-simulation harness runs against Postgres only, and says so in its own output — "passes against Postgres" is the claim being made, not "passes everywhere."

### Result Persistence and Self Threading

- **D-39:** Step results persist in the run row's `results` JSON column, written in the **same transaction** as the step's effect and progress pointer. `entflow.Result[T](ctx, "step")` keeps its Phase 1 signature exactly, honoring D-11's promise: the executor hydrates the ctx-carried result store from the run row at claim time, and the store's backing changes with no call-site change.

- **D-40:** Results **always** round-trip through JSON — including within a single uninterrupted worker pass, where the in-memory value is still available. Determinism beats convenience here: if the in-memory path returned a live value and the post-crash path returned a rehydrated one, every flow would have two behaviors and the harness would prove the wrong one. Documented consequence: a value obtained from `Result[T]` is **inert data**, not a live ent entity — unexported fields, the ent config handle, and loaded-edge state do not survive. — **Reversibility:** costly — flows written against live-entity semantics would all break if this were tightened later.

- **D-41:** The **positionally-threaded `self` entity** (`Activity(f, "refund", func(ctx, self *ent.Order, att))` and the `*Self` DB steps) is a different case and gets different treatment: it is **re-read from the database** at the start of each step's transaction via a declared `entflow.WithSelfLoader` `FlowOption` (a sibling of `WithSelfStatus`), never rehydrated from persisted JSON. `self` stays a live, current entity; `Result[T]` values stay inert data. Both are honest; the asymmetry is deliberate and must be documented as such.

- **D-42:** `SelfWas` keeps D-10's semantics — a snapshot taken at flow entry — but the snapshot now **persists on the run row** rather than living in memory, which is precisely what D-10 anticipated when it chose entry-snapshot semantics in Phase 1.

### Cancellation, Privacy, and the Workflow Marker

- **D-43:** Cancellation (OPS-04) is a **state transition, not a signal**: a conditional update to `cancelled` permitted only from `pending`/`running`. Because the claim transaction holds the row lock for the whole step (D-30), a cancel issued mid-step simply blocks until that step commits, then applies; and because terminal states are unclaimable (D-32), the run is never picked up again. The step-advance write is additionally guarded (`WHERE state = 'running' AND current_step = ?`) so a completing step can never resurrect a cancelled run. An in-flight step is allowed to finish — cancellation is "no further steps," not "abort mid-step," and the docs must say so.

- **D-44:** entflow ships an **unexported context marker type** set only inside the worker's transaction-opening path, with `entflow.IsWorkflow(ctx) bool` as the only public surface. This establishes SM-05/GEN-07's seam three phases early and at zero cost, and a test asserts no exported API — including test helpers shipped in the public module — can set it. PITFALLS' security table names a settable marker as a privilege-escalation path; closing it now is cheaper than auditing for it later.

- **D-45:** entflow core ships **no reusable privacy rules** in Phase 2 — it cannot name the application's viewer type. OPS-02 is proven by the fixture run schema declaring an ordinary `Policy()` (viewer required; owner-scoped read; cancel restricted), and the worker obtains its own viewer from an app-supplied `worker.Options.Context func(context.Context) context.Context`. Generated `DenyStatusEscalation` remains Phase 6's job.

- **D-46:** OPS-01 requires **no entflow code at all** — a run is an ordinary ent entity, so "this order's runs" is a generated query. The phase proves it with a test that queries runs through the generated client with privacy engaged, rather than by building anything.

### Worker Packaging, Topology, and Lifecycle

- **D-47:** The worker lives at **`entflow/worker`**, a subpackage of the root module (not a satellite) — it needs only `*ent.Client`-shaped access through the `RunStore` port, so it carries no dependency the core does not already have. API: `worker.New(eng, worker.Options{...})`, `w.Run(ctx) error`, `w.Shutdown(ctx) error`.

- **D-48:** Both DUR-09 topologies are the *same* object: in-process is `go w.Run(ctx)` alongside the API server; the dedicated binary is a ~20-line `main` that constructs the same worker. `internal/testdata/crashworker/` is that worked example — and it is simultaneously the kill target for the harness's tier 2, so the documented topology is the one actually under test. No generated `main` helper in Phase 2 (that is open question #4, and codegen's job).

- **D-49:** Graceful shutdown (DUR-10): stop claiming, let in-flight step transactions commit, then exit; past a drain deadline, cancel the step contexts so their transactions roll back. Either way no run is abandoned mid-step — a rolled-back step leaves the run exactly as claimable as it was, which is the same property D-30 gives a crash. The docs should state this as "shutdown is a crash we happened to be polite about," because that framing is what makes the guarantee obviously true.

- **D-50:** PITFALLS' connection-pool trap is addressed by **documentation plus an option**, not by a mandate: `worker.Options` accepts its own `*ent.Client`, and the docs recommend a separately-sized pool for the worker path when running in-process with an API server.

### DB-Step Retry

- **D-51:** DUR-07 says "after retries exhaust," so Phase 2 ships the minimum retry that makes that sentence true: an attempt counter, `retry_after` backoff scheduling, and a conservative built-in policy (3 attempts, exponential 100ms→2s, jittered) applied **only to retryable driver errors** — serialization failure, deadlock, connection loss. Every other error fails the run to `failed:<step>` immediately, because retrying a business-logic error just burns the counter. The declared `Retry(Backoff(...))` policy from CORE-09 governs Activities and takes effect in Phase 3; Phase 2 does not wire it to DB steps.

### Crash-Simulation Harness (TEST-01, TEST-02 — the release gate)

- **D-52:** Two tiers, named in the harness's own output so "the harness passed" is never ambiguous:
  - **Tier 1 — logical crash points.** In-process, deterministic, the bulk of the matrix. Implemented as an `internal/crashpoint` registry that is a single nil check in production builds and needs no build tag.
  - **Tier 2 — real process kill.** A subprocess `crashworker` binary `SIGKILL`ed at a controlled point. Fewer cases, but this is the only tier that tests the claim the release gate actually makes.

- **D-53:** Tier 2 synchronization is **DB-state-driven plus a sentinel file** at the target crash point — never a sleep. The worker blocks at the injected point, writes the sentinel, the test observes it (and the expected DB state), then kills. PITFALLS Pitfall 9 is explicit that sleep-based synchronization is how these harnesses become flaky and then get "fixed" into meaninglessness.

- **D-54:** Both tiers run against **real Postgres via `testcontainers-go`** (one container per package, fresh schema per test). When Docker is unavailable the tests skip with a loud message; the release-gate CI job sets `ENTFLOW_REQUIRE_CRASHSIM=1`, which turns every such skip into a failure. A silently-skipped release gate is worse than no gate.

- **D-55:** Assertions are **absence claims**, not just terminal-state equality. For every crash point: (effect present ∧ progress present) or (effect absent ∧ progress absent) — never one without the other; terminal state after resume identical to the uncrashed baseline; and a side-effect counter on the fixture asserting no duplicated effect. Checking only "it ended up in the right state" would pass a run that duplicated an effect and painted over it.

- **D-56:** The crash-point matrix is **enumerated from the flow's step graph**, not hand-listed, so adding a step automatically extends coverage and Phase 3/4 can extend the same harness to activity beats and relay boundaries (which the roadmap already commits to).

### Observability (OPS-05)

- **D-57:** entflow core takes a direct dependency on **`go.opentelemetry.io/otel/trace` (API only — never the SDK)**, and D-04's META-02 dependency test gains a **narrow, named exception** for the `go.opentelemetry.io/otel*` API packages. This touches a Phase 1 decision deliberately and is the one decision in this phase most worth a second look. Rationale: META-02's stated intent is "no transport, protobuf, or descriptor machinery," which the OTel API is not; `.planning/research/STACK.md` recommends exactly this; and the alternative — a bespoke `entflow.Tracer` interface plus an `entflow/otel` satellite module — gives users a worse API to instrument a library that OTel's API package was explicitly designed for. The exception is narrow by construction: any *other* new module still fails the test. **The alternative remains available** — if the "ent only" constraint is read literally, the satellite-module shape is a mechanical change at this point in the project and gets more expensive every phase after. — **Reversibility:** costly — it is a published dependency of the core module.

- **D-58:** One root span per run, one child span per step named `workflow.<Flow>.<step>`. IDs never appear in span names; `entflow.run_id`, `entflow.flow`, `entflow.step`, `entflow.attempt`, `entflow.state` are attributes, and errors go through `RecordError`. The `TracerProvider` defaults to the global no-op, so an application that wires nothing pays nothing.

- **D-59:** Because a run's steps may execute in **different processes** across a crash or a re-claim, the run's trace context is **persisted on the run row** (D-28's `trace_context`) and restored as the parent/link at each claim. Ambient context propagation cannot survive the resume boundary — PITFALLS names this precisely, and it is invisible until someone looks at a trace for a crashed run and finds two unrelated traces.

### Claude's Discretion

`--auto` mode selected the recommended option for every decision above; the user constrained none of them. Areas where the planner retains latitude:

- The exact method set and naming of the `RunStore` port, provided it stays tx-erased (D-24) and expresses claim / hydrate / advance / fail / cancel / record-result.
- Internal layout within `entflow/worker` (one file vs. several) — ARCHITECTURE.md's suggested `claim.go` / `dbstep.go` / `loop.go` split is a recommendation, not a contract.
- Whether `Engine` is a struct or an interface, and whether per-flow `RunStore`s are registered on it individually or as a map.
- Concrete backoff constants in D-51, and the exact retryable-error classification per driver.
- Test organization for the harness (table-driven vs. generated subtests), as long as D-56's graph-derived enumeration holds.
- Whether the fixture's side-effect counter (D-55) is a dedicated table or a column on `Order`.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project intent (ground truth)
- `entflow.md` — the design document. **§5.1** (starting a run), **§5.2** (worker loop — the claim query and the "advance state in the same tx" rule), **§3.5** (runs are rows: the injected entity's shape, the state enum derived from the step graph, privacy-governed runs, admin retry as a transition), **§3.6** (input codec — the run row persists the input), **§6** (observability — span naming, the run table as audit log), **§7** (testing strategy: the crash-simulation release gate, stated verbatim). §10.4 records the worker-topology open question this phase answers by shipping both.
- `.planning/PROJECT.md` — Core Value, Constraints (note the "entflow depends on ent only" constraint, which D-57 amends narrowly and deliberately), Key Decisions table.
- `.planning/REQUIREMENTS.md` — DUR-01…DUR-10, OPS-01, OPS-02, OPS-04, OPS-05, TEST-01, TEST-02 are this phase's contract. ACT-04's lease is explicitly *not*.
- `.planning/ROADMAP.md` §"Phase 2: Durability" — goal and the five success criteria.
- `.planning/STATE.md` — carries the open blocker "Dialect support matrix is a research recommendation, not yet a ratified project decision — must be explicitly decided during Phase 2 planning, before the claim query is finalized." **D-35 resolves it**; the blocker should be closed when this phase completes.

### Prior phase (locked, do not re-litigate)
- `.planning/phases/01-runtime-core/01-CONTEXT.md` — D-01…D-22. Directly load-bearing here: **D-04** (the META-02 dependency test, amended by D-57), **D-08** (`Exec(ctx, tx, in)` is the seam this phase's worker calls into), **D-10** (`SelfWas` entry-snapshot semantics, now persisted — see D-42), **D-11** (`Result[T]`'s signature must not change — see D-39), **D-13** (`ErrRequiresDurableRun` for Activity/Emit), **D-15/D-16** (scoped DI registry, panic-recovery at step boundaries), **D-17/D-18** (codec, exercised in tests specifically so this phase could depend on it), **D-21/D-22** (SQLite is a Phase 1 test choice and explicitly *not* a production statement — D-35 is the ratification it deferred).

### Research (read before planning)
- `.planning/research/PITFALLS.md` — the highest-value document for this phase. **Pitfall 1** (zombie runs / why no lease is needed while claim and step share a transaction, and exactly when that stops being true in Phase 3), **Pitfall 7** (dialect divergence — the source of D-35/D-36/D-37), **Pitfall 9** (false-confidence crash harnesses — the source of D-52 through D-55, including the sleep-vs-DB-state synchronization rule). Also the Performance Traps table (poll-storm jitter, terminal-row bloat, shared connection pool) and the Security Mistakes table (the workflow marker, D-44).
- `.planning/research/ARCHITECTURE.md` — **Pattern 3** (claim-execute-advance, and the explicit note that entflow's claimed unit needs *multiple* claim cycles unlike a job queue), **Anti-Pattern 2** (no lease across the flow), **Anti-Pattern 1** (never hold a tx across a network call), the Data Flow section's per-transaction ownership summary, and the suggested `worker/` package layout.
- `.planning/research/STACK.md` — pgx v5 in `stdlib` mode through `database/sql`, why the claim query must be raw SQL, testcontainers-go over dockertest/embedded-postgres, and the OTel API-vs-SDK library boundary underpinning D-57.
- `.planning/research/SUMMARY.md` — roadmap implications and gaps.

### Phase 1 source (the seams this phase extends)
- `exec.go` — `Exec(ctx, tx any, in In)`, `runStep`'s recover boundary, `topoOrder`, `RunInTx`, `WithSelfStatus`'s type-erasure pattern (the model for D-26's `WithOwnerRef` and D-41's `WithSelfLoader`).
- `flow.go` — `FlowOf[In]`, `flowConfig`, `FlowOption`, `WithOwner`, `Meta()`, `FlowsOf`.
- `result.go`, `codec.go`, `step.go`, `stepoptions.go`, `errors.go` — the result store to be re-backed (D-39), the codec that now persists input, step data, and `StepError`.
- `internal/testdata/ent/schema/order.go` and `order_flows.go` — the fixture the run entity attaches to and the worked `CancelOrder` example.

### Upstream source (the de-facto documentation)
- `entgo.io/ent` — `schema/mixin` (D-23's `RunMixin`), `field.Enum(...).NamedValues(...)` (D-27), `entsql.IndexWhere` (D-34), `entgo.io/ent/privacy` (D-45), and the `dialect/sql` driver surface for reaching the underlying `*sql.DB` to run the raw claim query.

### External docs
- No external ADRs or specs exist for this project — `entflow.md` is the sole design artifact, and the decisions above supplement it.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

Phase 1 shipped a working, tested runtime — this phase extends it rather than starting anything new:

- **`FlowOf[In].Exec(ctx, tx any, in In)`** (`exec.go`) — already accepts an erased transaction and already refuses Activity/Emit flows. Phase 2's worker calls exactly this seam; the change is that it must execute **one step per call** rather than the whole graph, which means `Exec`'s loop needs a resumable entry point (execute step *k* only) alongside the existing all-steps form.
- **`topoOrder`** (`exec.go`) — the deterministic step ordering the worker needs to answer "what is the next step after `current_step`", already written and tested, with declaration order as the tie-break.
- **`WithSelfStatus`'s erasure pattern** (`exec.go`) — a generic option capturing a typed closure into a `func(ctx, any, any)` adapter, with the `In` type recorded so `New[In]` can reject a mismatch at the declaration site. D-26 and D-41 copy this pattern exactly; do not invent a second one.
- **`withResults`/`putResult`** (`result.go`) — the per-execution result store, deliberately built as an unexported seam so Phase 2 could re-back it (D-39).
- **The codec surface** (`codec.go`, `json.go`) — D-18 exercised the round-trip in Phase 1 tests specifically so this phase could persist `input` with a locked, proven interface.
- **`runStep`'s recover boundary** (`exec.go`) — already converts a panicking closure into a `*StepError` rather than killing the process; a long-running worker depends on this and it is already in place.
- **`internal/testdata/ent`** — a fully generated ent package with an `Order` entity, a transitions-annotated status enum, and the `CancelOrder` flow. The run entity attaches here; the `Hooks()` passthrough that forces ent's `ent/runtime` split is already present and must not be removed.
- **`meta.FlowMeta`** — the immutable topology snapshot the worker uses to resolve step order and kinds without touching closures.

### Established Patterns

- **Type erasure at the entflow↔generated-code boundary.** `tx any`, `in any`, with a typed generic constructor capturing the closure. Every new boundary in this phase (`RunStore`, `WithOwnerRef`, `WithSelfLoader`) follows it.
- **Package-level generic functions, not methods**, wherever a second type parameter is needed (`RunInTx[In, T]`, `UpdateSelf[...]`) — because Go methods cannot declare type parameters. D-25's `Start[In]` is the same shape.
- **Declared, never inferred.** `WithSelfStatus` exists rather than inspecting `*Self` steps; `WithOwner` exists rather than deducing the owning entity. D-26 continues this, and it is the invariant Phase 5 depends on.
- **Honest refusal over a working-but-unsafe path** (D-13's `ErrRequiresDurableRun`). Phase 2 keeps it: Activity/Emit steps stay unexecutable until their phases land, even inside a durable run.
- **Machine-checked invariants over documented ones** — D-04's dependency test, D-19's closure-invocation counter. D-44's "no exported API can set the workflow marker" and D-38's claim-strategy conformance suite follow the same style.
- Tests exercising unexported seams live in `package entflow` (not `entflow_test`); the split is already established.

### Integration Points

- **`Exec` → per-step execution.** The single biggest structural change in the phase: the worker needs "run step *k* of this flow in this transaction," and `Exec`'s current all-steps loop becomes a caller of that primitive. Treat the per-step entry point as the contract Phase 6's generated runner will target.
- **`RunStore` port ↔ generated ent client.** The one place application-specific generated types enter; everything above it stays generic.
- **The run entity ↔ ent's migration, hook, and privacy stacks.** The run is an ordinary entity, which is what buys OPS-01 and OPS-02 for free — do not build a bespoke query or access layer over it.
- **Raw SQL claim query ↔ the same `*sql.DB` ent wraps.** The claim must execute on the same connection/transaction ent is using, then hand off to the generated client inside that transaction. Ent's query builder cannot express `FOR UPDATE SKIP LOCKED`.
- **`internal/testdata/crashworker`** — new, and simultaneously the DUR-09 dedicated-binary example and tier 2's kill target.

</code_context>

<specifics>
## Specific Ideas

- The worked example stays the `Order` / `CancelOrder` flow. In Phase 2 the flow's DB step (`cancel`) executes durably; its Activity (`refund`) and Emit (`order.cancelled`) still refuse to run. That means the phase's headline demo is a **single-DB-step durable run** — which is exactly right: it isolates worker-loop bugs from activity-protocol bugs, as ARCHITECTURE.md's build-order argument recommends. The harness needs a **multi-DB-step** fixture flow as well, since "crash at every step boundary" is vacuous with one step.
- The harness's tier-2 output should read like a report, not a test log: which tier ran, which dialect, how many crash points, which were skipped and why. "The crash-simulation harness passes" is invoked as a release gate from this phase forward, and it must be unambiguous what that sentence claims.
- The framing to use in docs for shutdown (D-49): *"shutdown is a crash we happened to be polite about."* If graceful shutdown needs any mechanism crash-resume does not already provide, that is a signal the crash-resume story is incomplete.
- The dialect matrix should ship as a real document (README section or `docs/dialects.md`), not as a comment — DUR-08 requires the developer to *see* it, and PITFALLS predicts a confused issue from the first SQLite user otherwise.
- The vibe-coding guardrail lens from `entflow.md` §2 remains the acceptance test for API decisions: for each choice, ask whether wrong generated code could still commit. D-30 is the phase's strongest answer — with claim and step in one transaction, there is no ordering a generated runner could get wrong that would commit an effect without its progress record.

</specifics>

<deferred>
## Deferred Ideas

- **Activity lease / heartbeat (`claimed_by`, `lease_expires_at`)** — ACT-04, Phase 3. Not needed in Phase 2 precisely because D-30 keeps claim and execution in one transaction; it becomes necessary the moment the three-beat protocol splits them. STATE.md already carries this as a Phase 3 blocker needing its own design pass.
- **Manual retry of a failed run** — OPS-03, Phase 3.
- **Outbox rows, the relay, `tx.OnCommit` nudging, NATS, flow chaining** — OUT-01…OUT-05, Phase 4. The relay reuses this phase's claim primitive wholesale, so keep `ClaimStrategy` (D-36) general enough to point at a different table.
- **`LISTEN`/`NOTIFY` cross-process wakeup** — a latency optimization, not a correctness mechanism. Revisit when poll latency is measured to be a real problem, or alongside Phase 4's `tx.OnCommit` work.
- **Terminal-row pruning / archival** — a real operational need (PITFALLS names it as River's dominant failure mode) with no requirement behind it in v1. D-34's partial index is the Phase 2 mitigation. SCAL-02 (bulk replay/cancel) is the adjacent v2 item.
- **River backend adapter** — SCAL-01, v2. D-36's `ClaimStrategy` seam is roughly where it would attach; do not design for it now.
- **Durable timers / `entflow.Sleep`** — TIME-01, v2, recommended as the first fast-follow after v1. D-29's `retry_after` is its eventual seat; do not build timer semantics on it now.
- **Generated `main` helpers for the worker binary** — design doc open question #4, codegen's job (Phase 6). Phase 2 ships a hand-written example instead.
- **Static analysis flagging `entflow.Use[T]` inside DB-step closures** — PITFALLS Pitfall 2's suggested mitigation, explicitly placed at v0.5 (codegen). Phase 2's contribution is the documentation half: "DB steps touch only the database."
- **`entflowtest` package** — DX-01, v2. Build only what this phase's own tests need.
- **Provider idempotency-key TTL / stale-retry warnings** — Pitfall 3, Phase 3 (ACT-06).

Every item above is a pre-existing roadmap or research item carried forward — `--auto` mode surfaced no new user-initiated capabilities, so nothing here is scope creep redirected out of the phase.

</deferred>

---

*Phase: 2-Durability*
*Context gathered: 2026-08-15*
</content>
</invoke>
