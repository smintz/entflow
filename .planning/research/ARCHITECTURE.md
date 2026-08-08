# Architecture Research

**Domain:** Library-shaped durable workflow engine (Go, ent schema extension + entc codegen)
**Researched:** 2026-08-08
**Confidence:** MEDIUM (design-doc self-consistency HIGH; ent extension-API mechanics MEDIUM, corroborated across multiple independent web sources and one direct-source read of enthistory; Temporal/DBOS/River comparisons MEDIUM, standard published architecture)

## Standard Architecture

### System Overview

entflow is not server-shaped (no central coordinator process, no separate datastore). It is library-shaped: every "component" below is either a Go package the application imports, a table in the application's own database, or a goroutine the application starts. The database is simultaneously the system of record, the queue, and the lock manager.

```
┌──────────────────────────────────────────────────────────────────────┐
│  SCHEMA LAYER  (author-facing — one file per aggregate)               │
│  ┌──────────────┐   ┌──────────────┐   ┌───────────────────────────┐ │
│  │ Fields()     │   │ Hooks()/     │   │ Flows() → entflow builders │ │
│  │ (+Transitions│   │ Policy()     │   │ (New/UpdateSelf/Activity/  │ │
│  │  annotation) │   │              │   │  Emit/After/When)          │ │
│  └──────┬───────┘   └──────┬───────┘   └──────────────┬─────────────┘ │
└─────────┼──────────────────┼──────────────────────────┼───────────────┘
          │ consumed at codegen time (data)              │ consumed at codegen time (data)
          │ consumed at runtime (closures, via ent/runtime)│ closures bind at runtime
          ▼                                              ▼
┌──────────────────────────────────────────────────────────────────────┐
│  BUILD-TIME  (entc extension — the "codegen last" layer)              │
│  ┌────────────┐ ┌───────────────┐ ┌────────────┐ ┌─────────────────┐ │
│  │ Run-entity │ │ Transitions-  │ │ Flow-graph │ │ Worker/runner +  │ │
│  │ injection  │ │ hook + cross- │ │ validation │ │ span/Describe    │ │
│  │ (schemast, │ │ validator     │ │ (DAG check)│ │ codegen          │ │
│  │ pre-pass)  │ │               │ │            │ │                  │ │
│  └────────────┘ └───────────────┘ └────────────┘ └─────────────────┘ │
└──────────────────────────────────────────────────────────────────────┘
          │ produces generated Go code (ent client, ent/runtime, run entity,
          │ generated hook, generated runner) — no dependency the other way
          ▼
┌──────────────────────────────────────────────────────────────────────┐
│  RUNTIME LIBRARY  (entflow core — importable standalone, v0.1 target) │
│  ┌───────────┐ ┌────────────┐ ┌────────────┐ ┌──────────┐ ┌────────┐ │
│  │ Flow/Step │ │ Attempt +  │ │ Codec[In]  │ │ DI       │ │ Retry/ │ │
│  │ builders  │ │ idempotency│ │ (JSON core)│ │ registry │ │ backoff│ │
│  │           │ │ key deriv. │ │            │ │ (Provide/│ │ policy │ │
│  │           │ │            │ │            │ │ Use[T])  │ │        │ │
│  └───────────┘ └────────────┘ └────────────┘ └──────────┘ └────────┘ │
└──────────────────────────────────────────────────────────────────────┘
          │ operates against
          ▼
┌──────────────────────────────────────────────────────────────────────┐
│  WORKER PROCESS  (goroutine(s) — in-process w/ API server OR standalone)│
│  ┌────────────┐ ┌────────────┐ ┌───────────────┐ ┌──────────────────┐ │
│  │ Claim loop │ │ DB-step    │ │ Activity 3-beat│ │ Outbox relay loop │ │
│  │ (FOR UPDATE│ │ executor   │ │ executor       │ │ (same claim shape)│ │
│  │ SKIP LOCKED│ │ (single tx │ │ (3 small txs,  │ │                   │ │
│  │ on runs)   │ │  per step) │ │  no tx around   │ │                   │ │
│  │            │ │            │ │  the call)      │ │                   │ │
│  └────────────┘ └────────────┘ └───────────────┘ └──────────────────┘ │
└──────────────────────────────────────────────────────────────────────┘
          │ all writes go through
          ▼
┌──────────────────────────────────────────────────────────────────────┐
│  APPLICATION DATABASE  (Postgres — the only piece of infrastructure)  │
│  ┌───────────┐ ┌───────────────┐ ┌─────────────┐ ┌──────────────────┐ │
│  │ Aggregate │ │ <Flow>Run rows│ │ Outbox table│ │ (optional) River  │ │
│  │ tables    │ │ (state, input,│ │ (topic,     │ │ jobs table if     │ │
│  │ (Order..) │ │ step, attempt)│ │ payload, ack)│ │ River backend used│ │
│  └───────────┘ └───────────────┘ └─────────────┘ └──────────────────┘ │
└──────────────────────────────────────────────────────────────────────┘
          │ post-commit
          ▼
                    NATS JetStream (or other pluggable target)
```

