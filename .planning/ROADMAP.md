# Roadmap: entflow

## Overview

entflow ships in the order the architecture research validated: runtime-first, codegen-last. Every capability through the outbox milestone (Phases 1-4) is expressible entirely by hand — closures bound at runtime via `ent/runtime`, exactly like `Hooks()`/`Policy()` already work — so codegen (Phases 5-6) is never load-bearing, only automation over a system that already works. Phase 1 proves the builder API and DB-step safety rules in a single transaction with no run row. Phase 2 makes that flow durable: a persisted run row, a `FOR UPDATE SKIP LOCKED` worker poller, and crash-resume proven against a real Postgres — this is also where the deterministic crash-simulation harness (the project's release gate) is born, and where runs become an inspectable, privacy-governed ops surface. Phase 3 adds Activities: the three-beat protocol, framework-supplied idempotency, leases, and retry/backoff — extending the crash-simulation harness to every activity beat. Phase 4 adds the transactional outbox and flow chaining, reusing Phase 2's claim-loop shape and extending the harness again to relay delivery. Phases 5 and 6 are the codegen milestone, split in two because research identified the run-entity injection mechanism (a `schemast`-based, disk-mediated two-pass bootstrap with no published precedent at entflow's required fidelity — the injected entity's own transitions annotation must be derived from the flow's step graph) as the single highest-risk unknown in the entire roadmap. Phase 5 proves that mechanism standalone. Phase 6 builds the flagship differentiator (bidirectional transitions cross-validation) and the remaining runner/observability codegen on top of a now-proven injection mechanism.

### Phase Count Rationale

`config.json` sets granularity to `coarse` (typically 3-5 phases), but research (`research/SUMMARY.md`, `research/ARCHITECTURE.md`) argues for splitting the codegen milestone into three sub-phases because its entity-injection mechanism is a genuinely novel, unproven problem — harder than any published ent-extension precedent (enthistory's injected entities are structurally simple append-only logs; entflow's injected `<Flow>Run` entity needs a *self-referential* transitions annotation computed from its own flow's step graph). Splitting this out is architecturally about risk-isolation, not padding: a failed spike here should not contaminate the flagship cross-validator or the runner codegen, and vice versa.

This roadmap lands at **6 phases** — inside the 5-7 range implied by reconciling coarse granularity with the research finding, not mechanically obeying either input:

- **Phases 1-4** stay coarse and follow natural delivery boundaries exactly as research validated (runtime core → durability → activities → outbox), each a coherent, independently-verifiable capability with 5-16 requirements apiece — no artificial splitting of well-trodden, well-precedented work.
- **Phases 5-6** split the codegen milestone in two, not three: Phase 5 isolates only the entity-injection spike (`GEN-01` through `GEN-04` — the from-scratch bootstrap, the DAG/reference validation, and the self-referential transitions-annotation derivation) as its own dedicated phase, because it is the one piece of this roadmap with no working precedent to model. Phase 6 combines transitions-hook generation + cross-validation (`SM-02`-`SM-05`, `GEN-07`) with runner/observability codegen (`GEN-05`, `GEN-06`) and the generated-code test suites (`TEST-03`, `TEST-04`) into one phase, because once the injection mechanism from Phase 5 is proven, this remaining work is template-and-validation-logic over an already-materialized schema — lower risk, and coherent as a single "codegen produces the rest of the generated surface" delivery boundary rather than three thinner phases.

This is one defensible split among a few; the key invariant preserved is that the entity-injection spike is never bundled with either the flagship cross-validator (whose suppression-annotation design is a one-way door per `PITFALLS.md` Pitfall 5) or the lower-risk runner codegen.

## Phases

**Phase Numbering:**
- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [ ] **Phase 1: Runtime Core** - The flow/step builder API and DB-only Multi-style execution work entirely by hand, with zero codegen dependency
- [ ] **Phase 2: Durability** - Runs persist, survive worker crashes on real Postgres, and are queryable/controllable as ordinary privacy-governed ent entities
- [ ] **Phase 3: Activities** - External calls execute safely via the three-beat protocol with framework-supplied idempotency, leases, and retry/backoff
- [ ] **Phase 4: Outbox & Flow Chaining** - Emitted events deliver at-least-once without holding a transaction open, and can start other flows
- [ ] **Phase 5: Codegen — Entity Injection Spike** - The run-entity injection mechanism (the roadmap's single highest-risk unknown) is proven standalone
- [ ] **Phase 6: Codegen — Cross-Validation & Runner Generation** - Transitions cross-validation, generated hooks/privacy, and full runner/observability codegen ship

## Phase Details

### Phase 1: Runtime Core
**Goal**: A developer can declare and execute a DB-only multi-step flow entirely by hand — no run-row persistence, no codegen — proving the core builder API and step-kind safety rules work before anything else is built on top of them.
**Mode:** mvp
**Depends on**: Nothing (first phase)
**Requirements**: CORE-01, CORE-02, CORE-03, CORE-04, CORE-05, CORE-06, CORE-07, CORE-08, CORE-09, CORE-10, CORE-11, CORE-12, SM-01, META-01, META-02
**Success Criteria** (what must be TRUE):
  1. A developer can declare a flow in a schema file via `Flows()` with a typed input (`entflow.New[In]("Name")`), chain DB steps (`UpdateSelf`, `CreateSelf`, `Query`, `Check`, etc.) whose closures receive `*ent.Tx`, and see the flow execute end-to-end inside a single transaction with no run row (the Ecto.Multi-equivalent baseline).
  2. A developer can declare Activity steps whose closures do not receive `*ent.Tx` — passing `*ent.Tx` into an Activity closure is a compile error, not a runtime check.
  3. A developer can register a client at startup with `entflow.Provide` and retrieve it typed inside a step body with `entflow.Use[T](ctx)`, and can supply any Go type as flow input by implementing `entflow.Codec[In]` (a JSON codec ships in core; no protobuf dependency required).
  4. A developer can call `Describe()` on a flow and see its full step topology (name, kind, deps, transition claims, emit topics, retry policy) printed as data without any step closure ever executing; the same topology data is exposed to an external consumer through a dedicated `meta` package that never imports transport or protobuf machinery.
  5. A developer can declare a status field's legal transitions with `entflow.Transitions(map[string][]string{...})` and read a prior step's typed result inside a later step via `entflow.Result[T](ctx, "step")`.
**Plans**: TBD

### Phase 2: Durability
**Goal**: A developer can start a flow as a durably persisted run that survives a worker crash at any step boundary and resumes to the correct terminal state against a real Postgres database, and an operator can inspect and control runs as ordinary, privacy-governed ent entities.
**Mode:** mvp
**Depends on**: Phase 1
**Requirements**: DUR-01, DUR-02, DUR-03, DUR-04, DUR-05, DUR-06, DUR-07, DUR-08, DUR-09, DUR-10, OPS-01, OPS-02, OPS-04, OPS-05, TEST-01, TEST-02
**Success Criteria** (what must be TRUE):
  1. A developer can call `flow.Start(ctx, in)` and see a run row persisted with `state=pending`, serialized input, and a lineage edge to the owning aggregate; multiple workers claim runs concurrently via `SELECT ... FOR UPDATE SKIP LOCKED` with no coordination service, re-claiming at each step boundary rather than holding a lease across the whole flow.
  2. A worker killed mid-flow — verified by a deterministic crash-simulation harness with two explicit tiers (fast in-process logical crash points, plus real subprocess `SIGKILL` runs against a real Postgres) — is resumed by a different worker to the same terminal state, with no step effect ever committed without its progress record in the same transaction.
  3. A run whose steps all succeed reaches `state=done` with the response recorded; a run whose step fails after retries exhaust reaches `failed:<step>`; a developer sees an explicit, documented dialect support matrix (Postgres first-class; MySQL/SQLite status stated) with a clear startup error rather than silent breakage on an unsupported dialect.
  4. An operator can query runs as ordinary ent entities (e.g. "this order's runs") with no bespoke code, governed by a `Policy()` on the run entity, can cancel an in-flight run, and can see each run emit one span with a child span per step named `workflow.<Flow>.<step>` carrying attempt count, error, and state as attributes.
  5. The worker shuts down gracefully without abandoning a claimed run mid-step, and can run either in-process alongside the API server or as a dedicated worker binary.
**Plans**: TBD

### Phase 3: Activities
**Goal**: A developer can declare an Activity step that safely calls an external service with framework-supplied idempotency and retry/backoff, surviving a worker crash at any point in the three-beat protocol without duplicating the external effect — extending the crash-simulation harness to every activity beat.
**Mode:** mvp
**Depends on**: Phase 2
**Requirements**: ACT-01, ACT-02, ACT-03, ACT-04, ACT-05, ACT-06, OPS-03
**Success Criteria** (what must be TRUE):
  1. An Activity closure executes with no transaction open, bracketed by two short transactions (stamp attempt+key, then persist result), and the framework supplies a deterministic idempotency key (`runID:stepName:attempt`) exposed to the closure via `att.IdempotencyKey()`.
  2. A worker killed between the external call and result recording — the crash-simulation harness now exercises all three activity beats, not just step boundaries — is resumed by another worker that reuses the same idempotency key rather than duplicating the effect; a claimed run carries a lease (`claimed_by`/`lease_expires_at`) so a crash mid-Activity doesn't strand it, and expired leases are reclaimable by another worker.
  3. An Activity that fails retries per its declared `Retry(Backoff(maxAttempts, initial, max))` policy; exhausting retries moves the run to `failed:<step>` with the error recorded.
  4. An operator can manually retry a failed run as a permitted state transition, and a developer can read documentation stating the at-least-once execution contract and the exact boundary of the exactly-once-outcome guarantee, including provider idempotency-key TTL limits.
**Plans**: TBD

### Phase 4: Outbox & Flow Chaining
**Goal**: A developer can emit an event from a flow step that is delivered at-least-once to an external target without ever holding a transaction open across the network call, and can start one flow from another flow's emitted event — extending the crash-simulation harness to relay delivery.
**Mode:** mvp
**Depends on**: Phase 2
**Requirements**: OUT-01, OUT-02, OUT-03, OUT-04, OUT-05
**Success Criteria** (what must be TRUE):
  1. A developer can declare `Emit("topic", After("step"))`, writing an outbox row in the same transaction as its anchor step.
  2. The relay delivers outbox rows using three separate short transactions (claim → deliver → mark), never wrapping the network call inside the claim transaction; a developer can deliver to NATS JetStream as a first-class target behind an interface that admits other targets.
  3. `tx.OnCommit` nudges the relay for low-latency delivery while polling remains the correctness backstop; the crash-simulation harness now covers the relay's claim/deliver/mark boundaries too.
  4. A developer can start one flow from another flow's emitted event (flow chaining) with no infrastructure beyond the shared database.
**Plans**: TBD

### Phase 5: Codegen — Entity Injection Spike
**Goal**: A developer's from-scratch ent project — with no `<Flow>Run` entity yet existing anywhere — can add a `Flows()` method and, on the very first `go generate`, get a correctly-shaped run entity injected automatically, proving the single highest-risk, least-precedented mechanism in the roadmap before any other codegen work is built on top of it.
**Mode:** mvp
**Depends on**: Phases 1-4 (automates behavior already proven by hand)
**Requirements**: GEN-01, GEN-02, GEN-03, GEN-04
**Success Criteria** (what must be TRUE):
  1. Running `go generate` on a project with a newly-added `Flows()` method injects a `<Flow>Run` schema entity to disk via a `schemast`-based pre-pass, with no developer ever hand-writing a run schema.
  2. The injected run entity's `state` enum and its transitions annotation are derived automatically from the flow's own step graph, and stay correct when the step graph changes between generate runs.
  3. The empty-project-to-first-`go generate` path — where the generated ent package does not yet exist when `Flows()` is first read — works end-to-end and is covered by a CI-run quickstart test, not discovered later by a confused first-time user.
  4. Codegen validates that a flow's step graph is a DAG and that every entity and step reference resolves, failing generation with an actionable error rather than a panic deep in template internals.
**Plans**: TBD

### Phase 6: Codegen — Cross-Validation & Runner Generation
**Goal**: Building on the proven entity-injection mechanism, a developer gets a generated transitions-enforcing hook with no escape hatch, a bidirectional state-machine cross-validator tuned against false positives from day one, and a fully generated worker/runner with observability wiring and compile-time-equivalent safety on cross-step result references.
**Mode:** mvp
**Depends on**: Phase 5
**Requirements**: SM-02, SM-03, SM-04, SM-05, GEN-05, GEN-06, GEN-07, TEST-03, TEST-04
**Success Criteria** (what must be TRUE):
  1. A step claiming a `Transition("x")` that is not a legal edge in the transitions map fails code generation with an actionable error; a transition declared in the map but claimed by no step produces a generation warning that is suppressible per-edge — never only a coarse global disable.
  2. The generated transitions-enforcing hook rejects any status change not present in the transitions map, with no `SkipHook` escape; a generated `DenyStatusEscalation` privacy rule permits privileged transitions only when the context carries a workflow marker that only the generated runner sets, and the generated runner refuses to run against a context with no viewer.
  3. Codegen produces the worker/runner dispatch code and per-step OpenTelemetry span wiring automatically for codegen-declared flows, matching the hand-written worker's behavior proven in Phases 2-4.
  4. A dangling or type-mismatched `entflow.Result[T](ctx, "step")` reference is caught at generation time as an actionable error, not discovered later as a runtime panic.
  5. Property tests cover the transitions cross-validator's claim-must-be-legal and edge-should-be-claimed behaviors, and golden-file tests cover the generated hook, runner, and span-wiring code output.
**Plans**: TBD

## Progress

**Execution Order:**
Phases execute in numeric order: 1 → 2 → 3 → 4 → 5 → 6

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Runtime Core | 0/TBD | Not started | - |
| 2. Durability | 0/TBD | Not started | - |
| 3. Activities | 0/TBD | Not started | - |
| 4. Outbox & Flow Chaining | 0/TBD | Not started | - |
| 5. Codegen — Entity Injection Spike | 0/TBD | Not started | - |
| 6. Codegen — Cross-Validation & Runner Generation | 0/TBD | Not started | - |
