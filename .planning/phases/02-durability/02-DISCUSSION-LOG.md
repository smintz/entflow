# Phase 2: Durability - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-08-15
**Phase:** 2-Durability
**Mode:** `--auto` — no interactive prompts; the recommended option was auto-selected for every gray area and logged below.
**Areas discussed:** Run-entity ownership & worker seam, Dialect support matrix, Claim-loop mechanics, Run state model, Start ergonomics & lineage, Result persistence & self threading, Cancellation & privacy, Worker packaging & lifecycle, DB-step retry, Crash-simulation harness, Observability dependency boundary

---

## Run-entity Ownership and the Worker↔Application Seam

| Option | Description | Selected |
|--------|-------------|----------|
| `RunStore` port + hand-written per-flow adapter, framework fields on an `entflow.RunMixin` | Core stays free of generated types; the mixin makes the hand-written and later-generated run entity the same artifact | ✓ |
| Raw SQL against a conventionally-named table, no ent entity | Simplest worker, but forfeits OPS-01/OPS-02 (runs are ordinary privacy-governed ent entities) entirely | |
| Reflection over the generated client to discover the run type | Avoids the adapter, but reintroduces the "infer rather than declare" pattern the project has ruled out | |

**Choice:** `RunStore` port + `RunMixin` (D-23, D-24).
**Notes:** Method signatures take the tx as `any`, reusing Phase 1's existing erasure rather than inventing a second mechanism. Phase 6 generates the adapter.

---

## Dialect Support Matrix (DUR-08)

| Option | Description | Selected |
|--------|-------------|----------|
| Postgres first-class / MySQL compatible-uncertified / SQLite dev-test single-worker / else startup error | Honest tiered boundary; every tier says exactly what it claims | ✓ |
| Postgres only, refuse everything else | Cleanest guarantee, but meets ent's SQLite-quickstart audience with a hard wall | |
| Claim it works everywhere ent works | Silently degrades on SQLite (no row locks) — the exact trap PITFALLS Pitfall 7 describes | |

**Choice:** Tiered matrix, ratified (D-35 through D-38).
**Notes:** Closes the open blocker STATE.md carried for this phase. A `ClaimStrategy` interface isolates the one non-portable query; SQLite forces `Concurrency: 1`; validation happens at `worker.New`, not lazily.

---

## Claim-Loop Mechanics

| Option | Description | Selected |
|--------|-------------|----------|
| The claim transaction *is* the step transaction; one run per claim; re-claim at each step boundary | DUR-03/DUR-05/DUR-06 fall out structurally, and no lease is needed | ✓ |
| Claim, commit, then execute the step in a second transaction | Requires a lease immediately and opens the zombie-run gap in Phase 2 rather than Phase 3 | |
| Batch-claim N runs per transaction | Holds row locks across unrelated runs' step execution | |

**Choice:** Single-transaction claim-execute-advance (D-30 through D-34).
**Notes:** Poll default 1s with ±25% jitter; in-process nudge from `Start`; `LISTEN`/`NOTIFY` deferred. Partial index on claimable states declared on the fixture.

---

## Run State Model

| Option | Description | Selected |
|--------|-------------|----------|
| `pending`/`running`/`done`/`cancelled` + one `failed:<step>` value per step, via `NamedValues` | DUR-07's `failed:<step>` is literally what an operator sees; the enum is genuinely step-graph-derived for Phase 5 | ✓ |
| Fixed five-value enum, failing step recorded in `current_step` only | Simpler, but makes §3.5's "annotation derived from the step graph" hollow | |
| Free-text state column | No transitions annotation possible; forfeits the dogfooding story | |

**Choice:** Step-derived enum with `NamedValues` (D-27, D-28).
**Notes:** Colons are legal as stored values because ent decouples the Go constant name from the value.

---

## Start Ergonomics and Lineage

| Option | Description | Selected |
|--------|-------------|----------|
| `entflow.Start[In](ctx, eng, f, in)` package-level generic + `WithOwnerRef` option | Matches Phase 1's `RunInTx[In, T]` shape; lineage is declared, never inferred | ✓ |
| `flow.Start(ctx, in)` backed by a package-level global engine | Matches the design doc literally, but D-15 already rejected process-global state | |
| Infer the owning row by inspecting the flow's `*Self` step closures | Violates the "codegen never inspects closure bodies" invariant | |

**Choice:** Package-level generic `Start` + explicit `WithOwnerRef` (D-25, D-26).
**Notes:** The deviation from DUR-01's literal wording is deliberate and documented; Phase 6 codegen restores `client.CancelOrder.Start(ctx, in)`.

---

## Result Persistence and Self Threading

| Option | Description | Selected |
|--------|-------------|----------|
| Persist results as JSON; always round-trip, even in-memory; re-read `self` live from the DB each step | One behavior crash or no crash — the harness then proves the real thing | ✓ |
| Serve in-memory when available, rehydrate only after a crash | Two behaviors per flow; the harness proves the wrong one | |
| Rehydrate `self` from JSON like any other result | `self` stops being a live ent entity mid-flow | |