### Component Responsibilities

| Component | Responsibility | Typical Implementation |
|-----------|----------------|-------------------------|
| Flow/step builder (runtime) | Typed DSL that records step name, kind, dependency edges, transition claims, retry policy as **data**, and binds the step **closure** | Generic `Flow[In]` struct built with fluent methods (`UpdateSelf`, `Activity`, `Emit`); mirrors `Hooks()`/`Policy()` in returning closures from a schema method |
| Flow metadata model | The in-memory, introspectable representation of a flow's topology (steps, kinds, deps, `In`/`Out` types) exposed as a stable API | A `FlowMeta`/`Describe()` struct walked by both the entc extension (build time) and entconnect (build time, external consumer) |
| Run store | Durable state of one execution: state, serialized input, current step, attempt counter, last error, timestamps, lineage edge | An ent entity (injected, not hand-owned) — reuses ent's full CRUD/hook/privacy/query stack for free |
| Worker/executor loop | Claims runnable rows, dispatches to the right step executor, advances state, drives retries | Single goroutine pool, `FOR UPDATE SKIP LOCKED` claim query, same shape for run-claim and outbox-claim |
| Activity execution protocol | The three-beat crash-safe envelope around an external call | Two tiny DB transactions bracketing one untransacted external call; framework, not step author, derives the idempotency key |
| Outbox relay | Delivers same-transaction-committed events to an external broker at-least-once | A second instance of the worker-loop shape, claiming outbox rows instead of run rows |
| Codegen extension (entc) | Injects the run entity, generates the enforcing transitions hook, cross-validates the flow DAG against the transitions map, generates the runner/worker glue and observability wiring | `entc.Extension` composed of a **pre-pass schema-writer** (schemast-based, like enthistory) + a **normal extension** (`Templates`/`Hooks`/`Annotations`) that only sees the injected entity once it exists on disk |
| DI registry | Typed handle registry so activity closures can reach external clients without the schema package constructing them | `sync.Map`-backed generic registry keyed by `reflect.Type`, `Provide[T]`/`Use[T](ctx)` |
| Observability layer | Per-run and per-step spans, attributes, the run table itself as audit log | OpenTelemetry spans wired by generated runner code; no separate telemetry store |

**Where the seams are (independent shippability):**
- Runtime library ↔ codegen: the builder API and step-execution semantics are fully expressible with a **hand-written** run schema (v0.1–v0.4). Codegen only *automates* what a user could otherwise hand-roll — this is the seam that makes "runtime-first, codegen-last" viable at all.
- Worker ↔ everything else: the worker only needs (a) a `*ent.Client`, (b) the flow metadata/registry, (c) the DI registry. It never needs to know whether flows were declared via handwritten Go or codegen-injected entities — it operates on ent query/mutation APIs either way.
- Outbox relay ↔ worker: architecturally the same claim-loop shape; can literally be the same goroutine pool with two claim queries, or a separate process — this is a deployment-topology decision, not an architectural dependency.
- entflow ↔ entconnect: one-directional metadata API (`FlowMeta`) is the entire contract; entflow never imports transport code (per the decoupling contract in the design doc).

## Recommended Project Structure

```
entflow/                       # root module — runtime library, no ent codegen dependency beyond ent itself
├── flow.go                    # Flow[In] builder, step kind types (DBStep/Activity/Emit)
├── step.go                    # step recording: name, kind, deps, transition claims, retry policy
├── attempt.go                 # Attempt type, deterministic idempotency key derivation
├── codec.go                   # Codec[In] interface + JSON codec (default, in core)
├── registry.go                # Provide[T]/Use[T](ctx) DI registry
├── retry.go                   # Backoff policy types
├── describe.go                # Describe()/dry-run — flow-as-data printer
├── meta/                      # flow metadata model — the entconnect contract surface
│   └── meta.go                # FlowMeta: name, owning entity, In/Out types, step topology
├── runner/                    # v0.1: single-tx executor (Multi-core, no run row)
│   └── multi.go
├── worker/                    # v0.2+: durable worker
│   ├── claim.go                # FOR UPDATE SKIP LOCKED claim query
│   ├── dbstep.go                # DB-step executor (one tx: effect + progress pointer)
│   ├── activity.go              # three-beat activity executor
│   ├── outbox.go                # relay loop (same claim shape, different table)
│   └── loop.go                  # poll interval / LISTEN-NOTIFY wiring, graceful shutdown
├── entc/                       # v0.5: codegen extension — separate importable package
│   ├── extension.go            # entc.Extension: Templates/Hooks/Annotations/Options
│   ├── inject/                 # schemast pre-pass: writes <Flow>Run schema files to disk
│   │   └── runentity.go
│   ├── validate/               # DAG check, transition cross-validator
│   │   └── graph.go
│   └── templates/               # go:embed'd templates for hook, runner, spans, Describe
└── testing/                    # crash-simulation harness (release gate)
    └── crashsim.go
```

