# Project Research Summary

**Project:** entflow — durable workflow orchestration as an ent extension ("Temporal-lite for ent")
**Domain:** Go library / entc codegen extension / database-backed durable workflow worker
**Researched:** 2026-08-08
**Confidence:** MEDIUM

## Executive Summary

entflow sits in a real, validated category — "durable execution on your own Postgres, no separate server" — whose closest true peer is DBOS Transact (which added a Go SDK in 2026). The runtime-first, codegen-last build order (v0.1 Multi core → v0.2 durability → v0.3 activities → v0.4 outbox → v0.5 codegen) is architecturally sound: every capability through v0.4 is expressible by hand because ent's own `Hooks()`/`Policy()` precedent (closures bound at runtime via `ent/runtime`, never inspected at codegen time) already proves the schema-package-returns-closures pattern works without any codegen involvement. The flagship differentiator — bidirectional state-machine cross-validation between declared transitions and step claims — has no analog in any surveyed competitor (Temporal, DBOS, Restate, Conductor, River/asynq), and the "runs as privacy-governed queryable ent rows" design gives entflow a free ops dashboard that every competitor had to build as a bespoke product.

The single biggest correction from research: **the design doc's cited precedent for injecting the run entity is wrong.** `entproto` never adds nodes to `gen.Graph` — it only reads an already-built graph to emit `.proto` files. The real, verified precedent is `entgo.io/contrib/schemast`, used by `enthistory`, which performs a two-pass, disk-mediated injection (an AST-manipulation pre-pass writes real `schema/*.go` files *before* `entc.Generate` runs at all, then a normal `entc.Extension` operates on the now-materialized schema). entflow's case is harder than enthistory's because the injected `<Flow>Run` entity's `state` transitions annotation must itself be derived from the flow's own step graph — a nested two-pass bootstrap with no published precedent. This is the single highest-risk unknown in the whole roadmap and should be spiked early, ideally validated by keeping the `Flows()` builder chain AST-extractable from v0.1 onward (never requiring the schema package to compile/reflect to read builder data).

Three other findings should directly shape the roadmap. First, SQLite has **no** `SKIP LOCKED`/`FOR UPDATE` support at all (not degraded — architecturally absent, whole-database-file locking), while ent's own quickstart defaults to SQLite — this needs an explicit, documented dialect matrix decision at v0.2, not a silent Postgres-only assumption that surprises users. Second, durable timers/sleep is the one Out-of-Scope item that doesn't hold up against the competitive landscape — every full-featured competitor treats it as core, and it's architecturally cheap (additive `wake_at` column + claim-query filter) — recommend reclassifying it from indefinite deferral to an explicit v1.x fast-follow; compensation/saga deferral, by contrast, is well-defended and should stay deferred. Third, the crash-safety model has a real gap: the Activity three-beat protocol needs a lease/heartbeat mechanism (not yet specified in the design doc) to avoid zombie runs when a worker dies between beats, and the crash-simulation harness itself needs two tiers (in-process logical-crash-point coverage plus real subprocess SIGKILL coverage) to actually prove the release-gate claim rather than produce false confidence.

## Key Findings

### Recommended Stack

Go 1.25+ with generics as the load-bearing language feature; ent v0.14.6 (pre-1.0, "stable in practice, not stable in promise" — pin exact versions). PostgreSQL 12+ (target 14+) is the only database where the worker's `FOR UPDATE SKIP LOCKED` claim query is a first-class citizen, accessed via `pgx/v5` through `database/sql` (ent has no native non-`database/sql` driver path). The claim query itself must be hand-written raw SQL — ent's fluent builder cannot express `SKIP LOCKED` — executed through the same tx as the generated ent client, exactly the pattern every Postgres-queue library (River, Oban, Solid Queue) uses. `entgo.io/contrib/schemast` is the verified mechanism for run-entity injection (see Architecture below). River is recommended as an optional, adapter-boundary pluggable backend, never a required dependency. OpenTelemetry API-only in entflow's production dependency tree (SDK stays test/example-only). `text/template` via `entc/gen.Template` for all codegen, matching how entc/entgql/entproto already work. `testcontainers-go` (not embedded Postgres, not dockertest) for the crash-simulation harness, since it needs real server crash/kill semantics.