**Choice:** Always round-trip results; re-read `self` (D-39 through D-42).
**Notes:** `Result[T]`'s Phase 1 signature is unchanged, honoring D-11. Documented consequence: `Result[T]` values are inert data, not live entities.

---

## Cancellation and Privacy

| Option | Description | Selected |
|--------|-------------|----------|
| Cancel as a guarded state transition; terminal states unclaimable; step-advance guarded against resurrection | Serialized by the same row lock the step already holds; no separate signal channel | ✓ |
| Cooperative context cancellation of the in-flight step | Aborts mid-step, which the atomic-step model makes unnecessary and harder to reason about | |
| A `cancel_requested` flag polled by the worker | A second source of truth alongside `state` | |

**Choice:** Cancellation as a transition (D-43); unexported workflow marker with no public setter (D-44); no reusable privacy rules in core (D-45).
**Notes:** In-flight steps finish — cancellation means "no further steps," and the docs must say so.

---

## Worker Packaging and Lifecycle

| Option | Description | Selected |
|--------|-------------|----------|
| `entflow/worker` subpackage; one object serves both topologies; drain-then-cancel shutdown | The documented dedicated-binary example is also the harness's kill target, so it stays honest | ✓ |
| Satellite module for the worker | Unnecessary — the worker adds no dependency the core lacks | |
| Generated `main` helper in Phase 2 | That is open question #4 and codegen's job | |

**Choice:** `entflow/worker` subpackage (D-47 through D-50).
**Notes:** Shutdown framed as "a crash we happened to be polite about" — if it needs machinery crash-resume lacks, crash-resume is incomplete.

---

## DB-Step Retry

| Option | Description | Selected |
|--------|-------------|----------|
| Attempt counter + `retry_after` backoff, conservative built-in policy, retryable driver errors only | Makes DUR-07's "after retries exhaust" true with the minimum surface | ✓ |
| Wire CORE-09's declared `Retry(Backoff(...))` to DB steps now | Pre-empts Phase 3's Activity retry semantics | |
| No retry at all in Phase 2 | Leaves DUR-07's wording unsatisfied | |

**Choice:** Minimal built-in DB-step retry (D-51).
**Notes:** Business-logic errors fail immediately; only serialization failures, deadlocks, and connection loss retry.

---

## Crash-Simulation Harness

| Option | Description | Selected |
|--------|-------------|----------|
| Two named tiers (in-process crash points + subprocess `SIGKILL`), DB-state + sentinel synchronization, absence assertions, real Postgres | The only shape that makes "the harness passed" an unambiguous release-gate claim | ✓ |
| In-process logical crash points only | Fast and deterministic, but proves nothing about real process death (PITFALLS Pitfall 9) | |
| Subprocess kills only | Too slow for matrix coverage across every crash point | |

**Choice:** Two tiers, both required (D-52 through D-56).
**Notes:** Skips are loud, and `ENTFLOW_REQUIRE_CRASHSIM=1` makes them fatal in the release-gate job. Crash points are enumerated from the step graph so Phases 3 and 4 extend the same harness.

---

## Observability Dependency Boundary

| Option | Description | Selected |
|--------|-------------|----------|
| Depend on `go.opentelemetry.io/otel/trace` (API only) + narrow named exception in the META-02 test | What STACK.md recommends; the OTel API exists precisely for libraries; the exception keeps the test's teeth | ✓ |
| Bespoke `entflow.Tracer` interface + `entflow/otel` satellite module | Honors "ent only" literally and matches the planned NATS satellite precedent, at the cost of a worse API | |
| No spans in Phase 2; defer OPS-05 to Phase 6 codegen | Leaves a Phase 2 requirement unmet and gives codegen nothing hand-written to match | |

**Choice:** OTel API in core with a narrow allowlist exception (D-57 through D-59).
**Notes:** This is the one decision in the phase that touches a locked Phase 1 decision (D-04) and a stated project constraint. It is flagged as such in CONTEXT.md, and the satellite-module alternative is recorded as still available — it is mechanical now and gets more expensive every phase. Trace context is persisted on the run row because ambient propagation cannot survive a resume in a different process.

---

## Claude's Discretion

`--auto` mode selected the recommended option for every question; the user constrained none. Latitude left to the planner is enumerated in CONTEXT.md's "Claude's Discretion" section — `RunStore` method naming, `entflow/worker` internal file layout, `Engine`'s concrete shape, backoff constants and retryable-error classification, harness test organization, and the fixture's side-effect-counter shape.

## Deferred Ideas

Recorded in CONTEXT.md's `<deferred>` section: Activity lease/heartbeat (Phase 3), manual retry (Phase 3), outbox and relay (Phase 4), `LISTEN`/`NOTIFY`, terminal-row pruning, River adapter (v2), durable timers (v2), generated worker `main` (Phase 6), DB-step static analysis (Phase 6), `entflowtest` (v2), provider idempotency-key TTL warnings (Phase 3). All were pre-existing roadmap or research items — no new capabilities were introduced during this discussion.
</content>