### Structure Rationale

- **Root package has zero codegen dependency.** This is the load-bearing structural decision matching the "runtime-first" build order: `entflow` (root) must compile and be independently useful before `entflow/entc` exists. Anything that needs generated types lives in a separate importable subpackage.
- **`meta/` is its own package** because it is the one artifact a *different* module (entconnect) needs to import. Keeping it free of both `worker/` and `entc/` dependencies enforces the decoupling contract mechanically, not just by convention.
- **`entc/inject/` is separated from `entc/` proper** because it runs in a different *phase* of codegen (a pre-pass that mutates the filesystem before `entc.Generate` even starts) from `entc/validate/` and the template-driven hooks (which run inside the normal `entc.Generate` call, once the injected entity already exists as an ordinary schema file). Conflating these two would hide the two-pass nature of the mechanism (see below) behind one misleadingly simple-looking package.
- **`worker/outbox.go` sits next to `worker/claim.go`**, not in a separate `outbox/` package, because the outbox relay and the run-claim loop are mechanically the same primitive (per the design doc, §5.4) — the file layout should not imply a bigger architectural gap than exists.

## Architectural Patterns

### Pattern 1: Two-pass, disk-mediated entity injection (the entproto/enthistory precedent)

**What:** ent's extension API (`entc.Extension`) exposes `Hooks() []gen.Hook`, which *can* mutate the in-memory `gen.Graph` before/after codegen — but there is no sanctioned way to hand that hook chain a wholesale new *entity type* (fields, edges, its own generated CRUD API) and have it materialize as if hand-authored, in a single `entc.Generate()` call. The actual precedent — used by `enthistory` for its per-entity "history table" injection — is a **two-step, disk-mediated** process:
1. A pre-pass (`enthistory.Generate(...)`, built on the experimental `entgo.io/contrib/schemast` package) programmatically constructs and **writes new `schema/*.go` files to disk** before `entc.Generate` runs at all.
2. The *normal* `entc.Generate()` call — with a *separate* `entc.Extension` (`Templates`/`Hooks`/`Annotations`) — then runs against a schema directory that already contains the injected entity as an ordinary, disk-resident `.go` file. This second extension only ever sees a fully-formed schema; it does not construct one.

`entproto` is not the precedent for injection at all — it walks an *already-generated* `gen.Graph` and emits `.proto` **out** of it; it never adds nodes to the graph.

**When to use:** Any time the extension needs to add a whole new queryable entity (with its own generated Create/Update/Query builders, hooks, privacy) rather than just decorating existing entities with extra methods/fields.

