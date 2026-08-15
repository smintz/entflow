# Requirements: entflow

**Defined:** 2026-08-08
**Core Value:** A multi-step business process declared in the ent schema executes durably — surviving crashes at any point without duplicating external effects or committing a step without its progress record.

## v1 Requirements

Requirements for initial release. Each maps to roadmap phases.

### Core Runtime

- [x] **CORE-01**: Developer can declare a flow inside an ent schema file via a `Flows()` method, with the input type supplied as a type parameter (`entflow.New[In]("Name")`)
- [x] **CORE-02**: Developer can write step bodies as Go closures inline in the flow declaration, with signatures compile-checked against the flow's input type
- [x] **CORE-03**: Developer can declare DB steps (`Step`, `CreateSelf`, `UpdateSelf`, `Create`, `Update`, `Query`, `Check`) whose closures receive `*ent.Tx`, so entity hooks and privacy policies fire on every mutation
- [x] **CORE-04**: Developer can declare Activity steps whose closures do not receive `*ent.Tx`, making a DB write from inside an external call a compile error
- [x] **CORE-05**: Developer can declare step ordering with `After(...)` and conditional execution with `When(...)` / `SelfWas(...)`
- [x] **CORE-06**: Developer can execute a DB-only flow end-to-end inside a single transaction with no run-row persistence (Ecto.Multi-equivalent baseline)
- [x] **CORE-07**: Developer can register external clients at startup with `entflow.Provide(registry, client)` and retrieve them typed inside activity bodies with `entflow.Use[T](ctx)`
- [x] **CORE-08**: Developer can use any Go type as flow input by satisfying `entflow.Codec[In]`, with a JSON codec shipped in core and no protobuf dependency required
- [x] **CORE-09**: Developer can attach a retry policy to an Activity using a small fixed parameter set (`Retry(Backoff(maxAttempts, initial, max))`)
- [x] **CORE-10**: Developer can read a prior step's typed result inside a later step via `entflow.Result[T](ctx, "step")`
- [x] **CORE-11**: The flow builder chain records every codegen-needed fact (step name, kind, dependency edges, transition claims, emit topics, retry policy) as discrete data readable without evaluating any closure body
- [x] **CORE-12**: Developer can print a flow's structure as data with `Describe()` (step, kind, deps, transitions claimed) and use that output as a dry-run

### Durability

- [x] **DUR-01**: Developer can start a run with `flow.Start(ctx, in)`, which persists a run row in `state=pending` and returns a run handle
- [x] **DUR-02**: The run row persists state, serialized input, current step, attempt counter, last error, timestamps, and an edge to the owning aggregate row for lineage
- [x] **DUR-03**: A DB step's effect and the run's progress pointer commit in the same transaction — a step effect never commits without its progress record
- [ ] **DUR-04**: The worker claims runs with `SELECT ... FOR UPDATE SKIP LOCKED`, so multiple workers run concurrently with no coordination service
- [ ] **DUR-05**: A run interrupted by a worker crash at any step boundary is resumed by any worker and reaches the same terminal state
- [x] **DUR-06**: The worker re-claims the run at each step boundary rather than holding a lease across the whole flow
- [ ] **DUR-07**: A run whose steps all succeed reaches `state=done` with the response recorded; a run whose step fails after retries exhaust reaches `failed:<step>` with the error recorded
- [x] **DUR-08**: Developer gets an explicit, documented dialect support matrix (Postgres first-class; MySQL and SQLite status stated) and a clear startup error rather than silent breakage on a dialect that cannot support the claim query
- [ ] **DUR-09**: Developer can run the worker in-process alongside the API server or as a dedicated worker binary
- [ ] **DUR-10**: The worker shuts down gracefully without abandoning a claimed run mid-step

### Activities

- [ ] **ACT-01**: An Activity closure executes with no transaction open, bracketed by two short transactions (stamp attempt and key → execute → persist result and advance)
- [ ] **ACT-02**: The framework supplies a deterministic idempotency key derived as `runID:stepName:attempt`, exposed to the closure via `att.IdempotencyKey()`
- [ ] **ACT-03**: An Activity re-executed after a crash between external call and result recording uses the same idempotency key, so a compliant provider returns the original effect rather than a duplicate
- [ ] **ACT-04**: A claimed run carries a lease (`claimed_by` / `lease_expires_at`) so a worker crash mid-Activity does not strand the run — expired leases are reclaimable by another worker
- [ ] **ACT-05**: Activity failures retry per the declared backoff policy; exhausting retries fails the run into an inspectable state
- [ ] **ACT-06**: Developer can read documentation stating the at-least-once execution contract and the exact boundary of the exactly-once-outcome guarantee (including provider idempotency-key TTL limits)

### Outbox and Emit

