# entflow

## What This Is

entflow is an [ent](https://entgo.io) extension that makes durable workflows a schema concern. Workflows are declared inside ent schema files via a `Flows()` method — a peer of `Fields()`, `Hooks()`, and `Policy()` — with step bodies written as typed closures inline in the declaration. The extension injects a persisted "run" entity per flow, generates a worker that executes runs with crash-resume semantics, and cross-validates workflow steps against the entity's declared state machine at codegen time.

Positioning: **Temporal-lite for ent** — durable, resumable, exactly-once-outcome workflows with no additional infrastructure beyond the database ent already uses. It is for Go teams building ent-backed applications who need multi-step business processes (sagas, order lifecycles, payment flows) without adopting a separate workflow engine.

## Core Value

A multi-step business process declared in the ent schema executes durably — surviving crashes at any point without duplicating external effects or committing a step without its progress record.

## Requirements

### Validated

(None yet — ship to validate)

### Active

- [ ] Flow/step builder API with typed closures declared via `Flows()` in schema files
- [ ] Three step kinds with distinct safety rules: DB steps (receive `*ent.Tx`), Activities (no `*ent.Tx` — external calls), Emits (fire-and-forget outbox)
- [ ] DB step execution where step effect and run progress pointer commit in the same transaction
- [ ] Run-row persistence: a run entity per flow with state, serialized input, current step, attempt counter, last error, timestamps, lineage edge to the owning aggregate
- [ ] Worker poller with `FOR UPDATE SKIP LOCKED` claim — the database is the queue, no coordination service
- [ ] Crash-resume: any interrupted run is resumable by any worker to the same terminal state
- [ ] Activity three-beat protocol (stamp attempt+key → execute untransacted → persist result) with framework-supplied deterministic idempotency keys (`runID:stepName:attempt`)
- [ ] Retry policies with backoff on Activities; exhausted retries move the run to `failed:<step>`
- [ ] Typed DI registry: `entflow.Provide(registry, client)` at startup, `entflow.Use[T](ctx)` in activity bodies
- [ ] `entflow.Codec[In]` input serialization with a JSON codec in core (no `proto.Message` requirement)
- [ ] Emit steps writing outbox rows in the anchor step's transaction, plus a relay (poll → claim → deliver → mark) with pluggable targets (NATS JetStream first-class)
- [ ] Flow chaining: an outbox event can start another flow
- [ ] Dependency edges (`After`) and conditional guards (`When`, `SelfWas`) validated as a DAG at codegen
- [ ] entc extension: run-entity injection, transitions-hook generation, flow-graph validation, worker/runner codegen
- [ ] State-machine cross-validation: every `Transition("x")` claimed by a step must be a legal edge in the status field's transitions annotation, and every declared edge should be claimed by some step
- [ ] Generated transitions-enforcing hook with no `SkipHook` escape
- [ ] `DenyStatusEscalation` privacy rule allowing privileged transitions only under a workflow marker only the generated runner sets
- [ ] Runs are privacy-governed queryable entities (status endpoints, ops dashboards, admin retry as a permitted state transition)
- [ ] Per-step OpenTelemetry spans named `workflow.<Flow>.<step>`, with attempt count, error, and state as attributes
- [ ] `Describe()` printing the flow as data (step, kind, deps, transitions claimed) — doubles as dry-run
- [ ] Deterministic crash-simulation harness: kill the worker at every step boundary and every activity beat against a real database, asserting no uncommitted-progress step effects, no duplicated activity effects, identical terminal states
- [ ] Property tests on the transitions cross-validator and golden-file tests for generated code
- [ ] Flow metadata API exposing flow name, owning entity, `In`/`Out` Go types, and step topology for external consumers

### Out of Scope

- Compensation / saga rollback (`entflow.OnFail`) — API reserved, but v0's honest story is that failed runs stay inspectable and manually retryable; undo after an Activity means more steps, not abort
- Timers, delays, scheduled steps (`entflow.Sleep`, `wake_at` column) — natural extension of the run row, post-MVP
- Transport binding (protobuf, Connect, gRPC, HTTP) — belongs to the sibling project **entconnect**; entflow imports `ent` only
- Proto `Codec` adapter — lives outside entflow core (entconnect or a ~10-line `entflowproto` shim)
- Schema-external flow declaration for multi-aggregate sagas — a workflow with no owning aggregate is evidence of a missing aggregate (e.g. `Settlement`); this is a recorded policy decision, not an oversight
- Generated per-flow results structs for cross-step typed access — deferred; `entflow.Result[T](ctx, "step")` is the accepted v0 compromise, revisit once real flows show how often the positional-self shortcut is insufficient
- Competing with River as a queue — River should be an optional backend behind an adapter boundary; the built-in poller is the zero-dependency default

## Context

**Why this exists.** Ent gives entities three homes for behavior: field validators (per-value invariants), hooks (per-mutation invariants), and privacy policies (access control). None can express multi-step business processes. Hooks in particular cannot originate mutations, cannot own transaction boundaries, cannot safely perform external side effects (the dual-write problem), and cannot express sagas with compensation. Every framework ecosystem that started with model callbacks — Rails, Laravel, Django — converged on the same conclusion: invariants live with the data, sequence lives in an explicit, inspectable artifact.

**The schema-as-constitution philosophy.** The surrounding stack treats the schema as the single reviewable artifact — the one file a human (or an LLM writing code under guardrails) needs to read and edit. Fields, transitions, invariants, and access rules already live there. entflow completes the picture so that "what is an Order" and "what happens to an Order" are answered by one file.

**The vibe-coding guardrail argument.** Generated or LLM-written orchestration code can be wrong, but with entflow it cannot be wrong *and commit*: every mutation flows through the transactional client (hooks and privacy fire normally), step wiring is enforced by types, external calls are structurally separated from DB state, and idempotency keys are supplied by the framework rather than trusted to generated code.

**Data/code split.** Builder-carried *data* (step names, kinds, dependency edges, retry policies, transition claims, emit topics) is visible to the entc extension at generation time. Closures are *not* serialized; they bind at runtime via `ent/runtime`, exactly as hooks do. One declaration, two consumers. Bootstrap is the standard two-pass flow ent users already know from hooks.

**Prior art (argument from convergence).** Rails callbacks → service objects; Ecto refused callbacks entirely and made the workflow a first-class value (`Ecto.Multi` — the direct inspiration for v0.1); Laravel `ShouldDispatchAfterCommit`; Django signals banned for business logic in favor of Celery; Axon/Commanded formalized aggregates-vs-sagas; Temporal/Restate made the workflow runtime durable — entflow's run row is Temporal's insight implemented on the application's own tables, with the state field as an explicit column rather than a persisted instruction pointer.

**Ecosystem risk, priced in.** Ent's extension API is stable but under-documented (entgql's source is the real documentation), and ent's center of gravity has shifted toward Atlas. entflow would likely become the most active project in the contrib ecosystem — simultaneously a maintenance commitment and a mindshare opportunity.

