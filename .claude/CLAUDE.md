<!-- GSD:project-start source:PROJECT.md -->

## Project

**entflow**

entflow is an [ent](https://entgo.io) extension that makes durable workflows a schema concern. Workflows are declared inside ent schema files via a `Flows()` method — a peer of `Fields()`, `Hooks()`, and `Policy()` — with step bodies written as typed closures inline in the declaration. The extension injects a persisted "run" entity per flow, generates a worker that executes runs with crash-resume semantics, and cross-validates workflow steps against the entity's declared state machine at codegen time.

Positioning: **Temporal-lite for ent** — durable, resumable, exactly-once-outcome workflows with no additional infrastructure beyond the database ent already uses. It is for Go teams building ent-backed applications who need multi-step business processes (sagas, order lifecycles, payment flows) without adopting a separate workflow engine.

**Core Value:** A multi-step business process declared in the ent schema executes durably — surviving crashes at any point without duplicating external effects or committing a step without its progress record.

### Constraints

- **Dependencies**: entflow depends on `ent` only — never imports protobuf, Connect, HTTP, or descriptor machinery. The decoupling contract with entconnect is non-negotiable.
- **Infrastructure**: No infrastructure beyond the database ent already uses. The database is the queue; no coordination service, no broker required for correctness.
- **Tech stack**: Go, ent, entc extension API (sanctioned extension surface only — modeled on entgql/entproto).
- **Input types**: `In` must satisfy `entflow.Codec[In]` but must never be required to be a `proto.Message` — inputs originate from RPC, cron, CLI, tests, or another flow's outbox event.
- **Type safety**: Activity closures must not receive `*ent.Tx` — structural, not conventional, separation of external calls from DB state.
- **Release gate**: No public release without the deterministic crash-simulation harness passing. Trust in a workflow engine is won or lost on crash-resume.
- **Build order**: Layers must be independently shippable — the runtime library must work (with ugly ergonomics, reflection-based) before codegen exists.

<!-- GSD:project-end -->

<!-- GSD:stack-start source:research/STACK.md -->

## Technology Stack

## Recommended Stack

### Core Framework

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| Go | 1.25+ (toolchain 1.26 released Feb 2026) | Language runtime | Generics are load-bearing for entflow's `entflow.New[In]`, `entflow.Use[T]`, `entflow.Codec[In]`, `entflow.Result[T]` API. Go 1.22+ is the practical floor for clean generic ergonomics; target 1.25 as the `go.mod` `go` directive to stay one version behind bleeding edge for contrib-library compatibility (mirrors ent/contrib's own conservatism). Confidence: MEDIUM. |
| entgo.io/ent | v0.14.6 (latest, Mar 2026) | ORM + schema-as-code + entc codegen pipeline entflow extends | This *is* the platform entflow builds on — not a choice, a given. Still pre-1.0 (no v1.0.0 tag exists), but the `entc.Extension`, `gen.Hook`, `gen.Graph`, `gen.Type`/`gen.Field` surface has been stable across the 0.11–0.14 line (edge schemas, the extension API, and the gen.Graph hook pipeline all predate 0.14). Treat this API as "stable in practice, not stable in promise" — pin the exact version in `go.mod` and re-verify on every ent minor bump. Confidence: MEDIUM (version verified via pkg.go.dev; stability claim is priced into entflow's own design doc as a named risk). |
| entgo.io/contrib/schemast | pinned to whatever version matches your `entgo.io/contrib` pin (module has no independent v1.0) | AST-based pre-codegen schema injection — the mechanism for injecting the per-flow run entity (`CancelOrderFlowRun`) | This is the load-bearing discovery of this research pass. `entc.Extension.Hooks()` runs `gen.Hook`s **after** the schema graph is already loaded from `schema/*.go` — hooks can *mutate* `gen.Graph` nodes (entgql does this, e.g. adding GraphQL directives to existing fields) but there is no supported way to hand the standard `entc` generator a brand-new `gen.Type` that didn't originate from a schema file on disk. `schemast` solves this by manipulating the Go AST of the `schema/` package directly — `schemast.Load(dir)` → `schemast.Mutate(ctx, &schemast.UpsertSchema{...})` → `ctx.Print(dir)` — writing a real `.go` schema file *before* `go generate ./ent` invokes the standard entc pipeline. This must run as a **separate pre-step** (its own `go:generate` line or a wrapping CLI), not inside `entc.Extension.Hooks()`, because by the time hooks run, ent has already parsed the schema package. Confidence: HIGH (verified directly against `entgo.io/contrib/schemast` package docs and cross-checked against entproto's actual `Hooks()`-only implementation, which does NOT inject entities — it only emits `.proto` files as a side effect). |