**Trade-offs:**
- ✅ The injected entity gets 100% of ent's normal generated surface (transactional client methods, hooks, privacy, edges) for free — nothing bespoke to maintain.
- ✅ Matches ent's mental model: generated code is always "just" the output of `entc.Generate` over a schema directory; nothing magic happens at runtime.
- ❌ **Two full codegen passes are required**, and they must run in the right order (schema-inject pass, then normal `entc.Generate`) every single `go generate`. This is an ordering dependency the tool must enforce (e.g. a single `entc.go` driver that calls both in sequence, mirroring enthistory's pattern), not something `gen.Hook` ordering alone can express.
- ❌ The injected schema file is either checked into version control (like enthistory's generated history schemas) or regenerated-and-discarded every run; either way it is a *real Go source file* subject to Go's own compile/package rules, not a virtual graph node — this is a genuine risk to validate early (see Data/Code Split section below), because a flow's step topology can change between runs, meaning the injected `<Flow>Run` entity's shape (e.g., its generated `state` transitions annotation, per §3.4 of the design doc) has to be regenerated from the flow definition *before* the transitions-hook cross-validator can check anything — a second, internal two-pass bootstrap nested inside the outer one.

**Example (mechanism, not entflow code):**
```go
// entc.go (driver — mirrors enthistory's documented two-step main)
func main() {
    // Pass 1: disk-mediated injection — writes schema/<flow>_run.go files
    if err := entflow.InjectRunSchemas("./schema", flowsDiscoveredFrom(ent.Schemas)); err != nil {
        log.Fatal(err)
    }
    // Pass 2: normal entc.Generate, now operating on a schema dir that
    // already contains the injected run entities as ordinary files.
    if err := entc.Generate("./schema", &gen.Config{}, entc.Extensions(
        entflow.NewExtension(), // transitions-hook gen, cross-validation, runner codegen
    )); err != nil {
        log.Fatal(err)
    }
}
```

### Pattern 2: Data/code split via `ent/runtime` (validated, with a caveat)

**What:** ent already solves the exact problem entflow's `Flows()` method has: a schema-package method (`Hooks()`, `Policy()`) needs to return closures that reference *generated* types (`*ent.Tx`, `ent.Order`, `order.StatusCancelled`) — but the schema package is itself an *input* to generation, so it cannot import the generated package without a cycle. ent's fix is `ent/runtime`: a generated package, imported once by the application (near client construction), whose sole job is to call back into the schema package's `Hooks()`/`Policy()` methods and register the returned closures against the live generated `ent.Client` at *runtime*, not at generation time. Generation time only ever needs the *data* half of a schema method's contract (which fields exist, which annotations are attached) — never the closure bodies.

**When to use:** Exactly entflow's situation. `Flows()` should be a schema method returning builder objects; at generation time the extension walks the builder's exported **data** (step names, kinds, `After`/`When` edges, `Transition(...)` claims, retry config, emit topics) — never touching the closures. At runtime, `ent/runtime` (extended to also walk `Flows()`) registers the closures against the live client, exactly as it does today for hooks.

**Trade-offs:**
- ✅ This is not a novel mechanism entflow has to invent — it is litigated, working ent infrastructure since ent's earliest hook design. Confidently reusable.
- ✅ Preserves "schema file is the program": step bodies stay inline, no generated cross-package interface.
- ⚠️ **The caveat that must be validated, not assumed:** ent's `Hooks()`/`Policy()` mechanism works because closures are *opaque* to codegen — codegen never needs to know what a hook *does*, only that one exists. entflow's design is stricter: the extension needs *some* structural facts that live inside the builder chain even though it must not execute closures — e.g., a step's declared `Transition("x")` claim (data, fine) is chained directly onto the same builder call that also carries the closure (`UpdateSelf("cancel", entflow.Transition("cancelled"), func(...) {...})`). This is fine **as long as the builder API is designed so every fact codegen needs is a discrete, inspectable argument/method on the builder — never something inferred by executing or parsing the closure body**. The design doc's step syntax already follows this discipline (transition claims, `After`, `When`, retry policy are all separate arguments/chained calls, not embedded in the closure) — the risk is a *future* feature (e.g. a step wanting to declare "I write to field X" for a more precise cross-validator) accidentally requiring closure introspection, which the ent precedent gives no answer for. **Recommendation: treat "codegen never inspects closure bodies, only chained builder data" as an explicit, enforced project invariant, not an implicit assumption** — a v0.1/v0.2 design-review checklist item, and worth a unit test asserting the builder's data-producing methods do not require evaluating the closure.

**Example:**
```go
// schema/order.go — generation time sees the *arguments* to UpdateSelf/Activity/Emit
// (step name, entflow.Transition("cancelled"), entflow.When(...), entflow.Retry(...))
// but never executes or inspects the trailing func literal's body.
func (Order) Flows() []ent.Flow {
    return []ent.Flow{
        entflow.New[*CancelOrderRequest]("CancelOrder").
            UpdateSelf("cancel", entflow.Transition("cancelled"), func(ctx context.Context, tx *ent.Tx, in *CancelOrderRequest) (*ent.Order, error) {
                return tx.Order.UpdateOneID(in.OrderID).SetStatus(order.StatusCancelled).Save(ctx)
            }),
    }
}
```

### Pattern 3: Worker loop as a claim-execute-advance state machine (River/asynq precedent)

**What:** Both River (Postgres-native) and asynq (Redis-native) converge on the same shape despite different backing stores: a claim step that atomically removes/locks a batch of runnable work, an execute step that runs user code without holding that lock across it, and a completion step that records outcome and either releases, retries, or terminates. River's Postgres implementation is the directly relevant precedent for entflow: `FOR UPDATE SKIP LOCKED` for the claim, a per-process "producer" that batch-claims once and fans work out to goroutines locally (rather than each goroutine round-tripping to the DB), and a **dual wakeup mechanism** — `LISTEN`/`NOTIFY` for low-latency dispatch (fires only post-commit, so it is inherently crash-safe — a notify for a row that then gets rolled back never fires) plus a periodic poll as the correctness backstop for missed/coalesced notifications or environments where `LISTEN`/`NOTIFY` doesn't reach the worker (e.g. through certain connection poolers).

**When to use:** This is the model for both the run-claim loop and the outbox relay loop (design doc §5.4 calls this out explicitly — "mechanically the same loop"). It also directly informs the "in-process vs dedicated worker binary" open question: because the loop's only dependencies are `*ent.Client` + flow registry, it is trivially startable as a goroutine inside an API server process or as a `main()` in a standalone binary — the topology question is deployment, not architecture.

**Trade-offs:**
- ✅ No coordination service needed — the row lock *is* the coordination.
- ✅ `SKIP LOCKED` means N workers scale claim throughput roughly linearly without contention (workers never block each other on the claim query).
- ⚠️ Poll interval is a latency/load tradeoff (design doc doesn't specify a default — recommend starting conservative, e.g. 1–5s, with `tx.OnCommit` nudge or `LISTEN`/`NOTIFY` doing the latency work in the common case, matching River's "notify does the work, poll is the backstop" division of labor).
- ⚠️ Unlike River/asynq, entflow's claimed unit (a run row) can require *multiple* claim-execute-advance cycles to reach a terminal state (one per step), not one claim-execute-done cycle per job. The loop must re-claim the same run row on its next step rather than treating "claimed once" as "owned until terminal" — this is a meaningful divergence from the job-queue precedent worth calling out explicitly in the worker design (no long-held lease across steps; each step boundary is a fresh claim).

## Data Flow

### Full flow execution: Start → claim → DB step → activity → emit → terminal

```
[Caller: RPC handler / cron / CLI / outbox consumer]
    │  flow.Start(ctx, in)
    ▼
┌─────────────────────────────────────────────────────────┐
│ TX 1 (owned by: whoever calls Start — the caller's tx,   │
│        or a dedicated tx if Start is called standalone)   │
│  INSERT <Flow>Run: state=pending, input=Codec.Marshal(in)│
└─────────────────────────────────────────────────────────┘
    │  commit
    ▼
[Worker loop, any worker process]
    │  SELECT ... WHERE state IN (claimable) FOR UPDATE SKIP LOCKED
    ▼
┌─────────────────────────────────────────────────────────┐
│ TX 2 (owned by: worker, for the DB step "cancel")        │
│  execute UpdateSelf closure against *ent.Tx              │
│  UPDATE Order SET status=cancelled   (hooks/policy fire) │
│  UPDATE <Flow>Run SET current_step=refund, state=running │
│  ── same tx: step effect and progress pointer commit atomically ──│
└─────────────────────────────────────────────────────────┘
    │  commit
    ▼
[Worker loop — next claim cycle picks up the same run row]
    ▼
┌─────────────────────────────────────────────────────────┐
│ TX 3a (owned by: worker, activity "refund", beat a)      │
│  UPDATE <Flow>Run SET attempt=attempt+1,                 │
│    idempotency_key = f(run_id, "refund", attempt)        │
└─────────────────────────────────────────────────────────┘
    │  commit
    ▼
┌─────────────────────────────────────────────────────────┐
│ NO TRANSACTION — external call                            │
│  stripe.Refunds.Create(..., IdempotencyKey: key)          │
│  ⚠ crash window: call succeeds but result never recorded  │
│    → re-execution reuses same key → provider returns      │
│      original effect (idempotent by construction)         │
└─────────────────────────────────────────────────────────┘
    │  success
    ▼
┌─────────────────────────────────────────────────────────┐
│ TX 3b (owned by: worker, activity "refund", beat c)       │
│  UPDATE <Flow>Run SET result_refund=JSON(...),             │
│    current_step=notify, state=running                      │
└─────────────────────────────────────────────────────────┘
    │  commit
    ▼
[Worker loop — next claim cycle]
    ▼
┌─────────────────────────────────────────────────────────┐
│ TX 4 (owned by: worker, Emit "order.cancelled")            │
│  INSERT outbox(topic=order.cancelled, payload=...)         │
│  UPDATE <Flow>Run SET state=done                            │
│  ── outbox row and terminal state commit atomically ──     │
│  tx.OnCommit → nudge relay (latency optimization)           │
└─────────────────────────────────────────────────────────┘
    │  commit (run is now terminal; caller/status-endpoint can query it)
    ▼
[Relay loop — separate/same claim shape, own poll or LISTEN/NOTIFY]
    │  SELECT ... FROM outbox WHERE delivered=false FOR UPDATE SKIP LOCKED
    ▼
┌─────────────────────────────────────────────────────────┐
│ publish to NATS JetStream (Nats-Msg-Id = outbox row id)    │
│  — no DB tx around the network call, same beat-b pattern    │
└─────────────────────────────────────────────────────────┘
    │  ack
    ▼
┌─────────────────────────────────────────────────────────┐
│ TX 5 (owned by: relay)                                      │
│  UPDATE outbox SET delivered=true                            │
└─────────────────────────────────────────────────────────┘
```

**Ownership summary — one line per transaction boundary:**
1. `flow.Start` owns the run-row insert. Caller-supplied or self-opened tx.
2. Worker owns every DB-step transaction: step effect + progress pointer, atomically, per step.
3. Worker owns activity beats (a) and (c) as separate small transactions; beat (b), the external call itself, is deliberately outside any transaction.
4. Worker owns the Emit transaction: outbox insert + terminal/next-state update, atomically.
5. Relay owns delivery (untransacted network call) and the delivered-mark transaction, separately — the same three-beat discipline as activities, structurally.

### Key Data Flows

1. **Codegen-time data flow (one-directional, schema → generated code):** `Flows()` builder data (never closures) → entc extension → (a) injected `<Flow>Run` schema file, written to disk in a pre-pass, (b) generated transitions-enforcing hook merged with the state-machine cross-validation, (c) generated runner/worker glue, (d) generated OTel span wiring. This entire flow happens once, at `go generate` time, and produces ordinary Go source — nothing about it exists at request-serving runtime.
2. **Runtime closure-binding flow (one-directional, ent/runtime → live client):** at application boot, `ent/runtime` (extended for `Flows()`) walks schema methods and registers step closures against the constructed `*ent.Client`, mirroring exactly how `Hooks()`/`Policy()` bind today.
3. **Metadata flow to entconnect (one-directional, entflow → entconnect, build time or runtime):** `FlowMeta` (name, owning entity, `In`/`Out` types, step topology) is the only artifact entconnect reads; entflow has zero awareness that a consumer exists.

## Scaling Considerations

| Scale | Architecture Adjustments |
|-------|--------------------------|
| Single process / dev | In-process worker goroutine, poll-interval-only (no `LISTEN`/`NOTIFY` needed), JSON codec, built-in poller — zero extra infra beyond Postgres, matches the "no infrastructure" constraint exactly. |
| Multiple API server replicas | Worker embedded in every replica is fine — `FOR UPDATE SKIP LOCKED` makes concurrent claim across N processes correct by construction; no leader election needed. Add `LISTEN`/`NOTIFY` (or `tx.OnCommit` nudge) to keep latency low as replica count (and therefore poll fan-out) grows. |
| High activity volume / long-running externals | Split into a dedicated worker binary so activity latency (e.g. a slow Stripe call) doesn't compete with API-server goroutine budget or trigger connection-pool starvation; this is the point at which the "in-process vs dedicated worker binary" open question resolves in favor of dedicated. |
| Very high run throughput | This is the point at which the design doc's optional River backend (§4, §10.5) becomes worth the added dependency — River's producer-side batch-claim and priority queues outperform a naive poll loop at scale; the adapter boundary should be designed *before* this is needed, not retrofitted. |

### Scaling Priorities

1. **First bottleneck: claim contention / poll latency**, not throughput — solved by `LISTEN`/`NOTIFY` + `tx.OnCommit` nudge (design doc §5.4 already specifies this), not by architecture changes.
2. **Second bottleneck: activity-call concurrency vs API-server resource budget** — solved by the worker-topology split (dedicated binary), which the design correctly defers as an open question rather than over-designing now.

## Suggested Build Order

The design doc proposes: **v0.1 Multi core → v0.2 durability → v0.3 activities → v0.4 outbox → v0.5 codegen** (runtime-first, codegen-last). Validated against the dependency structure surfaced by this research, with one refinement.

### Why the ordering is sound

- **Every capability through v0.4 is expressible by hand.** Because ent's own `Hooks()`/`Policy()` precedent already proves closures-in-schema-methods works without any codegen involvement (Pattern 2), entflow's core value proposition — crash-safe multi-step execution against the app's own DB — never actually *requires* codegen. It only requires the runtime library plus a hand-written run schema. This is what makes "codegen last" more than a sequencing preference: codegen is additive automation over a fully-working system, not a load-bearing dependency of it.
- **Each version strictly depends only on its predecessors, never forward.** v0.2 (run-row persistence, worker poller, crash-resume) depends on v0.1's builder/execution shape but not on activities or outbox. v0.3 (activity protocol, retries, DI) depends on v0.2's run row (attempt counter, current-step pointer already exist) but is otherwise additive — it does not change the DB-step or claim-loop code. v0.4 (outbox, relay) reuses v0.2's claim-loop shape wholesale (per Pattern 3) and is architecturally a sibling of the worker, not a dependent of v0.3. v0.5 (codegen) is the only version that depends on *everything* below it, because its entire job is automating hand-written v0.2–v0.4 artifacts — which is exactly why it must go last: there is nothing stable to automate until the shape it's automating has been proven by hand.
- **The release gate (crash-simulation harness, §7 of the design doc) is achievable at v0.2**, before activities or outbox exist, and should be — validating crash-resume against the simplest possible case (DB-only steps) first isolates worker-loop bugs from activity-protocol bugs. Recommend treating the harness as a *living* deliverable that gains new crash points at v0.3 (the three-beat window) and v0.4 (relay delivery), rather than a single v0.5-adjacent artifact — this is a refinement, not a disagreement with the design doc's placement of "no public release without it" as a gate that spans all versions.

### The one ordering risk worth flagging

**v0.5's actual mechanism is riskier and more novel than "codegen last" suggests, and should not be sized like a mechanical automation pass.** Per Pattern 1 above, the sanctioned way to inject a wholesale new entity into the ent graph (the `<Flow>Run` entity) is not a single `entc.Extension.Hooks()` pass — it is a two-phase, disk-mediated process (schemast pre-pass writing schema files, then a normal extension pass), and this has *not* been proven anywhere at the fidelity entflow needs: enthistory's injected entities are structurally simple (append-only change logs); entflow's injected `<Flow>Run` entity needs its `state` field's transitions annotation to be *derived from the flow's own step graph* (design doc §3.5, dogfooding the transitions cross-validator against itself). That is a strictly harder injection problem than any published precedent. **Recommendation: budget v0.5 as its own multi-phase milestone with an early spike specifically on "inject an entity whose generated transitions annotation is itself computed from sibling schema data," before committing to the full run-entity-injection + transitions-hook-generation + cross-validation scope in one phase.** This doesn't change the v0.1→v0.5 ordering — it only argues against treating v0.5 as a single homogeneous phase in the roadmap; it likely wants splitting into (5a) entity injection mechanism proven standalone, (5b) transitions-hook generation + cross-validation, (5c) runner/worker/observability codegen.

## Anti-Patterns

### Anti-Pattern 1: Holding a transaction open across an external call

**What people do:** Wrap the whole activity (DB write + external API call) in a single transaction "for consistency."
**Why it's wrong:** External calls have unbounded latency and unbounded failure modes; holding a DB transaction (and its row locks) open across them causes lock contention, connection-pool exhaustion, and — worse — makes the crash-recovery story ambiguous (was the effect committed before or after the external call?). This is precisely why the design doc's three-beat protocol structurally forbids it (Activities never receive `*ent.Tx`).
**Do this instead:** The three-beat protocol — stamp-and-commit, call-untransacted, record-and-commit — with the framework (not the step author) deriving the idempotency key from data that's stable across retries (`runID:stepName:attempt`).

### Anti-Pattern 2: Treating the run-row claim as a long-held lease across the whole flow

**What people do:** Model workflow execution like a classic job queue — claim once, hold ownership (a lease/heartbeat) until the entire multi-step flow finishes, to avoid "losing" the run to another worker mid-flight.
**Why it's wrong:** Leases require heartbeats, heartbeat failure handling, and lease-expiry reclaim logic — a second failure-mode surface on top of the one the run-row model already handles for free via re-claimability. Because every step commits its own progress pointer in its own transaction, there is nothing unsafe about a *different* worker picking up the same run row after step N — the DB, not a lease, is the source of truth for "how far did this get."
**Do this instead:** No lease. Each step boundary is an independent claim (`state` and `current_step` on the row are sufficient concurrency control alongside `FOR UPDATE SKIP LOCKED`). This is the correct generalization of River's model to entflow's multi-beat-per-row shape, not a deviation from it.

### Anti-Pattern 3: Letting codegen infer facts by inspecting closure bodies

**What people do:** To avoid asking the schema author to state something twice, an extension author is tempted to statically analyze the closure's Go AST/source to infer facts (e.g., "this closure calls `tx.Order.Update`, so it must write to Order") rather than requiring an explicit builder argument.
**Why it's wrong:** This breaks the entire mechanism entflow depends on (Pattern 2, above) — ent's `ent/runtime` cycle-breaking works precisely because generation time never needs to understand closure *behavior*, only closure *existence*. AST inspection is also fragile (breaks on refactors, indirection, helper functions) and reintroduces exactly the "generated code can be wrong" risk the design doc's vibe-coding guardrail argument is built to eliminate.
**Do this instead:** Every fact codegen needs must be a first-class, explicit builder argument (`entflow.Transition(...)`, `entflow.After(...)`, `entflow.When(...)`) — never inferred. If a future feature seems to need closure introspection, that's a signal the builder API is missing an argument, not a signal to add AST analysis.

## Integration Points

### External Services

| Service | Integration Pattern | Notes |
|---------|---------------------|-------|
| NATS JetStream | Relay publishes with `Nats-Msg-Id` header set to a stable value (e.g. outbox row id) for server-side dedup within the stream's configured window | Server-side dedup is a bounded-window optimization, not a substitute for idempotent consumers — outbox delivery is at-least-once end-to-end regardless. |
| Stripe (and compliant external providers generally) | Activity beat (b) passes the framework-derived idempotency key through the provider's own idempotency-key mechanism | Depends entirely on the provider actually honoring idempotency keys server-side; providers that don't are a documented limitation of the exactly-once-outcome guarantee, not something entflow can fix. |
| River (optional backend) | Adapter boundary swaps the built-in poller's claim/execute loop for River's client/producer, keeping the run-row model unchanged | Explicitly an open question (design doc §10.5) — the adapter interface should be designed with this swap in mind even in v0.1–v0.4, to avoid a breaking change later. |

### Internal Boundaries

| Boundary | Communication | Notes |
|----------|---------------|-------|
| Runtime library ↔ entc extension | One-directional: extension reads builder data at generation time; runtime library never imports the extension package | Enforced by Go module/package structure (`entflow` vs `entflow/entc`), not just convention — the extension package should be import-able independently and the root package must never depend on it. |
| Worker ↔ flow declarations | Worker resolves step closures via the flow registry populated by `ent/runtime` at boot; never inspects schema files directly | Matches how ent's own hook dispatch works — the worker is generic over any registered flow, codegen or hand-authored. |
| entflow ↔ entconnect | `FlowMeta` API only; no shared code beyond that struct/interface | Per design doc §9, this is a non-negotiable decoupling contract — entflow must remain fully usable with zero contract layer. |
| DI registry ↔ activity closures | `entflow.Use[T](ctx)` reads from a registry populated once at boot via `entflow.Provide` | Registry should be swappable per-test (mock client injection) without modifying schema files — this is explicitly called out in the design doc as a testability requirement. |

## Sources

- [ent Extensions documentation](https://entgo.io/docs/extensions/) — MEDIUM confidence (official docs)
- [ent extension.go source, ent/ent repo](https://github.com/ent/ent/blob/master/doc/md/extension.md) — MEDIUM confidence (official source)
- [entc package reference, pkg.go.dev](https://pkg.go.dev/entgo.io/ent/entc) — MEDIUM confidence (official godoc)
- [entgql package, entgo.io/contrib](https://pkg.go.dev/entgo.io/contrib/entgql) — MEDIUM confidence
- [Hooks | ent](https://entgo.io/docs/hooks/) — MEDIUM confidence (official docs, ent/runtime cycle-breaking)
- [enthistory repository and extension.go](https://github.com/flume/enthistory) — MEDIUM confidence (verified: source file read directly, cross-checked against README's documented two-step `entc.go` driver)
- [entgo.io/contrib/schemast (programmatic schema authoring)](https://entgo.io/docs/generating-ent-schemas/) — MEDIUM confidence (official docs, marked experimental by ent itself)
- [entproto package, pkg.go.dev](https://pkg.go.dev/entgo.io/contrib/entproto) — MEDIUM confidence (clarifies entproto generates proto FROM the graph, not the reverse)
- [Temporal: Events and Event History](https://docs.temporal.io/workflow-execution/event) — MEDIUM confidence (official docs)
- [Temporal: Tasks](https://docs.temporal.io/tasks) — MEDIUM confidence (official docs)
- [DBOS: Postgres-backed Durable Workflow Execution](https://www.dbos.dev/blog/postgres-is-all-you-need-for-durable-execution) — MEDIUM confidence (vendor blog, architecture claims consistent across DBOS docs and Supabase writeup)
- [DBOS Architecture docs](https://docs.dbos.dev/architecture) — MEDIUM confidence (official docs)
- [River: a Fast, Robust Job Queue for Go + Postgres — brandur.org](https://brandur.org/river) — MEDIUM confidence (author's own architecture writeup)
- [riverqueue/river client.go, GitHub](https://github.com/riverqueue/river/blob/master/client.go) — MEDIUM confidence (source-adjacent)
- [How to implement the Outbox pattern in Go and Postgres — ITNEXT](https://itnext.io/how-to-implement-the-outbox-pattern-in-go-and-postgres-e9bc2699cbe2) — LOW-MEDIUM confidence (community writeup, pattern is standard/well-established independent of this source)
- [NATS JetStream: Reliable Message Delivery — Synadia](https://www.synadia.com/blog/jetstream-reliable-delivery-dlq-replay) — MEDIUM confidence (vendor/maintainer blog)
- [JetStream Model Deep Dive | NATS Docs](https://docs.nats.io/using-nats/developer/develop_jetstream/model_deep_dive) — MEDIUM confidence (official docs)
- [hibiken/asynq repository](https://github.com/hibiken/asynq) — MEDIUM confidence (official README)

---
*Architecture research for: durable workflow orchestration library, ent extension (Go)*
*Researched: 2026-08-08*