- [ ] **OUT-01**: Developer can declare `Emit("topic", After("step"))`, writing an outbox row in the same transaction as its anchor step
- [ ] **OUT-02**: The relay delivers outbox rows post-commit using three separate short transactions (claim → deliver → mark), never wrapping the network call inside the claim transaction
- [ ] **OUT-03**: Developer can deliver emitted events to NATS JetStream as a first-class target, behind an interface that admits other targets
- [ ] **OUT-04**: `tx.OnCommit` nudges the relay for delivery latency while polling remains the correctness backstop
- [ ] **OUT-05**: Developer can start one flow from another flow's emitted event (flow chaining)

### Operations and Observability

- [ ] **OPS-01**: Operator can query runs as ordinary ent entities (e.g. "failed refunds this week", "this order's runs") with no bespoke code
- [ ] **OPS-02**: Who may view, cancel, or retry a run is governed by a `Policy()` on the run entity
- [ ] **OPS-03**: Operator can manually retry a failed run as a permitted state transition
- [ ] **OPS-04**: Operator can cancel an in-flight run
- [ ] **OPS-05**: Each run emits one span with a child span per step named `workflow.<Flow>.<step>`, carrying attempt count, error, and state as attributes

### State Machine Cross-Validation

- [x] **SM-01**: Developer can declare a status field's legal transitions with `entflow.Transitions(map[string][]string{...})`
- [ ] **SM-02**: Codegen produces a hook that rejects any status change not present in the transitions map, with no `SkipHook` escape
- [ ] **SM-03**: A step claiming `Transition("x")` that is not a legal edge in the map fails code generation with an actionable error
- [ ] **SM-04**: A transition declared in the map but claimed by no step produces a generation warning that is suppressible per-edge (never only a coarse global disable)
- [ ] **SM-05**: Codegen produces a `DenyStatusEscalation` privacy rule that permits privileged transitions only when the context carries a workflow marker that only the generated runner sets

### Code Generation

- [ ] **GEN-01**: Codegen injects a `<Flow>Run` entity per flow into the schema, so developers never hand-write a run schema
- [ ] **GEN-02**: The injected run entity's `state` enum and its transitions annotation are derived from the flow's own step graph
- [ ] **GEN-03**: Codegen reads `Flows()` builder data on a from-scratch project where the generated package does not yet exist — the empty-project-to-first-`go generate` path works and is covered in CI
- [ ] **GEN-04**: Codegen validates that the step graph is a DAG and that all entity and step references resolve, failing generation with an actionable error
- [ ] **GEN-05**: Codegen produces the worker/runner dispatch code and the per-step OpenTelemetry span wiring
- [ ] **GEN-06**: Codegen validates `entflow.Result[T](ctx, "step")` string references (step exists, type matches), turning a runtime panic into a generation error
- [ ] **GEN-07**: The generated runner refuses a context without a viewer, making the privileged transition path structurally unreachable

### Testing and Release Gate

- [ ] **TEST-01**: A crash-simulation harness kills the worker at every step boundary and every Activity beat against a real Postgres, asserting no step effect committed without its progress record, no duplicated Activity effect, and identical terminal state on resume
- [ ] **TEST-02**: The harness has two explicit tiers — fast in-process logical crash points for matrix coverage, plus real subprocess SIGKILL runs — and the release gate names which tiers must pass
- [ ] **TEST-03**: Property tests cover the transitions cross-validator
- [ ] **TEST-04**: Golden-file tests cover generated code output

### Metadata Contract

- [x] **META-01**: External consumers (entconnect) can read flow metadata — flow name, owning entity, `In`/`Out` Go types, step topology — through a dedicated package
- [x] **META-02**: entflow's production dependency graph contains `ent` and no transport, protobuf, or descriptor machinery — verified by an automated check

## v2 Requirements

Deferred to future release. Tracked but not in the current roadmap.

### Timers

- **TIME-01**: Developer can declare a durable sleep or delayed step (`entflow.Sleep`) backed by a `wake_at` column and a claim-query filter

  > Research flag: every full-featured competitor treats durable sleep as core, and it is additive rather than a redesign. Recommended as the **first fast-follow after v1 ships**, not an indefinite deferral. Kept out of v1 because the design document explicitly scoped it post-MVP.

### Developer Experience

- **DX-01**: Developer can run a flow synchronously against a test database with mocked DI via an `entflowtest` package (ordinary unit-test ergonomics, distinct from the crash-simulation release gate)
- **DX-02**: Developer can emit a mermaid diagram of a flow from `Describe()` data
- **DX-03**: Developer gets generated per-flow typed result structs replacing the stringly-typed `Result[T](ctx, "step")` escape hatch

### Scale and Backends

- **SCAL-01**: Developer can swap the built-in poller for River behind a backend adapter interface
- **SCAL-02**: Operator can bulk-replay or bulk-cancel runs

## Out of Scope

Explicitly excluded. Documented to prevent scope creep.