### Database / Queue Layer

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| PostgreSQL | 12+ (target 14+) | Primary supported database for v1 | `FOR UPDATE ... SKIP LOCKED` has been in Postgres since 9.5 (2016) — rock solid, this is the reference implementation every queue-on-Postgres library (River, gue, Solid Queue, good_job) builds on. Treat Postgres as the only database where entflow's worker-poller claim query is a first-class citizen. |
| `github.com/jackc/pgx/v5` (stdlib mode) | v5.x latest | Postgres driver, used *through* `database/sql` via `pgx/v5/stdlib` | ent does not have a native non-`database/sql` driver path — it consumes any `*sql.DB` via `entsql.OpenDB(dialect.Postgres, db)`. The idiomatic 2026 pattern is `sql.Open("pgx", dsn)` after `stdlib.GetDefaultDriver()` registration, which gets you pgx's connection-level performance (binary protocol, prepared statement caching) while staying inside ent's `database/sql` abstraction. Do **not** hand-roll a raw `pgxpool.Pool` integration with ent core — it isn't supported without dropping to raw SQL for the queue-claim query, which entflow needs anyway (see below). Confidence: MEDIUM. |
| Hand-written poller: `SELECT ... FOR UPDATE SKIP LOCKED` via raw SQL (not ent's query builder) | n/a (entflow-authored) | The worker's claim query — "the database is the queue" | Ent's fluent query builder cannot express `FOR UPDATE SKIP LOCKED` (no builder method for it as of v0.14). The claim query must be raw SQL executed through the same `*sql.DB`/`*sql.Tx` ent is using, e.g. `tx.QueryContext(ctx, "SELECT id FROM cancel_order_flow_runs WHERE state = ANY($1) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $2", ...)`, followed by loading/mutating those rows through the generated ent client inside the same transaction. This is the same trick every Postgres-queue library uses under an ORM — Ecto's `Oban`, Rails' Solid Queue, and River all drop to raw SQL for the claim statement even though they otherwise use a query builder. Confidence: HIGH (verified against River's own architecture and general Postgres queue literature). |
| `github.com/riverqueue/river` | v0.43.0 (latest, Aug 2026) | **Optional** pluggable worker backend, per the design doc's own open question #4/§3's "should not compete with River" | River is Postgres+pgx-v5-only (`riverpgxv5` driver; a `database/sql`-compatible `riverdatabasesql` driver exists for the OSS line too, but MySQL/SQLite are never supported — River made the same Postgres-only bet entflow should make). It already solved: SKIP LOCKED claiming, generic `river.Worker[T]`/`JobArgs` typed jobs, retry/backoff, periodic jobs, OTel metrics hooks (v0.41+), and a Pro tier with more. For entflow, the adapter boundary (open question #4) should be: entflow's run-claim step is the thing River can replace — River claims a "run advance" job, entflow's own runner logic still owns the transactional step-execution semantics (River doesn't know about run rows or the three-beat activity protocol; it just needs to reliably invoke "advance run X"). Do not attempt to model *steps* as individual River jobs — the atomicity contract (step effect + progress pointer commit together) is entflow's own invariant, not something River's job model expresses. Confidence: MEDIUM — the adapter design itself is unresearched/unbuilt (correctly flagged as an open question in PROJECT.md), but River's capabilities and constraints are verified. |
| `github.com/vgarvardt/gue` | v5.x | Alternative Postgres queue (NOT recommended as default) | Smaller, simpler, older lineage (fork of `bgentry/que-go`). Lower activity/mindshare than River, no typed-job generics, no built-in OTel. Only reason to reach for it: if you want a thinner dependency than River and don't need River Pro-adjacent features. Not recommended as entflow's optional backend — River has more momentum and a documented plugin-friendly driver interface. |
| MySQL 8.0+ | — | Secondary dialect ent supports; SKIP LOCKED works but is second-class for entflow's queue | MySQL 8.0+ (InnoDB) added `SKIP LOCKED`/`NOWAIT` on `SELECT ... FOR UPDATE`, so the claim query is portable in principle. In practice: no MySQL equivalent of pgx's `stdlib` ergonomics, weaker `RETURNING` support historically (MySQL 8.0.21+ finally supports `INSERT ... RETURNING`-style patterns are still limited), and no River-equivalent queue library to fall back on if the hand-rolled poller needs hardening later. Recommendation: support MySQL for the ORM/schema half of ent (fields, hooks, privacy) but explicitly document the worker/queue half as **Postgres-first**, MySQL-compatible-but-untested-at-scale. |
| SQLite | — | Third dialect ent supports; **`SKIP LOCKED` does not exist in SQLite at all** | This is a hard incompatibility, not a degraded-performance one. SQLite has no row-level locking — a writer transaction locks the whole database file. There is no `FOR UPDATE`, no `SKIP LOCKED`. For SQLite, entflow's worker poller must fall back to a different claim strategy (e.g., a single-writer assumption, or `UPDATE ... WHERE state = 'pending' LIMIT 1 RETURNING *` executed as an atomic single statement relying on SQLite's whole-DB write serialization to prevent double-claims). This works correctly for the *single-process, single-worker* dev/test topology SQLite is realistically used for with ent (in-memory test suites), but must never be presented as a production multi-worker option. Document this loudly — it is the kind of trap a contributor will hit once and file a confused issue about. |