**Core technologies:**
- Go 1.25+ (generics) — required for `entflow.New[In]`, `entflow.Use[T]`, `entflow.Codec[In]`, `entflow.Result[T]`
- entgo.io/ent v0.14.6 — the platform, not a choice; treat extension API as stable-in-practice
- entgo.io/contrib/schemast — the actual (corrected) mechanism for run-entity injection, as a pre-`entc.Generate` pass
- PostgreSQL (pgx/v5 via database/sql) — the only fully-supported worker/queue dialect; `SELECT ... FOR UPDATE SKIP LOCKED` via raw SQL
- go.opentelemetry.io/otel (API only) — per-run/per-step spans, `workflow.<Flow>.<step>` naming
- riverqueue/river (optional adapter) — pluggable backend behind an interface, never competes with the built-in poller
- testcontainers-go + modules/postgres — release-gate crash-simulation harness against a real Postgres process

### Expected Features

Every table-stakes feature in the category (crash-resumable execution, framework-supplied idempotency, retry/backoff, run inspection, manual retry/cancel, per-step tracing, local testability) is already `Active` in entflow's design and correctly scoped for v1. The state-machine cross-validation and "runs as free ops dashboard" differentiators have no direct competitor analog and should be led with in positioning. The Out-of-Scope list holds up almost entirely — with one exception (timers).