## Constraints

- **Dependencies**: entflow depends on `ent` only — never imports protobuf, Connect, HTTP, or descriptor machinery. The decoupling contract with entconnect is non-negotiable.
- **Infrastructure**: No infrastructure beyond the database ent already uses. The database is the queue; no coordination service, no broker required for correctness.
- **Tech stack**: Go, ent, entc extension API (sanctioned extension surface only — modeled on entgql/entproto).
- **Input types**: `In` must satisfy `entflow.Codec[In]` but must never be required to be a `proto.Message` — inputs originate from RPC, cron, CLI, tests, or another flow's outbox event.
- **Type safety**: Activity closures must not receive `*ent.Tx` — structural, not conventional, separation of external calls from DB state.
- **Release gate**: No public release without the deterministic crash-simulation harness passing. Trust in a workflow engine is won or lost on crash-resume.
- **Build order**: Layers must be independently shippable — the runtime library must work (with ugly ergonomics, reflection-based) before codegen exists.

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Flows declared in schema via `Flows()` method, not annotations | Code states its types; annotations are data and would need an `Input(...)` builder. Ent already returns closures from `Hooks()` and `Policy()`. | — Pending |
| Type parameter `entflow.New[In](name)` for input typing | Step closure signatures compile-checked against `In` | — Pending |
| Step bodies inline in the schema file | The schema file is the program; no deferred `steps/` package or generated cross-package interface | — Pending |
| Three step kinds distinguished by closure signature (Tx / no Tx / emit) | The type system makes it impossible to write DB state from inside an external call | — Pending |
| Progress pointer advances in the same transaction as the step effect | Crash anywhere re-runs the step cleanly — no step effect ever commits without its progress record | — Pending |
| Framework supplies idempotency keys (`runID:stepName:attempt`) | This is the exact line generated code must never be trusted to invent; at-least-once execution + idempotent effect = exactly-once outcome | — Pending |
| Runs are persisted rows in the app's own database | Durability falls out of persistence; runs become privacy-governed, queryable ops surface; no separate workflow store | — Pending |
| Run `state` is itself a transitions-annotated status enum generated from the step graph | Dogfooding — entflow enforces its own workflow-state legality with its own machinery | — Pending |
| `FOR UPDATE SKIP LOCKED` claim; database is the queue | Multiple workers need no coordination service; zero added infrastructure | — Pending |
| Codegen (entc extension) is the LAST milestone, not the first | The flashiest layer is the least essential to proving the model works | — Pending |
| DI via `entflow.Provide` / `entflow.Use[T](ctx)` | Schema files cannot construct a Stripe client; the honest boundary is schema declares *what happens*, `main.go` supplies handles to the outside world. Also makes flows testable unmodified. | — Pending |
| Multi-aggregate sagas get no schema-external declaration home | A workflow with no owning aggregate is evidence of a missing aggregate. Expected to be the most-asked question — document loudly. | — Pending |
| Compensation deferred past MVP | Single-tx rollback is physics-limited to DB-only flows; inspectable + manually retryable failed runs is the honest v0 story | — Pending |

## Open Questions

1. `ent.Flow` as a name requires either an upstream interface or entflow defining its own interface type returned from `Flows()` and discovered by the extension — the latter is self-contained and assumed for v0; naming (`entflow.Flow`) TBD.
2. Cross-step typed results: revisit generated per-flow results structs once real flows show how often the positional-self shortcut is insufficient.
3. Worker deployment topologies: in-process with the API server (default, zero-infra) vs. dedicated worker binary — both must work; generated `main` helpers TBD.
4. River backend: adapter boundary design so the poller and River are interchangeable.

## Evolution

This document evolves at phase transitions and milestone boundaries.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd-complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

---
*Last updated: 2026-08-08 after initialization*
