# entflow — Design Document

**Status:** Draft v1 · **Author:** Shahar Mintz (smintz) · **Date:** 2026-08-05

## 1. Summary

entflow is an [ent](https://entgo.io) extension that makes durable workflows a schema concern. Workflows are declared inside ent schema files via a `Flows()` method — peers of `Fields()`, `Hooks()`, and `Policy()` — with step bodies written as typed closures inline in the declaration. The extension injects a persisted "run" entity per flow, generates a worker that executes runs with crash-resume semantics, and cross-validates workflow steps against the entity's declared state machine at codegen time.

Positioning: **Temporal-lite for ent** — durable, resumable, exactly-once-outcome workflows with no additional infrastructure beyond the database ent already uses.

entflow is deliberately contract-agnostic. It does not know about protobuf, Connect, gRPC, or HTTP. Transport binding is the job of a sibling project, **entconnect** (separate design doc). The two projects must remain decoupled; the coupling contract is defined in §9.

## 2. Motivation

The problem entflow solves is where orchestration lives in an ent application. Ent gives entities three homes for behavior: field validators (per-value invariants), hooks (per-mutation invariants), and privacy policies (access control). None of these can express multi-step business processes. Hooks in particular cannot originate mutations, cannot own transaction boundaries, cannot safely perform external side effects (the dual-write problem), and cannot express sagas with compensation. Every framework ecosystem that started with model callbacks (Rails, Laravel, Django) converged on the same conclusion: invariants live with the data, sequence lives in an explicit, inspectable artifact.

The design philosophy of the surrounding stack is that the schema is the single reviewable constitution of the system — the one file a human (or an LLM writing code under guardrails) needs to read and edit. Fields, transitions, invariants, and access rules already live there. entflow completes the picture by making the processes live there too, so that "what is an Order" and "what happens to an Order" are answered by one file.

The vibe-coding guardrail argument is central: generated or LLM-written orchestration code can be wrong, but with entflow it cannot be wrong *and commit*, because every mutation flows through the transactional client (hooks and privacy fire normally), step wiring is enforced by types, external calls are structurally separated from DB state, and idempotency is supplied by the framework rather than trusted to generated code.

## 3. Core Concepts

### 3.1 Flow

A flow is a named, typed, directed sequence of steps declared on the entity that owns it (DDD: the aggregate whose lifecycle it drives). Declaration is a schema method:

```go
// schema/order.go (or order_flows.go, same package/type)
func (Order) Flows() []ent.Flow {
    return []ent.Flow{
        entflow.New[*orderv1.CancelOrderRequest]("CancelOrder").
            UpdateSelf("cancel", entflow.Transition("cancelled"),
                func(ctx context.Context, tx *ent.Tx, in *orderv1.CancelOrderRequest) (*ent.Order, error) {
                    return tx.Order.UpdateOneID(in.OrderId).
                        SetStatus(order.StatusCancelled).
                        Save(ctx)
                }),
            Activity("refund",
                entflow.When(entflow.SelfWas("paid")),
                entflow.Retry(entflow.Backoff(5, time.Second, time.Minute)),
                func(ctx context.Context, o *ent.Order, att entflow.Attempt) (entflow.JSON[RefundResult], error) {
                    ref, err := entflow.Use[*stripe.Client](ctx).Refunds.Create(ctx, &stripe.RefundParams{
                        PaymentIntent:  o.PaymentIntentID,
                        IdempotencyKey: att.IdempotencyKey(),
                    })
                    if err != nil {
                        return entflow.JSON[RefundResult]{}, err
                    }
                    return entflow.NewJSON(RefundResult{RefundID: ref.ID}), nil
                }),
            Emit("order.cancelled", entflow.After("cancel")),
    }
}
```

Design commitments embodied above, each deliberate:

1. **Type parameter, not declaration, for input.** `entflow.New[In](name)` returns `*Flow[In]`; step closure signatures are checked against `In` at compile time. There is no `entflow.Input(...)` builder — the earlier annotation-based design needed one because annotations are data; the schema-method design has code, and code states its types. The input type's only framework obligation is serializability (§3.6).
2. **Step bodies inline.** Business logic is not deferred to a separate `steps/` package or a generated interface in another package. The schema file is the program. Ent already establishes this pattern — `Hooks()` and `Policy()` return closures; `field.Validate` takes functions. entflow uses the same channel.
3. **Data/code split along ent's existing seam.** Builder-carried *data* (step names, kinds, dependency edges, retry policies, transition claims, emit topics) is visible to the entc extension at generation time. Closures are *not* serialized; they bind at runtime via `ent/runtime`, exactly as hooks do. One declaration, two consumers.
4. **Schema files import generated packages.** The closures reference `*ent.Tx`, `*ent.Order`, `order.StatusCancelled`. This is legitimate and is precisely why `ent/runtime` exists as a separate package (cycle-breaking). Bootstrap is the standard two-pass flow ent users know from hooks: generate once with stub/absent flows, then flesh out.

### 3.2 Step kinds

Steps come in three kinds with different safety rules, distinguished in the declaration and enforced by closure signature:

**DB steps** (`Step`, `CreateSelf`, `UpdateSelf`, `Create`, `Update`, `Query`, `Check`) receive `*ent.Tx`. Each DB step executes inside a transaction, and the run row's progress pointer advances **in the same transaction** — step effect and progress record commit atomically, so a crash anywhere re-runs the step cleanly. All mutations go through the tx client: entity hooks, transition enforcement, and privacy policies fire normally. `Self` variants bind to the owning entity and type their results as that entity without a string reference.

**Activities** (`Activity`) are external calls (Stripe, email, third-party APIs). Their closures pointedly do **not** receive `*ent.Tx` — the type system makes it impossible to write DB state from inside an external call. Execution is a three-beat dance (§5.3) with framework-supplied idempotency keys. Activities carry retry policies and their failures fail the run (into a retryable/inspectable state).

**Emits** (`Emit`) are fire-and-forget fan-out: an outbox row written in the same transaction as the preceding DB step, delivered post-commit by the relay. The workflow never reads the result. Rule of thumb baked into the design: *consequential* external effects (workflow needs the answer, failure should fail the run) are Activities; *notification* effects are Emits.

### 3.3 Dependencies and conditions

`entflow.After("step")` declares ordering edges; the extension validates the graph is a DAG at codegen. `entflow.When(...)` guards conditional steps; `entflow.SelfWas("paid")` is the canonical predicate (the owning entity's status before the flow's own transition). For self flows, the entity threads positionally through closures, covering the common case; the long tail of cross-step results uses a typed getter (`entflow.Result[T](ctx, "step")`) — an accepted ergonomic compromise versus the earlier generated-deps-struct design, traded for having bodies in the schema.

### 3.4 State-machine cross-validation

Status fields declare their legal transitions as an annotation:

```go
field.Enum("status").
    Values("draft", "pending", "paid", "shipped", "delivered", "cancelled").
    Annotations(entflow.Transitions(map[string][]string{
        "draft":   {"pending", "cancelled"},
        "pending": {"paid", "cancelled"},
        "paid":    {"shipped", "cancelled"},
        "shipped": {"delivered"},
    })),
```

The extension generates the enforcing hook (rejects any status change not in the map — no `SkipHook` escape). Because flows live on the same schema, codegen cross-validates in both directions: every `entflow.Transition("x")` claimed by a step must be a legal edge in the map, and every edge in the map should be claimed by some flow step — orphan and undeclared transitions are generation errors/warnings. The status enum, its legal moves, and the processes that make those moves are provably consistent in one file. This is the flagship feature; no comparable framework has it.

A related generated privacy rule, `DenyStatusEscalation`, allows privileged transitions (e.g. → `paid`, → `shipped`) only when the context carries a workflow marker that only the generated runner sets. Combined with a viewer-required rule, both halves of access control are structural rather than conventional.

### 3.5 Runs are rows

For each flow, the extension **injects** a run entity (precedent: entproto schema injection) — e.g. `CancelOrderFlowRun` — with a standard mixin: id, state, serialized input, current step, attempt counter, last error, timestamps, and an edge to the produced/affected aggregate row for lineage (`order.QueryFlowRuns()`).

Consequences, all intentional:

- The run's `state` field is itself a status enum whose transitions annotation is generated **from the flow's step graph** — entflow enforces workflow-state legality with its own machinery (dogfooding).
- Runs are privacy-governed: who may view, cancel, or retry a run is a `Policy()` on the run entity. A customer-facing "where's my order" status endpoint and an ops "failed refunds this week" dashboard are ent queries, not bespoke code. Retry is a state transition an admin is permitted to make.
- Durability falls out of persistence: a run row mid-flight after a crash is resumable by any worker.

### 3.6 Input codec

The run row persists the input, so `In` must satisfy `entflow.Codec[In]` (marshal/unmarshal). entflow **must not** require `proto.Message`. A JSON codec ships in core; a proto codec ships as a trivial adapter (in entconnect or a ~10-line `entflowproto` shim). Rationale: inputs may originate from RPC, cron, CLI, tests, or another flow's outbox event — flow-chains-flow saga composition only works if inputs aren't married to RPC messages.

### 3.7 Dependency injection

Schema files cannot construct a Stripe client. `entflow.Provide(registry, client)` at app startup + `entflow.Use[T](ctx)` in activity bodies is the typed registry. This is the honest boundary of the design: the schema declares *what happens*; `main.go` supplies *handles to the outside world*. It also makes flows testable without modification (provide a mock client).

## 4. Architecture

Three deliverables, separable:

1. **Runtime library** (`entflow`): flow/step builders, `Attempt`, idempotency keys, `Codec`, DI registry, retry policies, result access. No codegen dependency — a reflection-based v0 can run flows with ugly ergonomics.
2. **entc extension** (`entflow/entc`): modeled on entgql/entproto; uses only sanctioned extension surface. Responsibilities: run-entity injection, transitions-hook generation, flow-graph validation (DAG, transition cross-check, entity references resolve), worker and runner code generation, per-step OpenTelemetry span wiring (`workflow.CancelOrder.refund`), `Describe()`/dry-run output, optional mermaid diagram emission.
3. **Worker**: see §5. Should optionally sit on [River](https://riverqueue.com) as the queue backend rather than compete with it; the built-in poller is the zero-dependency default.

Known ecosystem risk, priced in: ent's extension API is stable but under-documented (entgql source is the real documentation), and ent's center of gravity has shifted toward Atlas — entflow would likely be the most active project in the contrib ecosystem, which is a maintenance commitment and a mindshare opportunity simultaneously.

## 5. Runtime Semantics

### 5.1 Starting a run

`flow.Start(ctx, in)` inserts a run row in `state=pending` and returns the run handle. Callers include: generated transport handlers (entconnect's job), cron triggers, CLI, tests, and outbox consumers (flow chaining). The generated `Exec`/runner is the only component that opens flow transactions and it refuses a context without a viewer — the privileged path is unreachable, not merely linted against.

### 5.2 Worker loop

Claim: `SELECT ... WHERE state IN (claimable) FOR UPDATE SKIP LOCKED` — the database is the queue; multiple workers need no coordination service. For a DB step: open tx → execute closure → advance run state **in the same tx** → commit. For terminal success: `state=done`, response recorded. Failures move to `failed:<step>` with the error recorded after retries exhaust.

### 5.3 Activity execution (the exactly-once-outcome protocol)

Three beats: **(a)** small tx: increment attempt, stamp the deterministic idempotency key (`runID:stepName:attempt` — a pure function of the run row), commit. **(b)** execute the closure with *no transaction open*. **(c)** small tx: persist the JSON result on the run row, advance state, commit.

Crash between (b) and (c) — external call succeeded but unrecorded — is handled by re-execution with the *same* idempotency key; a compliant provider (Stripe et al.) returns the original effect. At-least-once execution + idempotent effect = exactly-once outcome. The framework supplies the key precisely because this is the line generated code must never be trusted to invent.

### 5.4 Outbox and relay

`Emit` writes an outbox row in the same tx as its anchor step. The relay (poll → claim → deliver → mark) is mechanically the same loop as the run worker and folds into it; delivery targets are pluggable (NATS JetStream is the expected first-class target). `tx.OnCommit` nudges the relay for latency; polling is the correctness backstop.

### 5.5 Compensation (post-MVP, design reserved)

Single-tx rollback is physics-limited to DB-only flows; once an Activity has run, undo means *more steps*, not abort. Reserved API: `entflow.OnFail("step", ...)` declaring compensating steps, validated against the transitions map like any other step. Not in MVP; the run-row model already leaves failed runs inspectable and manually retryable, which is the honest v0 story.

## 6. Observability

Generated: one span per run, child span per step, named `workflow.<Flow>.<step>`; attempt count, error, and state on span attributes. The run table itself is the audit log and ops surface (queryable, privacy-governed). `Describe()` prints the flow as data (step, kind, deps, transitions claimed) — also serves as dry-run.

## 7. Testing Strategy (release gate, not afterthought)

The worker is the hard 20%; trust is won or lost on crash-resume. Required before any public release: deterministic crash simulation — a harness that kills the worker at every step boundary and every beat of the activity protocol, in a loop, against a real database, asserting: no step effect ever committed without its progress record, no activity effect duplicated (mock provider counts idempotency-key hits), every interrupted run resumes to the same terminal state. Plus: property tests on the transitions cross-validator, and golden-file tests for generated code (entgql precedent).

## 8. MVP Roadmap (dependency order)

1. **v0.1 — Multi core:** runtime builders + DB-step execution in a single transaction (no run row; equivalent to the Ecto.Multi shape). Reflection-based, no codegen. Proves the declaration ergonomics.
2. **v0.2 — Durability:** run-row persistence (handwritten run schema, not yet injected), worker poller, resume-on-crash, crash-simulation harness.
3. **v0.3 — Activities:** attempt protocol, idempotency keys, retry/backoff, DI registry.
4. **v0.4 — Outbox:** Emit steps, relay, NATS target, flow chaining.
5. **v0.5 — Codegen:** entc extension — run-entity injection, transitions hook + cross-validation, typed sugar, spans, Describe. (The flashiest layer is deliberately last; it is the least essential to proving the model.)

## 9. Decoupling Contract with entconnect

- entflow depends on `ent` only. It never imports protobuf, Connect, or descriptor machinery.
- entflow exposes flow **metadata** for external consumers: flow name, owning entity, `In`/`Out` Go types, step topology. entconnect consumes this (via the metadata API or codegen-time schema graph walk) to match RPCs to flows by request type and generate transport handlers that call `flow.Start`.
- The proto `Codec` adapter lives outside entflow core.
- entflow must be fully usable with plain-struct inputs and no contract layer at all.

## 10. Open Questions

1. `ent.Flow` as a name requires either an upstream interface or entflow defining its own interface type returned from `Flows()` and discovered by the extension — the latter is self-contained and assumed for v0; naming (`entflow.Flow`) TBD.
2. Cross-step typed results: revisit generated per-flow results structs once real flows show how often the positional-self shortcut is insufficient.
3. Multi-aggregate sagas: policy decision recorded — a workflow with no owning aggregate is evidence of a missing aggregate (e.g. `Settlement`); entflow will not add a schema-external declaration home. Document this loudly; it will be the most-asked question.
4. Worker deployment topologies: in-process with the API server (default, zero-infra) vs. dedicated worker binary — both must work; generated `main` helpers TBD.
5. River backend: adapter boundary design so the poller and River are interchangeable.
6. Timers/delays (`entflow.Sleep`, scheduled steps) — natural extension of the run row (`wake_at` column); post-MVP.

## Appendix A — Prior art and the argument from convergence

Rails callbacks → service objects/Interactor (callbacks for integrity only — a decade of scars); Ecto refused callbacks entirely and made the workflow a first-class value (`Ecto.Multi`), the direct inspiration for v0.1; Laravel observers + `ShouldDispatchAfterCommit` queued events; Django signals broadly banned for business logic in favor of Celery; event-sourcing frameworks (Axon, Commanded) formalized aggregates-vs-sagas; Temporal/Restate made the workflow runtime durable — entflow's run row is Temporal's insight implemented on the application's own tables, with the state field as an explicit column rather than a persisted instruction pointer; Hasura/Supabase/Convex all converged on generated CRUD + data-layer rules + explicitly separated effectful actions. entflow's two-box summary: **invariants live with the data (hooks, transitions, privacy); sequence lives in an explicit, inspectable artifact (the flow) — and both boxes live in the schema.**