### Outbox / Messaging

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| Hand-written outbox table + relay (poll → claim → deliver → mark), same poller mechanics as the run worker | n/a (entflow-authored) | Transactional outbox for `Emit` steps | There is no dominant standalone Go outbox *library* worth depending on (the pattern is small enough — one table, one poll loop — that every serious implementation hand-rolls it; this matches entflow's own design doc, which explicitly folds the outbox relay into "mechanically the same loop as the run worker"). Do not add a dependency here; reuse the same `SELECT ... FOR UPDATE SKIP LOCKED` claim primitive already built for run claiming. Confidence: HIGH (this is architectural, not a library gap). |
| `github.com/nats-io/nats.go` | v1.52.0 (latest, May 2026) | NATS/JetStream Go client — first-class relay delivery target | Official, actively released client. For dedup, JetStream's `Nats-Msg-Id` header gives **publish-side** exactly-once-within-a-window (default 2 min, tunable via `--dupe-window` at stream creation) — set `Nats-Msg-Id` to the outbox row's idempotency key (e.g. `runID:emitStep:seq`) on publish and a retried relay delivery is deduplicated server-side. This is *not* end-to-end exactly-once delivery to consumers — JetStream still gives at-least-once delivery to subscribers, so downstream consumers of emitted events must be idempotent on their own, same as entflow's own Activity protocol philosophy. Frame this consistently in docs: "the relay guarantees at-least-once publish with idempotent-key dedup at the broker; your subscriber still needs to be idempotent." Confidence: MEDIUM (version and dedup-window verified via NATS docs/blog, cross-checked across two independent sources). |
| Pluggable relay target interface (entflow-authored `RelayTarget` interface) | n/a | Abstraction so NATS is one implementation, not a hard dependency | Mirrors the River adapter-boundary principle: entflow core must not import `nats.go` in its main module (constraint: "entflow depends on `ent` only"). Ship the NATS target as a separate Go module or build-tagged subpackage (`entflow/relay/nats`), same pattern ent/contrib uses for entgql/entproto living alongside — but as an independently-versioned satellite module, not bundled into entflow's own `go.mod`, to honor the "entflow imports ent only" constraint literally. |

### Observability

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `go.opentelemetry.io/otel` | v1.45.0 (latest stable, Aug 2026) | Tracing API/SDK for per-run and per-step spans | This is the de facto standard for Go instrumentation in 2026 — no credible alternative. Use the **API** package (`go.opentelemetry.io/otel/trace`) in entflow's runtime/worker code and let the *application* wire up the SDK/exporter; a library should never force an exporter choice on its consumers. |
| `go.opentelemetry.io/otel/sdk` | matching v1.45.0 | SDK, only in entflow's own test/example code, never in the library's non-test import graph | Same "library vs app" boundary as above — keep `otel/sdk` out of entflow's production dependency tree; only the API package should be an unconditional dependency. |
| Span naming: `workflow.<Flow>.<step>` (as already specified in the design doc) | — | Span-naming convention for run/step spans | This already matches OTel's general guidance (low-cardinality, hierarchical, verb/object-shaped names — IDs belong in attributes, not the span name). Keep run IDs, attempt counts, and error text as **span attributes** (`entflow.run_id`, `entflow.attempt`, `entflow.step`, `entflow.state`), never interpolated into the span name itself — interpolating IDs into names blows up cardinality in every tracing backend. One span per run (root), one child span per step, matching the design doc's §6. Confidence: MEDIUM (semantic-convention guidance verified; entflow's own naming scheme approved by inspection, not measured against a running system yet). |

### Supporting Libraries

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `text/template` (stdlib) | stdlib | Codegen templates for the entc extension (worker code, transitions hook, `Describe()` output) | This is what `entc/gen` itself uses (`gen.Template` wraps `text/template`), and what entgql/entproto both use for all of their generated-code emission. Following the grain of the ecosystem here isn't just convention — entc's own template-loading/extension machinery (`gen.NewTemplate`, `Templates()` on `entc.Extension`) is `text/template`-shaped; fighting that with a different codegen approach means reimplementing template registration, funcmaps, and override/patch semantics that entc already gives you. |
| `github.com/dave/jennifer` (`jen` package) | v1.7.x (last tagged ~2020, low recent commit activity) | AST-first Go code generation, as an *alternative* to `text/template` for entflow's own internal tooling if template string-fiddling gets unwieldy | Not recommended as entflow's primary codegen mechanism — it would put entflow out of step with the rest of the ent ecosystem (nobody writing an entc extension uses it; entc's own extension hooks assume `text/template`), and its low maintenance velocity (essentially feature-complete/dormant, not actively broken) is an acceptable risk only for isolated internal use, not for the public codegen surface. If entflow ever needs to synthesize genuinely complex Go ASTs (e.g., programmatically building step-dispatch switch statements with many branches) where template string-concatenation becomes error-prone, reach for it as a scoped internal helper, not a replacement for the `gen.Template` pipeline. |
| `github.com/testcontainers/testcontainers-go` + `.../modules/postgres` | v0.44.0 (latest, Aug 2026) | Real-Postgres crash-simulation harness (the release-gate requirement in the design doc §7) | Actively maintained (weekly-ish releases), has a purpose-built Postgres module with wait-strategies and snapshot/restore support, and is the community's converged default for Go integration testing against real databases in 2026 — displacing `ory/dockertest` as the recommended choice even though dockertest v4 added container reuse. Testcontainers' per-test-package container lifecycle (`TestMain` + `t.Cleanup`) fits entflow's crash-simulation harness well: spin up one Postgres container, run migrations once, then create/drop a fresh schema or database per test case for isolation. |
| `github.com/ory/dockertest/v3` | v3.x | Alternative to testcontainers-go | Only reach for this if testcontainers-go's Docker API version requirements or its heavier dependency footprint become a problem in your CI image; otherwise testcontainers-go is the better default in 2026. |
| Embedded Postgres (e.g. `github.com/fergusstrange/embedded-postgres`) | — | NOT recommended for the crash-simulation harness | Embedded/in-process Postgres binaries diverge from your real deployment target (extension availability, exact version, connection semantics under `SKIP LOCKED` contention) and the crash-simulation harness specifically needs to exercise real crash/kill semantics against a real server process — a containerized real Postgres (testcontainers) is strictly more faithful for the one test suite in this project where faithfulness is the entire point. |
| `github.com/stripe/stripe-go/v82` (or whatever `/vNN` is current at implementation time — v86 observed during this research pass) | v82+ (Stripe ships a new major ~monthly, tracking their API version pins) | Reference implementation for the idempotency-key pattern entflow's Activity protocol is modeled on | Stripe's Go SDK passes idempotency via `stripe.Params.IdempotencyKey` (a field on the shared `Params` struct embedded in every request-params type, e.g. `RefundParams.IdempotencyKey`), which the design doc's own example (`att.IdempotencyKey()`) already mirrors correctly. Important nuance to document for entflow users: **Stripe's idempotency guarantee has its own window (24 hours) and per-account key scope** — entflow's deterministic key (`runID:stepName:attempt`) is a good match for this pattern precisely because it's stable across retries of the *same* logical attempt, but a new `attempt` number is a *new* idempotency key, which is correct (a fresh attempt after exhausting the provider's retry semantics should be allowed to create a new effect only if the framework intentionally advanced the attempt counter, not on transient re-delivery). Confidence: MEDIUM on exact current major version (Stripe ships majors too fast for a single version number to stay "current" for long — always resolve at implementation time with `go list -m -versions github.com/stripe/stripe-go/v82` or check `github.com/stripe/stripe-go/releases`), HIGH on the `Params.IdempotencyKey` mechanism itself. |
| `github.com/stretchr/testify` (`require`/`assert`) | v1.10.x | Test assertions across unit/property/golden-file tests | Ubiquitous, already assumed by ent's own test suite conventions; no reason to deviate. |
| `pgregory.net/rapid` or `testing/quick`-successor property-based testing | `rapid` v1.x | Property tests on the transitions cross-validator (design doc §7) | `rapid` is the actively maintained property-testing library for Go in 2026 (stdlib's `testing/quick` has been effectively frozen/deprecated in spirit for years, no generics support, awkward shrinking). Use it to generate random transition maps / step-claim graphs and assert the DAG + transition cross-validator either correctly accepts or correctly rejects. |

## Alternatives Considered

| Category | Recommended | Alternative | Why Not |
|----------|-------------|-------------|---------|
| Queue backend | Hand-rolled `SKIP LOCKED` poller (default) + River (optional, pluggable) | Make River the *only* backend | Violates the design doc's own "no infrastructure beyond the database ent already uses" and "database is the queue" pillars for the zero-dependency default; River should stay strictly optional per the design doc's stated goal to not compete with it. |
| Queue backend | River (when opting in) | `vgarvardt/gue` | Less active, no generics-based typed jobs, no built-in OTel hooks, smaller community — River is the better-maintained, more feature-complete Postgres queue if you're going to depend on one at all. |
| Codegen mechanism | `text/template` (via `entc/gen.Template`) | `dave/jennifer` (AST-based) | Diverges from how entc/entgql/entproto structure their generated-code pipeline; would require entflow to build its own template-registration/override system in parallel to the one `entc.Extension.Templates()` already provides. |
| Codegen mechanism | `text/template` | Hand-rolled `go/ast` + `go/printer` construction | Far more verbose for the amount of generated code entflow needs (worker dispatch, transitions hook, `Describe()`); `go/ast` is the right tool only when you need to *parse and rewrite existing* Go source (which is exactly what `schemast` is for, and why entflow should delegate to it rather than reinvent it for schema injection). |
| Postgres driver | `pgx/v5` via `stdlib` (through `database/sql`, as ent requires) | Native `pgxpool.Pool` bypass of `database/sql` | Ent core does not support a non-`database/sql` driver path; you'd lose ent's transaction management, hooks, and privacy pipeline for any code going through the raw pool, which defeats the entire "everything flows through the tx client" design guarantee. |
| Integration test harness | `testcontainers-go` | `ory/dockertest` | Community momentum and a purpose-built Postgres module have shifted the 2026 default to testcontainers-go; dockertest remains fine but is the second choice. |
| Messaging | NATS JetStream (first-class target) | Kafka (via `segmentio/kafka-go` or `confluent-kafka-go`) | Explicitly out of scope for entflow's "no additional infrastructure" pitch — Kafka is a heavier operational commitment than the zero-infra positioning wants for a default; leave it as a community-contributable relay target behind the same `RelayTarget` interface, not a first-class dependency. |
| Idempotency SDK reference | Stripe Go SDK | Twilio / PayPal Go SDKs | Stripe's `IdempotencyKey` pattern is the most widely copied and cleanly documented in the industry; use it as the canonical worked example in entflow's own docs even if a given user's actual provider differs — the pattern (deterministic key on `Params`, provider replays the original effect) generalizes. |

## What NOT to Use

| Avoid | Why | Use Instead |
|-------|-----|-------------|
| Building entity injection inside `entc.Extension.Hooks()` alone (mutating `gen.Graph` to add a wholly new `gen.Type`) | `gen.Hook`s run against an already-loaded graph parsed from `schema/*.go` files on disk; there's no supported path to hand the standard generator pipeline a synthetic `gen.Type` that never existed as a schema file, and hand-patching `gen.Graph` internals to fake one is fragile, undocumented, and will break silently across ent point releases. | `entgo.io/contrib/schemast` as a **separate pre-generation step** that writes a real `schema/*_run.go` file, then let the standard entc pipeline (plus entflow's own `Hooks()`/`Templates()` for the transitions/worker/span codegen) run normally against it. |
| `testing/quick` (stdlib) for property tests | Effectively unmaintained in spirit — no generics support, weak shrinking, hasn't kept pace with how Go property testing has evolved. | `pgregory.net/rapid`. |
| Embedded/in-process Postgres binaries for the crash-simulation harness | Diverges from real server crash semantics and connection-level behavior under lock contention — exactly the fidelity the release-gate test needs most. | `testcontainers-go` with the real `postgres` Docker image. |
| SQLite as a production target for the worker/queue half of entflow | No `SKIP LOCKED`, no real row-level locking — the "database is the queue" multi-worker claim story silently degrades to single-writer semantics. Silent because SQLite doesn't error on `FOR UPDATE` syntax in all drivers' surrounding SQL — it can either error or be a silent no-op depending on the driver, which is worse than a clean failure. | Postgres for any deployment with more than one worker process/goroutine pool claiming runs; document SQLite as dev/test-only for the worker half explicitly. |
| Treating NATS JetStream `Nats-Msg-Id` dedup as end-to-end exactly-once | It only dedups **publishes** within a bounded window (default 2 min) at the broker; subscribers still get at-least-once delivery. Marketing copy that says "exactly-once" without this caveat sets users up to build non-idempotent consumers and get bitten. | Document it as "effectively-once": at-least-once delivery + idempotent-key dedup at publish + idempotent consumers, the same philosophy already baked into entflow's own Activity protocol — be consistent. |
| Pinning entflow's own `go.mod` to `require entgo.io/ent latest` floating semantics or a pre-release commit hash | Ent's extension API (`entc.Extension`, `gen.Graph` internals) is explicitly called out in the design doc as "stable but under-documented" — floating on unreleased ent commits multiplies the surface area for breakage from upstream refactors that aren't yet reflected in any changelog. | Pin an exact tagged `entgo.io/ent` version, bump deliberately, and treat every ent minor bump as requiring a regression pass against entflow's own codegen output (the golden-file / generated-code-diff CI pattern ent itself recommends for consumers of its codegen). |
| A hard dependency on `nats.go` (or any relay target) inside entflow's core module | Violates the explicit constraint: "entflow depends on `ent` only — never imports protobuf, Connect, HTTP, or descriptor machinery" (and by extension, should not force a messaging-client dependency on users who only want the built-in poller/outbox table). | Ship NATS (and any other relay target) as a separate satellite Go module (`entflow/relay/nats` or a sibling repo), matching how `ariga.io/entcache` is versioned independently from `entgo.io/contrib` rather than folded into ent core. |

## Stack Patterns by Variant

- Use the hand-rolled `SKIP LOCKED` poller as the zero-dependency default worker backend.
- Offer River as an explicit opt-in behind an adapter interface, never as a required dependency.
- Treat MySQL/SQLite support as "ent's ORM half works; the worker/queue half is Postgres-first" and say so in docs — don't silently let MySQL/SQLite users hit `SKIP LOCKED` failures with no warning.
- The claim query still works syntactically on MySQL 8.0+, but ship this path with an explicit "less battle-tested" label and lean on the crash-simulation harness (parameterize it to run against both Postgres and MySQL containers in CI) before calling it supported, not just "happens to work."
- Force single-worker semantics explicitly — either refuse to start a second worker goroutine against a SQLite-backed client, or document that concurrent claim attempts serialize (correctly, just not concurrently) because of SQLite's whole-database write lock. Don't pretend SKIP LOCKED semantics exist there.
- Use `schemast` as a pre-`go generate` step (a small CLI or `go:generate` line the user runs once, or that entflow's own `entc.Extension` triggers via a documented two-pass bootstrap — matching the design doc's own "standard two-pass flow ent users already know from hooks").
- Keep the transitions-hook generation, span wiring, and `Describe()` output as normal `entc.Extension.Templates()`/`Hooks()` work, since those only need to *read* the already-loaded graph, not inject new types into it.

## Version Compatibility

| Package A | Compatible With | Notes |
|-----------|------------------|-------|
| `entgo.io/ent` v0.14.6 | `entgo.io/contrib` v0.7.0 (schemast, entgql, entproto) | contrib trails ent core releases; always check contrib's `go.mod` `require entgo.io/ent` line for the exact pinned version before assuming a newer ent works with a given contrib release. |
| `github.com/riverqueue/river` v0.43.0 | `github.com/jackc/pgx/v5` (via `riverpgxv5`) | River v0.4x line requires pgx v5 specifically (not v4); if entflow's core already standardizes on `pgx/v5/stdlib` for the `database/sql` connection, the River-as-optional-backend integration can share the same underlying pgx major version without conflict. |
| `go.opentelemetry.io/otel` v1.45.0 | `go.opentelemetry.io/otel/sdk`, `.../exporters/*` | Keep all `go.opentelemetry.io/otel*` submodules on the same release line/version — mismatched otel submodule versions are a very common source of confusing build/runtime errors in the Go OTel ecosystem. |
| `github.com/testcontainers/testcontainers-go` v0.44.0 | `.../modules/postgres` (same repo, versioned together) | Always bump the `modules/postgres` submodule in lockstep with the core `testcontainers-go` version — they're released from the same monorepo and drift causes API mismatches. |

## Sources

- pkg.go.dev direct fetch — `entgo.io/ent` (v0.14.6, Mar 2026), `github.com/riverqueue/river` (v0.43.0, Aug 2026), `go.opentelemetry.io/otel` (v1.45.0, Aug 2026), `github.com/nats-io/nats.go` (v1.52.0, May 2026), `github.com/testcontainers/testcontainers-go` (v0.44.0, Aug 2026), `entgo.io/contrib` (v0.7.0, Mar 2025) — confidence MEDIUM (primary source, but WebFetch-summarized rather than raw-read)
- GitHub source read — `ent/contrib/entproto/extension.go` (Extension struct, Hooks()-only implementation, no schema injection), `ent/contrib/entgql/extension.go` (Templates()/Hooks()/Options() implementation, gen.Graph node mutation pattern), `entgo.io/contrib/schemast` package docs (Load/Mutate/UpsertSchema/Print API) — confidence HIGH (read from source/official docs directly, cross-checked against entflow's own design-doc claim of "entproto schema injection precedent")
- WebSearch — River driver support (pgx v5 only, riverdatabasesql for compat), MySQL 8.0 SKIP LOCKED support, SQLite's total lack of SKIP LOCKED/FOR UPDATE, NATS JetStream Nats-Msg-Id dedup window semantics, testcontainers-go vs dockertest 2026 consensus, OpenTelemetry span-naming semantic conventions, Stripe Go SDK IdempotencyKey pattern, dave/jennifer maintenance status, ariga.io/entcache as a standalone module separate from entgo.io/contrib — confidence MEDIUM (cross-checked across 2+ independent search results per claim) to LOW (single-source claims called out inline, e.g. exact current Stripe major version, which moves too fast to pin reliably)
- Design document self-report (`/Users/smintz/go/src/github.com/smintz/entflow/entflow.md`, `.planning/PROJECT.md`) — used for constraint verification (e.g. "entflow depends on ent only," "River should be an optional backend," span-naming convention already specified) — treated as ground truth for project intent, not an external source

<!-- GSD:stack-end -->

<!-- GSD:conventions-start source:CONVENTIONS.md -->

## Conventions

Conventions not yet established. Will populate as patterns emerge during development.
<!-- GSD:conventions-end -->

<!-- GSD:architecture-start source:ARCHITECTURE.md -->

## Architecture

Architecture not yet mapped. Follow existing patterns found in the codebase.
<!-- GSD:architecture-end -->

<!-- GSD:skills-start source:skills/ -->

## Project Skills

No project skills found. Add skills to any of: `.claude/skills/`, `.agents/skills/`, `.cursor/skills/`, `.github/skills/`, or `.codex/skills/` with a `SKILL.md` index file.
<!-- GSD:skills-end -->

<!-- GSD:workflow-start source:GSD defaults -->

## GSD Workflow Enforcement

Before using Edit, Write, or other file-changing tools, start work through a GSD command so planning artifacts and execution context stay in sync.

Use these entry points:

- `/gsd-quick` for small fixes, doc updates, and ad-hoc tasks
- `/gsd-debug` for investigation and bug fixing
- `/gsd-execute-phase` for planned phase work

Do not make direct repo edits outside a GSD workflow unless the user explicitly asks to bypass it.
<!-- GSD:workflow-end -->

<!-- GSD:profile-start -->

## Developer Profile

> Profile not yet configured. Run `/gsd-profile-user` to generate your developer profile.
> This section is managed by `generate-claude-profile` -- do not edit manually.
<!-- GSD:profile-end -->