| Feature | Reason |
|---------|--------|
| Compensation / saga rollback (`entflow.OnFail`) | Once an Activity has run, undo means more forward steps, not abort. API reserved but unimplemented; "failed runs stay inspectable and manually retryable" is a complete interim story, and research confirms compensation is decoupled from every table-stakes ops feature |
| Transport binding (protobuf, Connect, gRPC, HTTP) | Belongs to the sibling project entconnect; entflow imports `ent` only (see META-02) |
| Proto `Codec` adapter | Lives outside entflow core — in entconnect or a ~10-line `entflowproto` shim |
| Schema-external flow declaration for multi-aggregate sagas | A workflow with no owning aggregate is evidence of a missing aggregate (e.g. `Settlement`). Recorded policy decision — document loudly, expect it to be the most-asked question |
| Separate coordination server / control plane | Contradicts the core positioning: no infrastructure beyond the database ent already uses. `FOR UPDATE SKIP LOCKED` against the existing DB is sufficient for the target scale |
| Workflow DSL (YAML/JSON state-machine definition) | Reintroduces a second artifact to keep in sync with the schema, undermining "one file answers what happens." Code states its types |
| Visual workflow builder / low-code canvas | Second source of truth that drifts from code; wrong target user. `Describe()` + mermaid emission generates visuals *from* the code instead |
| Event-sourced replay / persisted instruction pointer (Temporal model) | Imports Temporal's determinism-constraint footguns without its server-side tooling. State as an explicit column gives resumability without replay-determinism rules |
| Retry policy DSL with unlimited knobs | Over-parameterized retry APIs look powerful and go unused; a fixed small parameter set covers the overwhelming majority of real cases |
| Cross-language / cross-runtime workflow execution | Structurally incompatible — step bodies are Go closures and closures do not serialize. A permanent boundary, not a deferred feature. Cross-service reaction is what Emit + flow chaining already solves |
| Competing with River as a queue | River is mature and focused; the right relationship is optional backend behind an adapter (SCAL-01), never a reimplementation |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| CORE-01 | Phase 1 | Complete |
| CORE-02 | Phase 1 | Complete |
| CORE-03 | Phase 1 | Complete |
| CORE-04 | Phase 1 | Complete |
| CORE-05 | Phase 1 | Complete |
| CORE-06 | Phase 1 | Complete |
| CORE-07 | Phase 1 | Complete |
| CORE-08 | Phase 1 | Complete |
| CORE-09 | Phase 1 | Complete |
| CORE-10 | Phase 1 | Complete |
| CORE-11 | Phase 1 | Complete |
| CORE-12 | Phase 1 | Complete |
| SM-01 | Phase 1 | Complete |
| META-01 | Phase 1 | Complete |
| META-02 | Phase 1 | Complete |
| DUR-01 | Phase 2 | Complete |
| DUR-02 | Phase 2 | Complete |
| DUR-03 | Phase 2 | Complete |
| DUR-04 | Phase 2 | Pending |
| DUR-05 | Phase 2 | Pending |
| DUR-06 | Phase 2 | Complete |
| DUR-07 | Phase 2 | Pending |
| DUR-08 | Phase 2 | Complete |
| DUR-09 | Phase 2 | Pending |
| DUR-10 | Phase 2 | Pending |
| OPS-01 | Phase 2 | Pending |
| OPS-02 | Phase 2 | Pending |
| OPS-04 | Phase 2 | Pending |
| OPS-05 | Phase 2 | Pending |
| TEST-01 | Phase 2 | Pending |
| TEST-02 | Phase 2 | Pending |
| ACT-01 | Phase 3 | Pending |
| ACT-02 | Phase 3 | Pending |
| ACT-03 | Phase 3 | Pending |
| ACT-04 | Phase 3 | Pending |
| ACT-05 | Phase 3 | Pending |
| ACT-06 | Phase 3 | Pending |
| OPS-03 | Phase 3 | Pending |
| OUT-01 | Phase 4 | Pending |
| OUT-02 | Phase 4 | Pending |
| OUT-03 | Phase 4 | Pending |
| OUT-04 | Phase 4 | Pending |
| OUT-05 | Phase 4 | Pending |
| GEN-01 | Phase 5 | Pending |
| GEN-02 | Phase 5 | Pending |
| GEN-03 | Phase 5 | Pending |
| GEN-04 | Phase 5 | Pending |
| SM-02 | Phase 6 | Pending |
| SM-03 | Phase 6 | Pending |
| SM-04 | Phase 6 | Pending |
| SM-05 | Phase 6 | Pending |
| GEN-05 | Phase 6 | Pending |
| GEN-06 | Phase 6 | Pending |
| GEN-07 | Phase 6 | Pending |
| TEST-03 | Phase 6 | Pending |
| TEST-04 | Phase 6 | Pending |

**Coverage:**

- v1 requirements: 56 total
- Mapped to phases: 56
- Unmapped: 0 ✓

---
*Requirements defined: 2026-08-08*
*Last updated: 2026-08-08 after roadmap creation*
