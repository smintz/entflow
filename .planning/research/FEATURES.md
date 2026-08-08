# Feature Research

**Domain:** Durable workflow orchestration — embedded, database-backed workflow library for Go (ent schema extension)
**Researched:** 2026-08-08
**Confidence:** MEDIUM (broad ecosystem convergence across independent competitors gives HIGH confidence on *what the category expects*; specific claims about any single product's current feature set are LOW-confidence websearch snapshots and should be spot-checked against official docs before being quoted as fact)

## Landscape Surveyed

- **Server-orchestrated durable execution:** Temporal (Go SDK), Cadence (Temporal's predecessor, same model), Restate
- **Database-native durable execution (entflow's direct peer group):** DBOS Transact (Python/TS/Java/**Go**, Postgres-backed), Windmill (Postgres-only, no Redis)
- **Edge/serverless durable execution:** Inngest (steps + sleep on existing compute, no infra to run)
- **Go-native job/task queues (not full workflow engines):** riverqueue/river (Postgres), asynq (Redis); machinery and gue exist but are lower-signal/less maintained and were not separately deep-dived — both are conceptually subsumed by river/asynq's feature set
- **Managed state-machine orchestrators:** AWS Step Functions, Netflix Conductor
- **In-process/in-transaction composition (no durability):** Ecto.Multi (Elixir — entflow's acknowledged v0.1 inspiration), Laravel job batching/chaining (`Bus::batch`), Celery Canvas (chain/group/chord)
- **Saga/compensation frameworks:** Eventuate Tram Sagas (Java/Spring, outbox-based), Axon Framework (CQRS/event-sourcing sagas)

## Feature Landscape

### Table Stakes (Users Expect These)

Features users will not adopt a workflow engine without. Missing any of these makes the product feel unfinished or untrustworthy, regardless of how novel the differentiators are.

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Crash-resumable execution | The entire category's reason to exist — Temporal, DBOS, Restate, Windmill, Inngest, River all lead marketing with "survives crashes/restarts." A workflow engine that loses progress on a worker crash is not a workflow engine. | HIGH | entflow's core bet (`ACTIVE`: progress pointer + step effect commit atomically). Correctly the release gate per entflow.md §7. |
| Automatic retries with configurable backoff | Universal across every competitor surveyed — River, asynq, Temporal, Inngest, Conductor, Step Functions all ship default + configurable retry/backoff out of the box. Users expect exponential backoff with a max-attempts ceiling, not "roll your own." | MEDIUM | entflow has this on Activities (`ACTIVE`). River's default is a reasonable reference point: `attempts^4 ± jitter`, max 25 attempts. |
| Idempotent-effect guarantee (framework-supplied idempotency key) | DBOS, Temporal, Conductor, AWS all converge on the same pattern: at-least-once execution + idempotent effect = "exactly-once outcome." Every serious competitor either supplies the key or documents in detail how the caller must. Leaving this to generated/user code is a known failure mode. | MEDIUM | entflow already commits to framework-supplied deterministic keys (`runID:stepName:attempt`) — matches the strongest form of the industry pattern (DBOS's transactional co-location) *and* the weaker external-API form (Stripe-style key passthrough) simultaneously, because DB steps get transactional exactly-once and Activities get the key-passthrough form. This dual-mode coverage is unusual and worth stating explicitly as a strength. |
| Durable timers / sleep | Every full workflow engine surveyed (Temporal `workflow.Sleep`, Restate timers, Inngest `step.sleep`, Step Functions' up-to-a-year wait) treats sleep/delay as core, not optional — it's what separates a "workflow" from a "retry loop." Currently in entflow's **Out of Scope**. | MEDIUM (a `wake_at` column + claim-query change; low conceptually, but touches the worker's core claim/poll loop and needs its own crash-safety proof) | This is the single biggest gap between entflow's Out of Scope list and what the category treats as non-negotiable. See "Pressure-testing Out of Scope" below — recommend re-classifying as a fast-follow, not indefinitely deferred. |
| Run/job inspection UI or queryable surface | Every competitor treats "see what happened to a specific run" as baseline: River dashboard, asynq's asynqmon, Temporal Web UI, Inngest's step timeline, Conductor's UI. Users installing a workflow library immediately ask "how do I see failed runs." | LOW–MEDIUM (already falls out of entflow's design) | entflow gets this "for free" as a consequence of runs being privacy-governed ent rows — no bespoke dashboard code, just ent queries. This is a genuine structural advantage: competitors had to *build* a UI; entflow's users write an ent query or reuse any ent-based admin tool. |
| Manual retry / cancel of a failed run | Universal ops primitive — River (retry/cancel/delete from dashboard), Temporal (workflow reset via CLI), Inngest (bulk replay from UI), Conductor. Users need to un-stick a stuck run without redeploying code. | LOW (already designed in) | entflow already frames "admin retry" as a permitted state transition via `Policy()` — consistent with the field's convergence on "retry is a normal state transition," not a special escape hatch. |
| At-least-once activity execution semantics, clearly documented | Every mature engine is explicit that Activities/steps run at-least-once and the *user's job* is idempotency of side effects the framework cannot make idempotent for them (e.g. calling a non-idempotent third-party API). Users need this contract stated, not implied. | LOW (documentation + `Attempt.IdempotencyKey()` surface) | entflow already exposes `att.IdempotencyKey()` in the Activity closure signature — matches Temporal/DBOS's guidance to "pass a stable key to every attempt." |
| Observability / tracing per step | OpenTelemetry or equivalent spans per step is standard in modern engines (Temporal, Inngest, Restate all integrate tracing). Users debugging a multi-service flow expect to correlate a workflow run with the rest of their trace graph. | LOW–MEDIUM | entflow has this in `ACTIVE` (`workflow.<Flow>.<step>` spans with attempt/error/state attributes) — correctly scoped as table stakes, not a differentiator. |
| Local/offline testability without standing up infrastructure | Temporal ships `TestWorkflowEnvironment` with time-skipping timers and mockable Activities; Inngest ships a full local dev server (`inngest dev`) that mirrors cloud behavior; DBOS/River just require a local Postgres. Go library users in particular expect `go test` to exercise real logic without network calls. | MEDIUM–HIGH | entflow's crash-simulation harness (kill-the-worker-at-every-boundary against a real DB) is *stronger* than most competitors' default test story, but entflow should also ensure ordinary unit-level flow tests (no crash injection, just "does step 3 run after step 2 with mocked DI") are easy — this is different from the release-gate harness and currently under-specified. |

### Differentiators (Competitive Advantage)

Features where entflow's schema-native, state-machine-cross-validated approach is genuinely novel — not present, or present only in much heavier form, in any competitor surveyed.

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| State-machine cross-validation (transitions ↔ steps, both directions) at codegen time | No competitor surveyed does this. Temporal/DBOS/Restate treat "what state can this entity legally be in" as application code the workflow engine has no opinion on. Conductor/Step Functions have state machines but for the *workflow's own* control flow, not for validating against an independently-declared *entity* status enum. entflow catches "you wrote a step that claims a transition your schema doesn't allow" and "you declared a transition no step ever performs" as build-time errors instead of runtime surprises. | HIGH (already `ACTIVE`) | This is correctly entflow.md's self-described "flagship feature" — the research confirms no direct analog exists. Worth leading with in any positioning copy. |
| Zero-infrastructure durability on the app's existing database | DBOS is the only true peer (also Postgres-backed, also a library not a server), and DBOS is the strongest validation that this category is real and growing (added a Go SDK in 2026). Windmill is Postgres-only but still requires running a server process. Temporal/Restate/Conductor all require a separate server/cluster. River/asynq are Postgres/Redis-backed but are job queues, not multi-step orchestrators with state-machine semantics. | HIGH (already `ACTIVE`) | entflow's "Temporal-lite for ent" positioning is accurate and defensible — the closest thing to unclaimed territory is "DBOS's model, but wired into ent's schema/hooks/privacy stack specifically," which is a real gap DBOS does not fill for ent users. |
| Compile-time-typed step wiring + structural DB/external-call separation | Temporal's Go SDK enforces determinism conventions (workflow code must not do I/O directly) but does so by *documentation and workflow-sandboxing at runtime*, not by the Go type system. entflow's three step kinds (distinct closure signatures — `*ent.Tx` present or absent) make the DB/external-call boundary a compile error, not a runtime sandbox violation. | MEDIUM (already `ACTIVE`) | Directly serves the "vibe-coding guardrail" positioning — this is a Go-specific, type-system-native answer to a problem every other engine solves with runtime instrumentation or developer discipline. |
| Runs as privacy-governed, queryable ent rows (dashboard-for-free) | Competitors had to build bespoke UIs (Temporal Web, asynqmon, River UI, Inngest dashboard) as separate products/subsystems. entflow's runs inherit ent's existing privacy/query machinery, so "who can see this run" and "how do I list failed refunds this week" are ordinary ent code, reusable with any ent-based admin tool (including auto-generated ones). | MEDIUM (falls out of `ACTIVE` run-row design) | This should be marketed explicitly — "you already have an ops dashboard, you just didn't know it" is a stronger pitch than "we also have a dashboard." |
| Schema as single source of truth for "what happens," not just "what is" | Rails/Laravel/Django/Ecto's collective retreat from model callbacks (documented in entflow.md's Appendix A) shows the industry converged on separating invariants from sequence — but no ent-adjacent (or comparable ORM-adjacent) framework has closed the loop by putting *both* in the same reviewable file with cross-validation between them. Django+Celery, Rails+service objects, Laravel+jobs all still require a human to manually keep the state machine and the process code in sync. | N/A (positioning, not a feature per se) | This is the thesis-level differentiator; every concrete feature above is downstream of it. |

### Anti-Features (Commonly Requested, Often Problematic)

Things that look attractive by analogy to competitors but would compromise entflow's zero-infrastructure, library-not-server positioning, or duplicate complexity the ent ecosystem already solves elsewhere.

| Feature | Why Requested | Why Problematic | Alternative |
|---------|---------------|------------------|-------------|
| Separate coordination server / control plane | "Temporal has one, Restate has one, Conductor has one — surely we need one for scale/multi-region?" | Directly contradicts the core positioning ("no infrastructure beyond the database ent already uses"). A server means a new deployable, a new SPOF, a new thing to operate — exactly what DBOS and entflow both exist to avoid. Every server-based competitor pays an onboarding tax entflow's target users are explicitly trying to escape. | `FOR UPDATE SKIP LOCKED` claim against the existing DB is sufficient for the target scale (single-app, ent-backed teams); document this as a permanent architectural boundary, not a v1 limitation. |
| A workflow DSL (YAML/JSON state-machine definition, Amazon States Language-style) | Step Functions, Conductor, and many "visual workflow builder" tools use a declarative DSL, and it looks appealing for "non-engineers can edit workflows" or cross-language portability. | Fights entflow's central thesis directly: code states its types (per entflow.md §3.1's own rationale for rejecting annotation-based `Input(...)`). A DSL reintroduces a second artifact to keep in sync with the schema, undermining "one file answers what happens." It also reintroduces the exact annotation-vs-code tension entflow explicitly designed away from. | Typed Go closures in `Flows()`, as already designed. `Describe()` already gives a data-shaped view of the flow for tooling/diagrams without needing a DSL as the *source* of truth. |
| Persisted instruction pointer / full event-sourced replay (Temporal-style event history + deterministic replay) | Temporal's model is the most famous in the category, so it's a natural instinct to copy the replay-on-crash mechanism wholesale. | Determinism constraints (no direct time/random/I-O calls in workflow code, replay-safety rules) are Temporal's single biggest source of user confusion and footguns (see Windmill's positioning against exactly this). entflow's explicit design choice — state as an explicit column, not a replayed event log — avoids this entire failure class. Re-adding event-sourced replay would import Temporal's complexity without its server-side tooling to manage it. | Run row with an explicit `state` enum + progress pointer, as already designed — resumability without replay-determinism rules. |
| Compensation/saga rollback as a v1 requirement | Eventuate Tram and Axon both center their entire framework on saga compensation, and "sagas" is literally in entflow's own positioning language (payment flows, order lifecycles). | Once an Activity has executed an external effect, "rollback" is fiction — it's actually a new forward step (a refund is not an un-charge). Building generic compensation orchestration (Eventuate/Axon-scale complexity: correlation properties, separate compensating event handlers) before proving the core durability model is solid is exactly the scope-creep that would delay the harder, load-bearing 20% (crash-resume). | Ship with `entflow.OnFail` reserved but unimplemented, as already decided — "failed runs stay inspectable and manually retryable" is a defensible, honestly-stated interim story that every competitor implicitly relies on too (see Idempotency/Observability rows above — manual retry is table stakes everywhere, compensation is not). |
| A visual workflow builder / low-code canvas | Windmill, Step Functions, Conductor, n8n-style tools all have some visual layer, and it's a common "wouldn't it be nice" ask once a text-based DSL exists. | Directly conflicts with the target user (Go teams writing ent schemas, not low-code users) and the "schema is the constitution" philosophy — a visual canvas is a second source of truth that inevitably drifts from the code. Building and maintaining a UI canvas is also a massive scope commitment for a library-first, single(-ish)-maintainer project. | `Describe()` + optional mermaid diagram emission (already scoped in entflow.md §4) gives visual insight *generated from* the code, never edited independently of it. |
| Cross-language / cross-runtime workflow execution | DBOS markets multi-language workflow interop (TS/Python/Java/Go/PL-pgSQL) as a 2026 feature; competitors positioning against Temporal often cite polyglot support as a selling point. | entflow is structurally an ent/Go-native tool — the moment a flow needs to be invoked from or resume in another language runtime, the entire "step bodies are typed Go closures bound via `ent/runtime`" model breaks, because closures don't serialize. This isn't a v2 feature to defer, it's actually incompatible with the core architecture. | If cross-service invocation is needed, that's what Emit (outbox) + flow chaining already solves at the event boundary — other services react to `order.cancelled`, they don't reach into entflow's closures. |
| Configurable retry policy DSL with unlimited knobs (per-error-type backoff curves, custom jitter functions, retry budgets across a whole run, etc.) | Temporal's `RetryPolicy` exposes many knobs (initial interval, backoff coefficient, max interval, max attempts, non-retryable error types) and it's tempting to match feature-for-feature. | Real-world usage across the surveyed competitors converges on a small number of parameters that cover the overwhelming majority of cases: max attempts + backoff shape (River's `attempts^4 ± jitter` default, Temporal's exponential-with-cap). Over-parameterizing retry policy is a classic case of API surface that looks powerful in a table of options and is barely used in practice — most teams pick the default and move on. | Ship `entflow.Retry(entflow.Backoff(maxAttempts, initial, max))` as already designed (entflow.md §3.1 example) — a fixed, small parameter set covering exponential backoff with a ceiling. Add non-retryable-error classification only if real usage demands it; don't pre-build a policy DSL. |

## Feature Dependencies

```
Crash-resumable execution (run-row persistence + FOR UPDATE SKIP LOCKED claim)
    └──requires──> Progress pointer commits atomically with step effect (DB steps)
                       └──enables──> Manual retry / cancel as a state transition
                       └──enables──> Run inspection UI-for-free (queryable ent rows)

Framework-supplied idempotency keys
    └──requires──> Attempt counter + deterministic key derivation (runID:stepName:attempt)
    └──enables──> Exactly-once-outcome guarantee (the headline durability claim)

State-machine cross-validation (steps <-> transitions annotation)
    └──requires──> Transitions annotation on the status field (existing ent primitive)
    └──requires──> Run `state` enum generated from the flow's own step graph (dogfooding)
    └──enables──> DenyStatusEscalation privacy rule (workflow-marker-gated transitions)

Durable timers/sleep (Out of Scope, flagged for reconsideration)
    └──requires──> wake_at column on the run row
    └──requires──> Claim query changes (WHERE state IN (claimable) AND wake_at <= now())
    └──conflicts-with-nothing──> additive to existing worker loop, not a redesign

Compensation/saga rollback (Out of Scope, correctly deferred)
    └──requires──> OnFail step declarations validated against the transitions map (same machinery as forward steps)
    └──requires──> A model for "which effects are compensable" (Activities only; DB steps roll back for free via tx abort)
    └──enhances──> but is NOT required by: inspectable/retryable failed runs (already sufficient interim story)

Local/offline testability
    └──requires──> DI registry (already ACTIVE) to substitute mocks for real external clients
    └──enhances──> Crash-simulation harness (different concern: ordinary flow tests vs. crash-injection tests)

Visual diagram / Describe() output
    └──requires──> Flow metadata already captured as builder-carried data (step, kind, deps, transitions) — no new data model, purely a rendering step
```

### Dependency Notes

- **Crash-resumable execution requires the atomic progress-pointer commit:** this is the one dependency the whole product is downstream of — every other durability claim (idempotency, manual retry, run inspection) assumes the run row never lies about what actually happened. This must land first and be proven by the crash-simulation harness before anything else is trustworthy.
- **State-machine cross-validation requires the transitions annotation to already exist as an ent primitive:** entflow is extending, not inventing, this — the differentiator is the *cross-validation*, not the annotation itself. This makes it lower-risk to build than it looks, since half the machinery is borrowed from ent's existing `Transitions()` support.
- **Durable timers/sleep is additive, not a redesign:** every competitor treats sleep as core, and entflow's own design doc calls it "a natural extension of the run row." The dependency graph shows why it's cheap relative to its perceived importance — recommend moving it from "Out of Scope" to "first fast-follow after MVP" rather than leaving it in the same bucket as compensation (which genuinely is hard).
- **Compensation/saga rollback is NOT required by anything else on this list:** "failed runs stay inspectable and manually retryable" is a complete, independently-shippable interim story — every table-stakes ops feature (retry, cancel, inspect) works without compensation existing at all. This validates deferring it past v1; it is genuinely decoupled, not merely postponed under pressure.
- **Local/offline testability requires the DI registry, which is already `ACTIVE`:** this dependency is already satisfied by existing design decisions — the gap is ergonomics/documentation (a `entflow/entflowtest` style harness for "run this flow synchronously against a test DB with mocked DI"), not new architecture.
- **Cross-language execution conflicts with the closure-based step body model:** flagged in Anti-Features because unlike other deferred items, this one cannot be added later without breaking the "step bodies are typed Go closures" architecture — it should be documented as a permanent boundary, not a future roadmap item.

## Pressure-Testing the Out-of-Scope List

Read against the competitive landscape, entflow.md's six Out-of-Scope items hold up with one clear exception:

1. **Compensation/saga rollback — defensible.** Every saga-specialist framework surveyed (Eventuate Tram, Axon) is substantially more architecturally invasive (event sourcing, message buses, correlation properties) than anything else in entflow's design. Deferring this is consistent with how the rest of the industry treats it: even Temporal's "first-class Saga support" is a pattern/library convention layered on general-purpose workflow primitives, not a separate guaranteed subsystem. "Inspectable + manually retryable" is a genuinely complete interim story because every table-stakes ops feature is independent of it (see Dependency Notes above).
2. **Timers/delays/scheduled steps — reconsider the timeline, not the decision.** This is the one place research pushes back: every full workflow engine surveyed (Temporal, Restate, Inngest, Step Functions) treats durable sleep as core rather than optional, and it is cheap relative to its importance (additive `wake_at` column + claim-query filter, no architectural redesign — see Feature Dependencies above). Recommend flagging this explicitly in the roadmap as the first fast-follow after MVP durability ships, not lumped in with compensation as "someday."
3. **Transport binding (entconnect) — correct boundary, matches the pattern of every competitor that separates "engine" from "API layer."** No workflow engine surveyed bundles HTTP/gRPC binding into its core durability library; this is universal, not just an entflow choice.
4. **Proto codec adapter — correct, same reasoning as #3.**
5. **Schema-external flow declaration for multi-aggregate sagas — defensible policy stance, and rare among competitors to even take a position on this.** Most competitors (Temporal, DBOS, Restate) are aggregate-agnostic by construction since they aren't tied to an ORM's entity model at all — entflow's "a workflow with no owning aggregate is a missing aggregate" is a genuinely novel opinion enabled by being schema-native, not a gap relative to competitors.
6. **Competing with River as a queue — correct.** River is a mature, focused product; every workflow-layer feature entflow needs (transactional enqueue, `FOR UPDATE SKIP LOCKED`, retry/backoff) is exactly the primitive River already optimizes, which is why treating it as an optional backend behind an adapter (rather than reimplementing it) is the right call and matches how DBOS also treats "durable queues" as a primitive layered on the same DB-native philosophy.

## MVP Definition

### Launch With (v1)

Minimum viable product — what's needed to validate the concept and match table stakes.

- [ ] Crash-resumable execution with atomic progress-pointer commit (DB steps) — the whole product's foundation
- [ ] Framework-supplied idempotency keys for Activities (three-beat protocol) — table stakes, already core to the design
- [ ] Retry with a small, fixed backoff parameter set (max attempts, initial/max interval) — table stakes; resist the urge to over-parameterize (see Anti-Features)
- [ ] Run rows as privacy-governed, queryable ent entities — table-stakes inspection surface, delivered "for free" by design
- [ ] Manual retry/cancel as a permitted state transition — table-stakes ops primitive
- [ ] State-machine cross-validation (transitions ↔ steps, both directions) — the flagship differentiator; must ship in v1 to justify the "schema-native" positioning, not deferred to codegen-later
- [ ] Per-step OpenTelemetry spans — table stakes for any team running this in production
- [ ] `Describe()` / dry-run output — cheap, gives testability and documentation for free
- [ ] Deterministic crash-simulation harness as the release gate — not a "feature" but the trust mechanism every table-stakes claim above depends on

### Add After Validation (v1.x)

Features to add once the core durability model is proven and real flows exist.

- [ ] Durable timers/sleep (`entflow.Sleep`, `wake_at` column) — reclassify from "Out of Scope" to "first fast-follow"; cheap relative to importance, and closes the single biggest gap versus every full-featured competitor
- [ ] `entflowtest` local/offline testing harness (synchronous flow execution against a test DB with mocked DI) — DI registry already exists; this is packaging/ergonomics, triggered by real user friction reports
- [ ] River backend adapter — triggered by a real user needing River's throughput/queue features beyond the built-in poller
- [ ] Generated per-flow typed results structs — triggered once `entflow.Result[T](ctx, "step")` proves insufficient in practice (already flagged as an open question)

### Future Consideration (v2+)

Features to defer until the core model has product-market fit within the ent ecosystem.

- [ ] Compensation/saga rollback (`entflow.OnFail`) — defer until real multi-Activity flows expose concrete compensation needs the "inspectable + manually retryable" story doesn't cover
- [ ] Bulk operations on runs (bulk replay, bulk cancel) — Inngest's bulk-replay UI is a nice-to-have once run volume is high enough to matter; trivially an ent query/mutation once the single-run primitives exist
- [ ] Mermaid diagram emission — cosmetic, cheap, no urgency

## Feature Prioritization Matrix

| Feature | User Value | Implementation Cost | Priority |
|---------|------------|---------------------|----------|
| Crash-resumable execution (atomic progress commit) | HIGH | HIGH | P1 |
| Framework-supplied idempotency keys | HIGH | MEDIUM | P1 |
| Retry/backoff (fixed small parameter set) | HIGH | LOW | P1 |
| State-machine cross-validation | HIGH | HIGH | P1 |
| Run rows as queryable/privacy-governed ent entities | HIGH | LOW (falls out of design) | P1 |
| Manual retry/cancel as state transition | HIGH | LOW | P1 |
| Per-step OpenTelemetry spans | MEDIUM | LOW | P1 |
| Crash-simulation test harness | HIGH (trust mechanism) | HIGH | P1 |
| `Describe()` / dry-run | MEDIUM | LOW | P1 |
| Durable timers/sleep | HIGH | MEDIUM | P2 |
| Local/offline testing harness (`entflowtest`) | MEDIUM | LOW–MEDIUM | P2 |
| River backend adapter | MEDIUM | MEDIUM | P2 |
| Generated per-flow typed results structs | LOW–MEDIUM | MEDIUM | P3 |
| Compensation/saga rollback | MEDIUM (real, but narrow until proven) | HIGH | P3 |
| Bulk run operations (replay/cancel) | LOW (until high volume) | LOW | P3 |
| Mermaid diagram emission | LOW | LOW | P3 |

**Priority key:**
- P1: Must have for launch (matches entflow.md's `Active` requirements almost exactly)
- P2: Should have, add when possible (reclassifies timers up from Out of Scope)
- P3: Nice to have, future consideration (matches entflow.md's `Out of Scope` for the items that should stay deferred)

## Competitor Feature Analysis

| Feature | Temporal (Go SDK) | DBOS Transact | River / asynq | entflow's Approach |
|---------|--------------------|----------------|-----------------|---------------------|
| Infrastructure required | Separate server/cluster (Temporal Service) | None — app's own Postgres | None — app's own Postgres/Redis | None — app's own Postgres (ent's DB); matches DBOS, ahead of Temporal |
| Durability mechanism | Event-sourced history + deterministic replay | Postgres checkpoint per step | Postgres/Redis row per job | Run row with explicit `state` column + atomic progress pointer — no replay-determinism rules, closer to DBOS than Temporal |
| Idempotency | At-least-once Activities; user supplies idempotent logic, aided by workflow-scoped IDs | Workflow-ID-based; transactional exactly-once when step writes to same Postgres | User-managed uniqueness keys (River `UniqueOpts`, asynq unique tasks) | Framework-supplied deterministic key (`runID:stepName:attempt`) for Activities + transactional atomicity for DB steps — combines both patterns |
| Retries | Rich `RetryPolicy` (many knobs: coefficient, max interval, non-retryable types) | Framework-managed, step-level | Exponential backoff, configurable, sane default | Small fixed parameter set (`Backoff(max, initial, max interval)`) — deliberately narrower than Temporal by design (see Anti-Features) |
| Timers/sleep | `workflow.Sleep`/Timer, durable | Available via scheduling primitives | Not applicable (job queue, not workflow) | **Out of Scope in current design — flagged as the one gap worth reconsidering** |
| State-machine validation against entity status | None | None | None | **Unique to entflow** — cross-validates transitions map against step claims at codegen |
| Ops/inspection UI | Temporal Web (engineer-facing, event-history vocabulary) | Conductor UI (DBOS's own product) | River dashboard / asynqmon | Free via ent's existing privacy/query layer — no bespoke UI code needed |
| Compensation/saga | First-class Saga pattern support (library convention on top of general primitives) | Not a primary focus | Not applicable | Reserved API (`OnFail`), deferred past MVP — consistent with "not the primary focus" pattern seen even in Temporal |
| Testing | `TestWorkflowEnvironment` (time-skipping, mockable Activities) | Standard Postgres test DB | Standard Postgres/Redis test instance | Crash-simulation harness (stronger for crash-safety) + DI registry (enables mocking); ordinary fast unit-test ergonomics currently under-specified — flagged as a v1.x gap |
| Language/runtime model | Multi-language SDKs, server is language-agnostic | Multi-language (TS/Python/Java/**Go**/PL-pgSQL), cross-language workflow calls | Go-only (or Redis-only per language) | Go-only, structurally — closures don't serialize, so cross-language is a permanent non-goal, not a gap |

## Sources

- [temporal package - go.temporal.io/sdk/temporal](https://pkg.go.dev/go.temporal.io/sdk/temporal) — LOW confidence (websearch digest)
- [Workflow message passing - Go SDK | Temporal Docs](https://docs.temporal.io/develop/go/workflows/message-passing) — LOW confidence
- [Versioning - Go SDK | Temporal Docs](https://docs.temporal.io/develop/go/workflows/versioning) — LOW confidence
- [DBOS Transact | Open Source Durable Execution Library](https://www.dbos.dev/dbos-transact) — LOW confidence
- [Postgres-backed Durable Workflow Execution | DBOS](https://www.dbos.dev/blog/postgres-is-all-you-need-for-durable-execution) — LOW confidence
- [The Case for Co-Locating Workflow State with Your Data | DBOS](https://www.dbos.dev/blog/co-locating-workflow-state-with-your-data) — LOW confidence
- [DBOS vs Temporal: Choosing Durable Execution in 2026](https://tiarebalbi.com/en/blog/dbos-vs-temporal-postgres-durable-execution) — LOW confidence
- [DBOS Product Enhancements - April 2026](https://www.dbos.dev/blog/dbos-new-features-april-2026) — LOW confidence
- [Key Concepts - Restate](https://docs.restate.dev/foundations/key-concepts) — LOW confidence
- [Building a modern Durable Execution Engine from First Principles | Restate](https://www.restate.dev/blog/building-a-modern-durable-execution-engine-from-first-principles) — LOW confidence
- [GitHub - riverqueue/river](https://github.com/riverqueue/river) — LOW confidence
- [Job retries | River Docs](https://riverqueue.com/docs/job-retries) — LOW confidence
- [Scheduled jobs | River Docs](https://riverqueue.com/docs/scheduled-jobs) — LOW confidence
- [GitHub - hibiken/asynq](https://github.com/hibiken/asynq) — LOW confidence
- [Netflix Conductor: A microservices orchestrator | Netflix TechBlog](https://netflixtechblog.com/netflix-conductor-a-microservices-orchestrator-2e8d4771bf40) — LOW confidence
- [Replacing Netflix Conductor with AWS Step Functions | AWS Blog](https://aws.amazon.com/blogs/migration-and-modernization/replacing-netflix-conductor-with-aws-step-functions-what-we-learned/) — LOW confidence
- [Best Practices — Durable Execution for workflows and agents (Conductor OSS)](https://conductor-oss.github.io/conductor/devguide/bestpractices.html) — LOW confidence
- [Composable transactions with Multi — Ecto v3.12.4](https://hexdocs.pm/ecto/3.12.4/composable-transactions-with-multi.html) — LOW confidence
- [Ecto.Multi — Ecto v3.14.1](https://hexdocs.pm/ecto/Ecto.Multi.html) — LOW confidence
- [Inngest - Durable Execution - Reliable Workflows](https://www.inngest.com/uses/durable-workflows) — LOW confidence
- [Steps in Inngest | Checkpointed, Retriable Units of Work](https://www.inngest.com/docs/learn/inngest-steps) — LOW confidence
- [How a durable workflow engine works: you might not need a queue - Inngest Blog](https://www.inngest.com/blog/how-durable-workflow-engines-work) — LOW confidence
- [GitHub - inngest/inngestgo](https://github.com/inngest/inngestgo) — LOW confidence
- [Narayana team blog: Saga implementations comparison](https://jbossts.blogspot.com/2017/12/saga-implementations-comparison.html) — LOW confidence
- [GitHub - eventuate-tram/eventuate-tram-sagas](https://github.com/eventuate-tram/eventuate-tram-sagas) — LOW confidence
- [Saga with AXON | Medium](https://medium.com/@blogs4devs/saga-with-axon-bb7b9cfe0b64) — LOW confidence
- [Canvas: Designing Work-flows — Celery documentation](https://docs.celeryq.dev/en/stable/userguide/canvas.html) — LOW confidence
- [Harnessing Celery Canvas | Reintech](https://reintech.io/blog/building-complex-workflows-with-celery-canvas) — LOW confidence
- [Idempotency and retries - AWS Durable Execution SDK Developer Guide](https://docs.aws.amazon.com/durable-execution/patterns/best-practices/idempotency/) — LOW confidence
- [Error handling in distributed systems: A guide to resilience patterns | Temporal](https://temporal.io/blog/error-handling-in-distributed-systems) — LOW confidence
- [Durable Workflow Engines in 2026: Every Major Option Compared | Upstash Blog](https://upstash.com/blog/durable-workflow-engines-compared-every-major-option-in-2026) — LOW confidence
- [Inngest vs Temporal: Durable execution that developers love](https://www.inngest.com/compare-to-temporal) — LOW confidence
- [Manage job batches efficiently with Bus::batch in Laravel | Medium](https://medium.com/@harrisrafto/manage-job-batches-efficiently-with-bus-batch-in-laravel-aed0edba7a74) — LOW confidence
- [Job batching in Laravel: How it works - Mohamed Said](https://themsaid.com/queue-job-batching-in-laravel-how-it-works) — LOW confidence
- [Windmill vs Temporal: an honest side-by-side comparison | Windmill](https://www.windmill.dev/compare/temporal) — LOW confidence
- [GitHub - windmill-labs/windmill](https://github.com/windmill-labs/windmill) — LOW confidence
- [testsuite package - go.temporal.io/sdk/testsuite](https://pkg.go.dev/go.temporal.io/sdk/testsuite) — LOW confidence
- Internal: `/Users/smintz/go/src/github.com/smintz/entflow/entflow.md` (design document, pressure-tested against above) — HIGH confidence (primary source, project's own design)
- Internal: `/Users/smintz/go/src/github.com/smintz/entflow/.planning/PROJECT.md` (requirements/scope context) — HIGH confidence (primary source)

**Confidence caveat:** All external competitor claims above are drawn from web search result summaries (LOW confidence tier per the research verification protocol — single-source, unverified, no official-docs deep-read via Context7 or equivalent). The consistent convergence across many independent competitors on the same table-stakes features (retries, idempotency, run inspection, manual retry) raises practical confidence in the *category-level* conclusions even though any individual product-feature claim should be verified against current official docs before being used in external-facing positioning copy or exact API design decisions.

---
*Feature research for: durable workflow orchestration library for Go (embedded, ent-schema-native)*
*Researched: 2026-08-08*