**Must have (table stakes) — all already `Active`:**
- Crash-resumable execution with atomic progress-pointer commit
- Framework-supplied deterministic idempotency keys (Activities)
- Retry with a small, fixed backoff parameter set (resist over-parameterizing, unlike Temporal's many-knobs `RetryPolicy`)
- Run rows as privacy-governed, queryable ent entities (dashboard-for-free)
- Manual retry/cancel as a permitted state transition
- Per-step OpenTelemetry spans
- Deterministic crash-simulation harness as the release gate

**Should have (differentiators):**
- State-machine cross-validation (transitions ↔ steps, bidirectional) — the flagship feature, no competitor analog
- Zero-infrastructure durability on the app's own database — DBOS is the only true peer
- Compile-time-typed step wiring (Tx vs no-Tx closure signatures) — Go-type-system answer to what Temporal solves via runtime sandboxing

**Defer (v1.x/v2+):**
- Durable timers/sleep — **reclassify from indefinite Out-of-Scope to explicit v1.x fast-follow** (cheap, additive `wake_at` column; every full-featured competitor treats this as core)
- `entflowtest` local/offline testing harness — packaging/ergonomics on top of the already-`Active` DI registry
- River backend adapter — triggered by real throughput need
- Compensation/saga rollback (`entflow.OnFail`) — correctly deferred; "inspectable + manually retryable" is a genuinely complete interim story, decoupled from every other table-stakes feature

### Architecture Approach

entflow is library-shaped, not server-shaped: every component is a Go package, a database table, or a goroutine — the database is simultaneously system of record, queue, and lock manager. The corrected entity-injection mechanism (schemast, two-pass, disk-mediated) is the central architectural fact this milestone must design around, and it means v0.5 (codegen) is not a mechanical automation pass over already-proven v0.1–v0.4 behavior — it is its own novel, higher-risk milestone that should be split into sub-phases (entity-injection mechanism spike → transitions-hook generation + cross-validation → runner/worker/observability codegen).

**Major components:**
1. Runtime library (`entflow` root, v0.1) — Flow/step builders, Attempt/idempotency-key derivation, Codec, DI registry, retry policy — zero codegen dependency, independently useful and testable
2. Worker process (`worker/`, v0.2+) — claim loop (`FOR UPDATE SKIP LOCKED`), DB-step executor (single tx), Activity three-beat executor (two small txs bracketing one untransacted external call), outbox relay (same claim-loop shape as run claiming)
3. Codegen extension (`entc/`, v0.5) — split into `entc/inject/` (schemast pre-pass writing `<Flow>Run` schema files to disk) and `entc/validate/`+`entc/templates/` (normal `entc.Extension` operating on the now-materialized schema: DAG check, transitions cross-validator, worker/hook/span codegen)
4. `meta/` package — the sole, one-directional metadata contract (`FlowMeta`) with the sibling entconnect project; enforces the "entflow imports ent only" boundary mechanically via package structure

Worker loop pattern follows River/asynq precedent (claim-execute-advance) but diverges meaningfully: a run row requires *multiple* claim-execute-advance cycles (one per step), never a long-held lease across the whole flow — each step boundary is an independent, fresh claim, which is the correct generalization of the job-queue model to entflow's multi-beat-per-row shape.

### Critical Pitfalls

1. **Zombie runs from claim-without-lease during Activities** — DB steps never produce zombies (effect+progress commit atomically), but the Activity three-beat protocol commits "attempt in progress" in beat (a) before the untransacted external call in beat (b); a crash there leaves a row that looks claimed with no lease to expire. Fix: stamp `claimed_by`/`lease_expires_at` in the claiming transaction and include lease-expiry in the claim predicate. Must land by v0.3.
2. **Entity-injection precedent was misidentified (see Executive Summary)** — budget v0.5 as a multi-phase milestone with an early standalone spike on "inject an entity whose generated transitions annotation is computed from sibling schema data," before committing to the full run-entity-injection + cross-validation scope in one phase.
3. **State-machine cross-validator false positives** — "every declared edge must be claimed by some step" must ship as a warning with per-edge suppression (e.g., `entflow.ManualTransition(...)`) from day one, never a hard error with only a coarse global disable — admin-only transitions, conditional multi-flow edges, and staged enum evolution are all legitimate unclaimed-edge cases. Changing suppression-annotation shape after users adopt it is a breaking change, so design it before the first release.
4. **Crash-simulation harness false confidence** — an in-process, cooperatively-killed harness (error returns, context cancellation, `panic`+`recover`) always unwinds through Go's normal defer/rollback path and does not exercise real SIGKILL/OOM-kill semantics. The release gate needs two explicit tiers: logical crash-point coverage (fast, in-process, bulk of the matrix) plus real subprocess-kill coverage (smaller, `exec.Command` + `SIGKILL` against a real DB-visible state, deterministic via injected delays rather than signal-race timing).
5. **SQLite has no SKIP LOCKED at all** — not degraded, absent by design (whole-database-file locking). Since ent's own quickstart defaults to SQLite, this must be an explicit, documented dialect-support matrix decided at v0.2 (Postgres-first for the worker/queue half; SQLite dev/test-only, single-writer), not discovered by a confused user filing an issue.

## Implications for Roadmap

Based on research, suggested phase structure (broadly validating and refining the design doc's own v0.1–v0.5 plan):

### Phase 1: Runtime core (Multi-style single-tx execution)
**Rationale:** Every later capability builds on the builder/execution shape; this is expressible entirely by hand (no codegen needed), proving the core API ergonomics fast.
**Delivers:** `Flow[In]` builder, step recording (name/kind/deps/transition claims), DB-step execution against `*ent.Tx`, DI registry, Codec, retry policy types.
**Addresses:** Compile-time-typed step wiring differentiator; the schema-as-constitution thesis.
**Avoids:** Anti-pattern of closure introspection — builder chain must carry every codegen-needed fact as discrete data from day one (design invariant, testable now).

### Phase 2: Durability (run-row persistence + worker poller)
**Rationale:** The whole product's foundation — every other durability claim (idempotency, manual retry, inspection) assumes the run row never lies about what happened. Must be proven before Activities/Emit exist so worker-loop bugs are isolated from activity-protocol bugs.
**Delivers:** Run-row schema (hand-written, not yet codegen-injected), `FOR UPDATE SKIP LOCKED` claim loop, atomic step-effect + progress-pointer commit, crash-resume.
**Uses:** PostgreSQL + pgx/v5, raw-SQL claim query, testcontainers-go.
**Implements:** Worker/executor loop component; the claim-execute-advance pattern with no cross-step lease.
**Avoids:** Pitfall 1 (zombie runs — though DB-only steps are safe here by construction), Pitfall 7 (multi-dialect SKIP LOCKED gap — decide and document the dialect matrix in this phase), Pitfall 9 (false-confidence harness — split into logical vs. real-kill tiers starting here, since this is where the release-gate harness is born).

### Phase 3: Activities (three-beat protocol, retries, DI)
**Rationale:** Depends on v0.2's run row (attempt counter, current-step pointer already exist); purely additive, does not touch DB-step or claim-loop code.
**Delivers:** Three-beat activity executor, framework-supplied deterministic idempotency keys, retry/backoff with exhaustion → `failed:<step>`.
**Addresses:** Framework-supplied idempotency-key table-stakes feature; at-least-once-execution contract.
**Avoids:** Pitfall 1 (lease/heartbeat must land here, since this is where the split-transaction zombie gap actually lives), Pitfall 3 (idempotency-key TTL/provider-window mismatch — document the guarantee's actual scope and consider a stale-attempt warning).

### Phase 4: Outbox / relay
**Rationale:** Reuses v0.2's claim-loop shape wholesale — architecturally a sibling of the worker, not a dependent of v0.3.
**Delivers:** Outbox table, relay loop (poll → claim → deliver → mark), NATS JetStream as first-class pluggable target, flow chaining.
**Uses:** Same `SKIP LOCKED` claim primitive; `nats.go` in a separate satellite module, never a core dependency.
**Avoids:** Pitfall 2's relay-specific form — claim/deliver/mark must be three separate short transactions, never one "claim + deliver to NATS + mark" transaction wrapping the network call.

### Phase 5a: Codegen — entity injection mechanism spike
**Rationale:** This is the single highest-risk technical unknown in the whole roadmap (corrected precedent: schemast two-pass, not entproto-style graph mutation) and should be proven standalone before committing to the full codegen scope.
**Delivers:** A working schemast-based pre-pass that injects a `<Flow>Run` schema file whose `state` transitions annotation is derived from the flow's own step graph; a validated approach (AST-parsing vs. compiled-reflection) for reading `Flows()` builder data on a from-scratch project where the generated package doesn't exist yet.
**Avoids:** Pitfall 4 (two-pass bootstrap chicken-and-egg) — the from-scratch "empty project → first `go generate` with `Flows()` already present" scenario must be an explicit, CI-tested quickstart, not discovered by the first external user.

### Phase 5b: Codegen — transitions-hook generation + cross-validation
**Rationale:** Depends on 5a's injection mechanism existing; this is the flagship differentiator and must ship with the warning/suppression design already correct, since changing it later breaks users.
**Delivers:** Generated transitions-enforcing hook (no `SkipHook` escape), bidirectional state-machine cross-validator (claim→map is a hard error; map→claim is a warning with per-edge suppression), `DenyStatusEscalation` privacy rule.
**Addresses:** State-machine cross-validation differentiator (flagship feature).
**Avoids:** Pitfall 5 (false positives train users to disable the whole check) — ship warning-by-default with fine-grained suppression annotations from the first release.

### Phase 5c: Codegen — runner/worker/observability codegen + `Result[T]` validation
**Rationale:** The remaining automation over already-proven v0.1–v0.4 behavior; lower risk than 5a/5b since it's generating code whose hand-written equivalent already works.
**Delivers:** Generated worker dispatch, OTel span wiring, `Describe()` output, codegen-time validation of `Result[T](ctx, "step")` string references (step exists, type matches).
**Avoids:** Pitfall 6 (`Result[T]` stringly-typed decay into runtime panics) — this is the point where the escape hatch gets a compile-time-equivalent safety net.

### Phase Ordering Rationale

- Runtime-first, codegen-last is validated by research: ent's own `Hooks()`/`Policy()` precedent proves the closures-in-schema-methods pattern works without codegen, so codegen is additive automation, never a load-bearing dependency of the core value proposition.
- Each phase strictly depends only on its predecessors, never forward (v0.4 outbox is a sibling of v0.3 activities, not a dependent).
- The crash-simulation harness is introduced at Phase 2 and extended at Phase 3 (activity beats) and Phase 4 (relay delivery) rather than built once at the end — it's a living, growing release-gate artifact, not a single v0.5-adjacent deliverable.
- Codegen (v0.5) is deliberately split into three sub-phases rather than treated as one milestone, because the entity-injection mechanism is a genuinely novel, unproven problem (harder than any published precedent) that deserves isolation from the comparatively lower-risk template/validation work around it.
- **Durable timers/sleep is not in this phase list** — per the FEATURES.md/PITFALLS.md convergent recommendation, reclassify it as the first v1.x fast-follow after the phases above ship, not a phase in the initial roadmap; it is additive (a `wake_at` column + claim-query filter) and does not require redesigning any of the phases above if deferred correctly.

### Research Flags

Needs deeper research during planning:
- **Phase 5a (entity injection spike):** Highest-risk unknown in the roadmap — the corrected schemast-based mechanism has no precedent at entflow's required fidelity (self-referential transitions annotation derived from the flow's own step graph). Spend a `--research-phase` pass here specifically on AST-vs-reflection extraction and the nested two-pass bootstrap.
- **Phase 5b (cross-validation):** The warning/suppression annotation design is a one-way door (breaking change to alter after adoption) — worth a focused design pass before implementation, informed by Pitfall 5's enumerated false-positive cases.
- **Phase 3 (Activities/lease):** The lease/heartbeat mechanism is unspecified in the source design doc — needs its own small design spike (lease duration, reclaim predicate, interaction with the claim query) before implementation.

Phases with standard, well-documented patterns (skip deep research):
- **Phase 1 (runtime core):** Directly modeled on Ecto.Multi and ent's own Hooks()/Policy() pattern — established precedent, low risk.
- **Phase 2 (worker poller):** `FOR UPDATE SKIP LOCKED` claim-loop is the standard, well-documented Postgres-queue pattern (River, Oban, Solid Queue all converge on the same shape).
- **Phase 4 (outbox):** Transactional outbox is a small, standard, widely-documented pattern; no library gap to fill, just careful transaction-boundary discipline (see Pitfall 2).

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | MEDIUM | Versions cross-checked against pkg.go.dev/GitHub directly; the schemast-vs-entproto correction is HIGH confidence (read from source, cross-checked against entflow's own claim) |
| Features | MEDIUM | Category-level convergence (table stakes, timers-as-core) is high-confidence given independent agreement across many competitors; individual product-feature claims are LOW-confidence websearch snapshots and should be spot-checked before external positioning copy |
| Architecture | MEDIUM | Design-doc self-consistency is HIGH; ent extension-API mechanics are MEDIUM (corroborated across multiple sources plus one direct source read of enthistory); Temporal/DBOS/River comparisons are MEDIUM (standard published architecture) |
| Pitfalls | MEDIUM-HIGH | General distributed-systems/Go/Postgres patterns are MEDIUM (cross-checked web research); ent-specific and product-specific pitfalls are HIGH (derived directly from entflow's own documented architecture, not external sourcing) |

**Overall confidence:** MEDIUM

### Gaps to Address

- **Activity lease/heartbeat mechanism** is not specified anywhere in the source design doc — must be designed during Phase 3 planning, not assumed to fall out of existing decisions.
- **AST-extractability of the `Flows()` builder chain** is currently a design *intent*, not a proven mechanism — needs an early (ideally v0.1-adjacent) spike/unit test asserting builder data-producing methods never require evaluating the closure, before Phase 5a depends on it.
- **Dialect support matrix** (Postgres-first, MySQL-secondary, SQLite dev/test-only) is a research recommendation, not yet a committed project decision — should be explicitly ratified during Phase 2 planning and documented before the poller's claim query is finalized.
- **Crash-simulation harness tiering** (logical vs. real-process-kill) needs to be an explicit, documented split in the release-gate definition — currently the design doc's "no public release without the harness passing" constraint doesn't specify which tier(s) that means.
- **Ordinary (non-crash-injection) unit-level flow testing ergonomics** is under-specified relative to the crash-simulation harness — flagged by FEATURES.md as a v1.x gap needing its own `entflowtest`-style packaging pass, distinct from the release-gate harness.
- All external competitor feature claims in FEATURES.md are LOW-confidence websearch digests and should be verified against official docs before being used in any external-facing marketing/positioning copy.

## Sources

### Primary (HIGH confidence)
- `entgo.io/contrib/schemast` package docs, `ent/contrib/entproto/extension.go`, `ent/contrib/entgql/extension.go` (source read directly) — corrected the entity-injection precedent
- `enthistory` repository and `extension.go` (source read directly) — verified two-pass, disk-mediated injection pattern
- `/Users/smintz/go/src/github.com/smintz/entflow/entflow.md` and `.planning/PROJECT.md` — project's own design/requirements, ground truth for intent

### Secondary (MEDIUM confidence)
- pkg.go.dev direct fetches for all recommended package versions (ent, river, otel, nats.go, testcontainers-go, contrib)
- ent official docs (Extensions, Hooks, entc, schemast/generating-ent-schemas)
- DBOS, River, NATS JetStream, Temporal official docs/blogs — architecture and dialect-support claims
- Postgres SKIP LOCKED / SQLite locking-model divergence — cross-checked across 2+ independent sources (bigbinary.com, Vlad Mihalcea, SQLAlchemy mailing list)

### Tertiary (LOW confidence)
- All competitor-feature comparison claims in FEATURES.md (Temporal, DBOS, Windmill, Inngest, Conductor, Step Functions, Ecto, Laravel, Celery, Eventuate Tram, Axon) — websearch digests, not verified against current official docs; consistent cross-competitor convergence on table-stakes features raises practical confidence at the category level even though individual product claims are unverified

---
*Research completed: 2026-08-08*
*Ready for roadmap: yes*
