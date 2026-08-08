# Pitfalls Research

**Domain:** Durable workflow orchestration library for Go, built as an ent (entgo.io) schema extension — database-backed run persistence, worker poller, activity idempotency protocol, transactional outbox
**Researched:** 2026-08-08
**Confidence:** MEDIUM-HIGH (cross-checked web research for general distributed-systems/Go/Postgres patterns [MEDIUM per source_hierarchy]; HIGH-confidence synthesis against entflow's own design document for ent-specific and product-specific pitfalls, which are derived directly from the documented architecture in `entflow.md` rather than external sourcing)

## Critical Pitfalls

### Pitfall 1: Zombie runs from workers that claim without crash-safe status semantics

**What goes wrong:**
`SELECT ... WHERE state IN (claimable) FOR UPDATE SKIP LOCKED` only holds a row lock for the duration of the claiming transaction. If the claim and the "mark as running / stamp owner+lease" write are two separate transactions (claim-then-commit, then a second tx to start work), a worker that crashes between them leaves the row in a state where its earlier row lock is already released — but the run row was never marked in-progress, so it looks claimable and gets double-claimed, OR (opposite bug) it was marked in-progress in that first tx and the worker dies before ever executing the step, so it now looks "owned" forever with no lease to expire, and no other worker will touch it. Postgres's row lock disappears the instant the holding transaction ends (commit, rollback, or connection death) — it does not persist as an application-level ownership marker.

**Why it happens:**
Developers assume `FOR UPDATE SKIP LOCKED` gives them a durable "this job is mine" signal. It only prevents two workers running the claim query *concurrently* from selecting the same row — it says nothing about what happens after the claiming transaction ends. entflow's own design (§5.2) commits the claim and the state transition together, which is directionally correct, but the design document does not yet specify a lease/heartbeat mechanism for runs that get claimed into an in-progress state and then the worker dies mid-step (after committing "claimed" but before finishing the step body).

**How to avoid:**
- Every claim must, in the *same* transaction, either (a) execute the entire DB step and advance progress atomically (matches entflow's committed design for DB steps — this is the safe case), or (b) if claim and execution must be split (true for Activities, which cannot hold a tx open across the external call — see Pitfall 2), stamp a `claimed_by` + `lease_expires_at` on the run row in the claiming transaction, and have the poller's claim predicate include `(state IN (claimable) OR (state = 'running' AND lease_expires_at < now()))`. A worker that dies mid-activity leaves a row that is reclaimable once its lease lapses.
- Never treat "row was lockable" as durable evidence of liveness. Only a written lease timestamp is durable evidence.
- For entflow specifically: since DB steps commit effect+progress atomically, they never produce zombies. Only the three-beat Activity protocol (§5.3) needs a lease, because beat (a) commits "attempt N in progress" before beat (b) (the untransacted external call) runs — a crash after (a) but before (c) leaves a row that looks claimed with no lease.

**Warning signs:**
- Runs sitting in `running`/`in_progress` state with no forward movement and no error, discovered only via manual DB inspection.
- Support/ops reports of "my order is stuck" with no crash-simulation coverage of the claimed-but-orphaned case.
- Crash-simulation harness (per constraint in PROJECT.md) that only kills workers at *step* boundaries, not mid-attempt-protocol beats — this specific failure mode requires killing between beat (a) and (b), and between (a)/committed-claim and the eventual retry poll.

**Phase to address:**
v0.2 (Durability — run-row persistence, worker poller, crash-resume) must define the lease/heartbeat contract before the poller ships. v0.3 (Activities) must extend it to the three-beat protocol, since that's where the split-transaction gap actually lives.

---

### Pitfall 2: The cardinal sin, in a place the design almost avoids — but the escape hatch reintroduces it

**What goes wrong:**
entflow's core architectural decision (DB step effect + progress pointer commit atomically) structurally prevents "transaction held open during a network call" for DB steps and, by construction of the closure signature (no `*ent.Tx` on Activities), for Activities too. This is a genuinely strong design. The risk is not in the happy path — it's in every place a contributor or user is tempted to bypass the separation: a DB step closure that, inside the transaction, calls out to a service to "just check something quickly" (nothing in the type system stops a DB-step closure from making an HTTP call using a captured client, since the DB step closure receives `*ent.Tx` but the closure body is arbitrary Go); a retry/backoff implementation that holds a connection checked out of the pool while sleeping between attempts; or a relay/poller implementation that, to reduce round-trips, wraps "claim + deliver to NATS + mark delivered" in one transaction (this is precisely the emit/relay design's temptation — do NOT synchronously deliver to NATS inside the claiming transaction).

**Why it happens:**
The type system prevents an Activity from accessing `*ent.Tx`, but it does not (and cannot) prevent a DB step from making a network call — the closure just has `*ent.Tx` in its capture list; nothing stops it from also capturing an HTTP client. This is a convention, not a structural guarantee, for the DB-step side specifically. The design doc's "structural, not conventional" claim (Constraints, PROJECT.md) is true for the Tx-vs-no-Tx split on Activities but does not itself prevent a DB step from doing something slow/networked while holding the tx.

**How to avoid:**
- Document, loudly, in the schema-authoring guide: "DB steps must only touch the database. If your DB-step closure needs data from outside, fetch it in a prior Activity and pass it in as `entflow.Result[T]`, never fetch it inline."
- Consider a lint/codegen-time static check (AST walk in the entc extension, v0.5) that flags DB-step closures referencing anything from the DI registry (`entflow.Use[T]`) — since legitimate external-call access always goes through `Use[T]`, seeing it inside a DB step body is a near-certain signal of the anti-pattern, and can be a generation warning even though it can't be perfectly guaranteed (closures can still call an external SDK captured via a package-level var).
- For the relay specifically: claim outbox rows in one short transaction (mark `delivering`, commit), deliver to NATS *outside* any transaction, then mark `delivered` in a second short transaction. Never call `nc.Publish` (or any network I/O) while holding the claiming transaction open — this is the same mistake as Pitfall 1 applied to the outbox relay instead of the run worker, and it's an easy one to reintroduce because "claim and deliver in one tx" looks simpler and more "atomic-feeling" to a contributor who hasn't internalized why it's wrong.

**Warning signs:**
- Connection pool exhaustion under load that correlates with specific step types, not overall throughput — a giveaway that one particular step is holding connections too long.
- p99 transaction duration metrics (if instrumented) spiking for a subset of DB steps.
- Code review finding a DB-step closure that imports an HTTP or gRPC client package.

**Phase to address:**
v0.1 (documentation of step-kind rules) and v0.5 (entc static-analysis warning for DI-registry access inside DB-step closures). The relay-specific version of this pitfall belongs to v0.4 (Outbox).

---

### Pitfall 3: Idempotency key TTL / provider window mismatch silently defeats exactly-once-outcome

**What goes wrong:**
entflow's deterministic idempotency key is `runID:stepName:attempt` — a pure function of the run row, framework-supplied so generated/LLM-written code can't get it wrong (§5.3). This is correct *as a key derivation strategy*, but the guarantee it buys ("at-least-once execution + idempotent effect = exactly-once outcome") depends entirely on the downstream provider actually treating that key as idempotent for as long as entflow might resend it. Real providers bound this: Stripe prunes idempotency records after ~24 hours. If a run is stuck (e.g., blocked on a manual admin intervention, or retried after a long operational incident) and the *same* attempt number is replayed after the provider's window has lapsed, the provider treats it as a brand-new request and executes the side effect again — silently. entflow has no visibility into this: from its perspective, the same key was reused, and the provider says "200 OK" either way. There is no way for the framework to detect that the "idempotent" retry actually created a duplicate charge/refund/email.

**Why it happens:**
Idempotency-key correctness is an externally-imposed, provider-specific, time-bounded property. entflow can guarantee it generates the *same* key deterministically forever, but it cannot guarantee any given provider treats that key as canonical forever. This is invisible until an incident is old enough (>24h stuck run + retry) to cross the window, which won't show up in unit tests or even the crash-simulation harness (which presumably runs fast, well within any provider TTL).

**How to avoid:**
- Document the exactly-once-outcome guarantee's actual scope: "exactly-once outcome, *provided retries occur within the target provider's idempotency-key retention window*." This is a documentation and API-design responsibility, not something entflow can code its way out of for arbitrary third-party providers.
- For Activities whose retry could plausibly span >24h (failed runs sitting in `failed:<step>` awaiting manual admin retry), surface a warning at retry time if `now() - attempt_timestamp` exceeds a configurable threshold, and let the operator decide (re-verify via the provider's list/query API before blindly retrying) rather than silently resending the same key.
- For the mock-provider crash-simulation harness (release-gate requirement), explicitly include a test that simulates a provider idempotency-key-window expiry between attempts, and assert entflow's behavior (or documented lack of protection) is the expected one — this converts a silent gap into a known, tested boundary rather than a surprise.

**Warning signs:**
- Any workflow with human-in-the-loop or long-hold retry paths (admin-retry of `failed:<step>` runs) touching payment/refund/email-send Activities.
- Absence of an "attempt age" concept anywhere in the Activity retry/backoff design.

**Phase to address:**
v0.3 (Activities — attempt protocol, retry/backoff). Document the boundary explicitly in the same phase that ships the three-beat protocol; add the stale-retry warning as a v0.3 or v0.5 (ops/admin retry surface) feature.

---

### Pitfall 4: The two-pass bootstrap chicken-and-egg breaks in ways beyond "just run generate twice"

**What goes wrong:**
Schema files import generated packages (`*ent.Tx`, `order.StatusCancelled`, `*ent.Order`) inside `Flows()` closures — this is architecturally identical to how `Hooks()` and `Policy()` work today, and ent solves it via `ent/runtime` as a separate binding package specifically to break the import cycle (schema package → generated package → schema package via runtime hooks). This pattern is well-trodden for hooks/privacy, but entflow adds a new failure surface: the entc extension itself needs to *read* the `Flows()` declarations at generation time (to extract step names, dependency edges, transition claims) without being able to *execute* them, because on a from-scratch project the generated package the closures reference doesn't exist yet. If the extension tries to load/compile the schema package to introspect `Flows()` (rather than treating it purely as an AST/reflection data extraction problem, the way ent's field/edge declarations already work), first-time `go generate` on a brand-new schema fails outright, not just with reduced ergonomics — because the schema package fails to *compile* (it references `order.StatusCancelled` from a package that doesn't exist yet), and package-level compilation failure means the extension can't even load the package to inspect it, unlike hooks/privacy which are pure closures with no builder-time *data* the generator needs to extract before generation.

**Why it happens:**
Hooks and privacy policies are 100% opaque to the generator — it doesnn't need to know anything about what's inside a `Hooks()` closure at generation time, so the two-pass bootstrap (stub generate → real generate) works trivially: pass 1 generates with hooks/privacy simply not wired in yet (they're picked up via `ent/runtime` reflection at *runtime*, not codegen time), pass 2 is identical from the generator's perspective. entflow's `Flows()` is different in kind: the extension needs *data* out of the builder chain at codegen time (step names, `After()` edges, `Transition()` claims, entity references) to generate the run-entity schema, the transitions-enforcing hook, and the worker. That data lives in the same Go source file as closures that reference not-yet-generated types. If extracting that data requires the package to type-check/compile, first-time bootstrap is broken by construction, not just inconvenient.

**How to avoid:**
- The builder chain (`entflow.New[In](name).UpdateSelf(...).Activity(...)...`) must be evaluatable as *data* independent of whether the closures inside it reference types that don't exist yet. In practice this likely means: on first pass (no `ent.Order` yet), the schema package genuinely will not compile if a step closure references `order.StatusCancelled` — so the true first-time bootstrap sequence must be documented as "add `Flows()` after the first successful `go generate` that establishes the base entity types," exactly as ent's own docs tell hook/privacy authors to do. This needs to be an explicit, tested, documented onboarding step, not discovered by users via a cryptic compile error.
- Alternatively (harder, more valuable): make the extension tolerant of a schema package that fails to compile by extracting flow metadata via `go/ast` static analysis (parsing the builder-chain call expressions textually) rather than requiring `go build`/reflection — this is how ent's own field/edge extraction already works (AST-based, not compiled-and-reflected), and is the standard technique entgql/entproto use. If entflow's extension instead tries to `go build` and reflect into the schema package to read `Flows()`, this pitfall is unavoidable; if it parses the AST like ent's core loader does, first-time bootstrap "just works" the same way fields/edges always have.
- Either way: write an explicit "create a brand-new ent project with a flow from step 1" integration test / quickstart doc, and run it in CI. This exact scenario (empty project → first `go generate` with a `Flows()` method already present) is exactly the kind of thing that "works for the maintainer's existing project" and silently breaks for every new user, because the maintainer's dev loop never actually starts from zero.

**Warning signs:**
- Any implementation detail where extension code does `packages.Load` + type-checking (not pure `go/ast` parsing) to read builder-chain data before deciding whether flows are AST-extractable.
- The quickstart docs skipping straight to "add `Flows()` to your schema" without an explicit "run `go generate` once first, with fields only" step.

**Phase to address:**
v0.5 (Codegen — entc extension). This is the single highest-risk technical unknown for the whole codegen milestone and should be spiked/prototyped before committing to the AST-vs-reflection extraction approach — ideally validated in v0.1 already by keeping the `Flows()` builder chain's data model AST-parseable by design even while codegen doesn't exist yet.

---

### Pitfall 5: State-machine cross-validation is either too strict (false positives users disable) or too weak (misses real gaps) — there is no comfortable middle

**What goes wrong:**
The flagship feature — "every `Transition("x")` claimed by a step must be a legal edge in the transitions map, and every edge in the map should be claimed by some step" — sounds like a clean bidirectional invariant but runs into real-world cases that make the second half (every edge must be claimed) a false-positive generator:
1. **Conditional/branching transitions**: a single step with `entflow.When(SelfWas("paid"))` guarding one of two possible transitions from the same source state (e.g., `paid → shipped` via normal flow, `paid → cancelled` via a *different* flow's cancel step) — both edges are legitimately claimed, by different flows, and the validator must aggregate claims across *all* flows on the entity, not just the one being validated, or it will falsely flag edges as orphaned.
2. **Transitions made outside any flow entirely**: manual admin edits (an ops engineer force-updating a status via `client.Order.UpdateOneID(x).SetStatus(...)` directly, bypassing flows but still going through the transitions-enforcing hook, which only checks the edge is legal, not that a flow claims it), data migrations, or seed scripts. These are legal transitions with no flow claim at all — if the validator treats "must be claimed by some step" as a hard error rather than a warning, any entity with an admin-only transition (e.g., `draft → cancelled` reachable only via an ops tool, never via a customer-facing flow) is permanently unbuildable without either adding a dummy flow step or disabling the check.
3. **Enum evolution**: adding a new status value or a new legal edge to the transitions map without immediately writing the flow step that claims it (e.g., "we're adding a `refund_pending` status this sprint, the step lands next sprint") — a hard error here blocks incremental development in exactly the way ent's own two-pass bootstrap philosophy (ship stubs, flesh out later) is designed to avoid elsewhere in the system.

**Why it happens:**
The validator's mental model conflates "every legal edge should be reachable by *some* explicit mechanism" (true and valuable) with "every legal edge must be claimed by an entflow step" (false — flows are not the only legitimate way an entity transitions, by entflow's own admission that runs are privacy-governed and admin retry is itself "a state transition an admin is permitted to make," §3.5 — which is not necessarily a *flow* step at all).

**How to avoid:**
- Make "every declared edge must be claimed by some step" a **warning**, not a **hard generation error**, from day one — and make it suppressible per-edge (e.g., `entflow.Transitions(map, entflow.Unclaimed("draft", "cancelled"))` or an annotation marking an edge as "admin-only, intentionally unclaimed by any flow") rather than a single global escape hatch that gets used to blanket-disable the whole check (which is what happens when the only escape hatch is coarse — see how ESLint/golangci-lint suppression comments get used at file-scope instead of line-scope once a rule is too noisy).
- The "every `Transition()` claim must be a legal edge" direction (claim → must exist in map) should stay a hard error — that direction has no legitimate false-positive case; a step claiming a transition not declared as legal is unambiguously a bug (typo, stale edge after enum edit).
- Aggregate claims across *all* flows declared on the entity (not per-flow) before evaluating "claimed by some step," to avoid false positives from case 1 above.
- Ship a documented, sanctioned pattern for admin/manual transitions from day one (e.g., a lightweight `entflow.ManualTransition("draft", "cancelled", "ops-initiated cancellation, no flow")` annotation that satisfies the validator without requiring a fake flow) rather than leaving users to discover the workaround (writing a throwaway one-step flow purely to silence the validator, which is worse than not having the check).

**Warning signs:**
- Early adopters filing issues like "how do I add a status my flows never reach" or "the generator won't build and I don't have a flow for this transition."
- The suppression mechanism, if coarse-grained, showing up disabled at the file/package level in real usage rather than per-edge — a sign the check produced enough noise that users opted out entirely, defeating its value.

**Phase to address:**
v0.5 (Codegen — flow-graph validation). Design the warning/error split and the suppression annotation *before* shipping the first version of the cross-validator, since changing a hard-error check to a warning later is easy, but changing the suppression annotation's shape after users have written it into schemas is a breaking change.

---

### Pitfall 6: `entflow.Result[T](ctx, "step")` is a stringly-typed escape hatch that decays into the project's biggest source of runtime panics

**What goes wrong:**
The design doc explicitly names this as "an accepted ergonomic compromise" (§3.3) — the positional-self shortcut covers the common case, but any cross-step data access beyond the immediately preceding step goes through a string-keyed, type-parameterized getter. This pattern decays predictably: (1) step names get renamed during refactors and the string literal in a *different* step's `Result[T](ctx, "old-name")` call doesn't get caught by any compiler check — it's a runtime lookup failure or, worse, silently returns a zero value if the API isn't designed defensively; (2) the `T` in `Result[T](ctx, "step")` has no relationship enforced by the compiler to the actual output type of the named step — a caller can ask for `Result[int](ctx, "refund")` when the refund step actually produced `RefundResult`, and get a runtime type-assertion failure deep inside a workflow, potentially only exercised by one conditional branch that isn't hit in tests; (3) as flows grow beyond 3-4 steps, the "just look one step back positionally" ergonomic win stops applying to most real access patterns, so the *common* case in mature flows becomes the *stringly-typed* case, not the typed positional one — meaning the "accepted compromise" becomes the dominant code path, not the escape hatch.

**Why it happens:**
This is a direct, known consequence of deferring "generated per-flow results structs for cross-step typed access" (explicitly listed in PROJECT.md's Out of Scope, revisit-later). It's a reasonable MVP tradeoff, but the design doc's own framing ("revisit once real flows show how often the positional-self shortcut is insufficient") suggests the team expects this to be common enough to need revisiting — meaning the decay is anticipated, not hypothetical.

**How to avoid:**
- At minimum, make step-name references in `Result[T](ctx, "step")` calls validate at codegen time (v0.5) even before generated typed structs exist: the entc extension already walks the step graph for transition cross-validation, so it can also validate that every `Result[T](ctx, "name")` call site references a real, upstream (already-executed-by-dependency-order) step name, and ideally that `T` matches that step's declared output type via reflection on the builder metadata. This converts the failure mode from "runtime panic in production" to "generation error," which matches entflow's whole thesis (can't be wrong *and commit*).
- Before v0.5 ships that check, document `Result[T]` as an explicitly discouraged/last-resort API in v0.1-v0.4, and dogfood real multi-step flows early (in the crash-simulation harness's test flows) specifically to pressure-test how often it's needed — this validates or invalidates the "positional covers the common case" assumption before external users hit it.
- Track (even informally, e.g., a code comment or dashboard in the example app) how many `Result[T]` call sites exist per flow as a proxy metric for whether the compromise is holding; if it climbs, that's the signal to pull forward the generated-typed-struct design rather than waiting for "real flows to show" it organically via user complaints.

**Warning signs:**
- Any flow with more than ~4 steps needing `Result[T]` calls to more than one prior step.
- Bug reports describing panics that only reproduce on a specific conditional branch of a flow.
- Step renames during refactors that don't trigger any build failure elsewhere in the schema file.

**Phase to address:**
v0.1 (document as discouraged, measure usage) through v0.5 (add codegen-time validation of step-name references and, ideally, type matching). Full resolution (generated typed structs) is explicitly post-MVP per PROJECT.md, but the *validation* of the escape hatch should not wait that long.

---

### Pitfall 7: Multi-dialect claims are aspirational until proven — SKIP LOCKED and enum/JSON support diverge sharply across SQLite/MySQL/Postgres

**What goes wrong:**
`FOR UPDATE SKIP LOCKED` is Postgres-native since 9.5 and MySQL 8.0+, but **SQLite does not support it at all** (SQLite has no row-level locking — a transaction locks the whole database file, by design, for serializable isolation). Since ent supports SQLite as a first-class dialect (commonly used for tests and small deployments) and entflow's crash-simulation harness explicitly runs "against a real database" (constraint in PROJECT.md), if the harness or the default worker implementation is written against `FOR UPDATE SKIP LOCKED` unconditionally, it will not run on SQLite at all — not "run slower," but fail to compile the query or behave incorrectly. This has two downstream consequences: (1) any user who reaches for SQLite for local dev/tests (extremely common in the ent ecosystem, since ent's own quickstart uses SQLite) cannot use entflow's worker without a dialect-specific fallback (e.g., whole-database-lock semantics standing in for row-level SKIP LOCKED on SQLite, or an explicit "SQLite not supported for the worker, only for Multi/single-tx flows" restriction); (2) JSON column support (needed for the run row's serialized input/result) and enum representation (needed for the transitions-annotated status field) also diverge — Postgres has native `jsonb` and `enum` types, MySQL has `JSON` and no true enum-with-CHECK-constraint-equivalent-safety, SQLite has neither natively (JSON stored as TEXT, enums as CHECK constraints) — meaning a naive migration or query built against Postgres-specific SQL syntax silently misbehaves or fails on the other dialects.

**Why it happens:**
Ent itself is famously multi-dialect (that's a core value proposition), so entflow inherits an implicit expectation of dialect parity that the underlying primitives (SKIP LOCKED specifically) simply do not have. It's easy to develop and test entflow exclusively against Postgres (the dialect where every advanced feature "just works") and only discover the SQLite gap when a user tries it, or when CI is set up to run the crash-simulation matrix against multiple dialects and someone notices SQLite was quietly excluded.

**How to avoid:**
- Decide and document, explicitly and early (v0.2), entflow's dialect support matrix: e.g., "Postgres is the fully-supported worker dialect (SKIP LOCKED, advisory locks); MySQL 8+ is supported with SKIP LOCKED; SQLite is supported for `Multi`-style single-transaction flows (v0.1 shape, no run-row worker) but not for the durable worker, because SQLite's locking model cannot express non-blocking concurrent claim." This turns an accidental gap into an intentional, documented boundary — consistent with entflow's own "honest boundary" design philosophy already applied elsewhere (DI, compensation).
- If SQLite worker support is desired anyway, the claim query needs a dialect-specific implementation (e.g., `UPDATE ... WHERE id = (SELECT id FROM runs WHERE state IN (...) LIMIT 1)` single-statement claim, which is safe under SQLite's whole-database transaction lock but far lower throughput) — this should be a conscious v0.2+ decision, not a silent fallback that looks like it works until someone runs two SQLite workers concurrently.
- Abstract the claim query behind a small dialect-aware interface from the start (even if Postgres is the only real implementation in v0.2), so MySQL/SQLite support later doesn't require restructuring the poller.
- The crash-simulation harness (release gate) should explicitly state which dialect(s) it certifies — "passes against Postgres" is not the same claim as "passes against every ent-supported dialect," and the release-gate constraint in PROJECT.md should be read as dialect-scoped unless stated otherwise.

**Warning signs:**
- CI running the crash-simulation harness against only one dialect (or defaulting to whatever `go test` picks, often SQLite for ent projects, silently).
- Issues from users trying `entflow` with SQLite (very likely given ent's own quickstart defaults to SQLite) hitting an opaque SQL syntax error rather than a documented "not supported" message.

**Phase to address:**
v0.2 (Durability — worker poller). Decide the dialect matrix before the poller's claim query is finalized; this is much cheaper to decide upfront than to retrofit.

---

### Pitfall 8: "Temporal-lite" scope creep stall — the gap between "compensation deferred" and real user pain arrives faster than expected

**What goes wrong:**
Projects positioning themselves as "X-lite" for a category with a dominant, feature-complete incumbent (Temporal) tend to stall at a predictable point: v0.1-v0.4 deliver a genuinely useful, honest subset (which entflow's roadmap does well — deferring compensation and timers is a defensible, documented decision), but the *first* real production flow a team tries to model almost always needs either compensation (payment flows are the headline example in entflow's own docs — refund-after-partial-failure is exactly a compensation scenario) or a timer (any flow with "wait 24h then check," "retry with the customer's confirmation," dunning/retry billing, or shipping-timeout flows). Because these two are both explicitly out of scope for v0-MVP, the realistic failure mode isn't "the library is broken," it's "the first non-toy flow a team wants to build doesn't fit the model," which is a much quieter, harder-to-detect failure — teams don't file bugs, they just quietly go back to hand-rolled orchestration or evaluate Temporal instead, and entflow never hears about it.

**Why it happens:**
Compensation and timers are deferred for good reasons (compensation is "physics-limited to DB-only flows" once an Activity has run; timers need a `wake_at` mechanism that's a natural-but-not-yet-built extension of the run row) — but the deferral decision was made from the framework author's implementation-complexity perspective, not validated against what fraction of real target flows (order lifecycles, payment flows, sagas — entflow's own named use cases) actually need one or both. If, empirically, most real flows in the target domain need at least a timer or at least manual-retry-as-compensation, the MVP's "honest v0 story" (failed runs stay inspectable and manually retryable) may not be honest enough to be *usable*, only honest about its limitation.

**How to avoid:**
- Before finalizing the MVP roadmap, stress-test the "deferred" list against 2-3 concrete target flows end-to-end on paper (a payment/refund saga, a shipping flow with a timeout, an onboarding flow with a wait-for-confirmation step) and explicitly document, per flow, what the MVP-shape workaround is (e.g., "timer" flows can be modeled as: emit an event, have an external cron/scheduler call `flow.Start` again later, since flow-chaining and outbox already support flows starting other flows — a `wake_at` timer is sugar over a pattern that's already expressible, not a hard blocker). This turns "deferred" into "deferred with a documented workaround" rather than "deferred, hope nobody needs it yet."
- Track this as a leading indicator, not a lagging one: instrument (or manually survey) early adopters' abandoned/never-shipped flow ideas, not just shipped ones — the failure mode is silent, so passive telemetry on *successful* usage won't surface it.
- Resist the temptation to build compensation/timers reactively-and-quickly once the pain is felt; both interact with the run-row model and the transitions cross-validator (an `OnFail` compensating step needs its own transition claims, per §5.5) — a rushed retrofit risks the same schema-migration pain the state-machine validator is designed to prevent elsewhere.

**Warning signs:**
- Community/discussion channels (issues, forum) with more "how do I do X" questions about timers/compensation than actual production bug reports — a sign people are hitting the wall trying to adopt, not using it and finding bugs.
- Example/showcase flows in docs that are all conveniently DB-only or single-Activity — if the maintainer's own example flows never need a timer or compensation, that's worth noticing as a possible blind spot rather than validation.

**Phase to address:**
Roadmap/planning phase (before v0.1 kickoff) — stress-test deferred-scope decisions against concrete flows now, while the cost of discovering a gap is a documentation/roadmap change, not a redesign. Revisit explicitly at the v0.4→v0.5 transition (before codegen locks in the transitions/run-row shape further).

---

### Pitfall 9: Crash-simulation harnesses that pass are not proof of crash-safety — nondeterministic kill points and in-process fakes produce false confidence

**What goes wrong:**
A harness that "kills the worker" by, e.g., returning an error from a mocked step function, cancelling a context, or calling `os.Exit` from within the same test process **does not exercise the actual failure mode it's meant to simulate** — a real crash (OOM kill, SIGKILL, host failure, panic in a goroutine that doesn't unwind cleanly) interrupts execution at an arbitrary machine instruction, including mid-syscall, mid-buffered-write, and critically, *it does not get a chance to run any deferred cleanup, rollback, or "graceful" error path*. A test harness that kills via a Go-level mechanism (returning an error, `panic()` + `recover()`, cancelling `ctx`) always runs inside the same process and always unwinds through Go's normal control flow — even a "hard" simulated crash via `t.Fatal` or `runtime.Goexit` still lets deferred functions and, more importantly, the database driver's connection-level cleanup run, which a real process kill would not. This means a harness that "kills the worker" but does so cooperatively can pass green while a real crash (e.g., the process is SIGKILLed by the OS OOM-killer mid-transaction) leaves a connection in a state the cooperative test never modeled — for example, a transaction that a real crash leaves half-written at the TCP level, vs. a Go-level fake-crash that always results in a clean rollback because the underlying `*sql.Tx` object's `Close`/rollback still executes via defer.

**Why it happens:**
True process-kill simulation (fork a real subprocess running the worker, `SIGKILL` it at a controlled point, verify DB state from a separate process) is significantly more engineering effort than an in-process fake, and the in-process version is much easier to make deterministic (you can guarantee the "crash" happens at exactly step 3 of 5, which is hard to guarantee with real signal timing) — so the natural implementation path drifts toward the easier, less faithful version, especially under time pressure, while still calling it a "crash simulation harness" and treating a green run as the release gate it's meant to be (constraint in PROJECT.md: "No public release without the deterministic crash-simulation harness passing").

**How to avoid:**
- Distinguish two tiers explicitly in the test suite and in what "passing" means for the release gate: (1) **logical crash-point coverage** — an in-process harness that intercepts at every step boundary and every activity-protocol beat and forces the *code path* to stop there (this is valuable, deterministic, and fast, and should be the bulk of the test matrix, but its scope is "did we forget to commit progress atomically," not "does this survive a real process death"); (2) **real process-kill coverage** — a smaller number of tests that actually `exec.Command` a real worker binary, `SIGKILL` it after a controlled delay or after observing a specific DB-visible state transition (e.g., poll the run row until it flips to `running`, then kill within a tight window), and verify from a fresh process that the DB is left in a valid, resumable state. Only tier (2) tests the actual release-gate claim ("survives crashes at any point"); tier (1) alone produces false confidence if it's the only thing badged as "the crash-simulation harness."
- To make tier (2) deterministic rather than flaky/racy: don't try to kill at a precise instruction — instead, inject a deterministic delay (e.g., an env-var-controlled `time.Sleep` or a blocking read on a channel/file the test controls) at each of the specific beats under test (before/after each of the three Activity beats, before/after the DB-step commit), start the worker as a subprocess with that delay active at exactly one target beat, wait for the delay to be observably entered (e.g., poll for a sentinel file write, or poll the DB for an intermediate state only reachable inside that window), then `SIGKILL`. This makes "kill mid-beat-2" deterministic and repeatable without needing real signal-race timing.
- Test-time database isolation: since tier (2) needs a real subprocess talking to a real database, either use a dedicated ephemeral Postgres per test run (Docker/testcontainers) with a fresh schema per test to avoid cross-test interference, or namespace run rows per test (a `test_run_id` tag) and clean up explicitly — do not share a long-lived dev database across parallel crash-simulation test runs, since two tests racing to observe/kill at a specific DB-visible state will interfere with each other's polling.
- Explicitly verify the *absence* claims, not just the *presence* claims: "no step effect committed without its progress record" requires querying both the effect (e.g., the row the DB step wrote) and the progress pointer and asserting they're either both present or both absent — an assertion that only checks "the run eventually reached the right terminal state" can pass even if an intermediate crash caused a duplicate effect that later retries painted over (e.g., two refund rows because of a duplicated Activity effect, but the *run's* terminal state still looks correct) — the mock provider's idempotency-key-hit counter (already planned per PROJECT.md) is the right mechanism, but it must be asserted per-attempt, not just checked once at the end.

**Warning signs:**
- The "crash simulation" test suite has zero uses of `os/exec` or `syscall.Kill`/`SIGKILL` — everything is simulated via error returns, mocked interfaces, or context cancellation.
- Kill points are expressed as "after N calls to a mock" rather than "after observing DB state X from an external process" — the former is deterministic but proves nothing about real crash timing; the latter is what actually matters.
- Flaky tests in the crash-simulation suite that get "fixed" by adding sleeps/retries rather than by switching to a DB-state-driven synchronization mechanism — a sign the harness is fighting real concurrency rather than deterministically controlling it.

**Phase to address:**
v0.2 (Durability — this is literally what the crash-simulation harness is for, and the release-gate constraint applies from this phase forward). Explicitly split tier (1)/tier (2) scope in the v0.2 plan so "harness passing" has an unambiguous, documented meaning before it's invoked as a release gate in v0.5.

---

## Technical Debt Patterns

| Shortcut | Immediate Benefit | Long-term Cost | When Acceptable |
|----------|-------------------|-----------------|------------------|
| Reflection-based v0.1 runtime (no codegen) | Proves declaration ergonomics fast, ships value before the hard 20% (worker) is built | Ugly ergonomics ship to early adopters, who anchor on the API surface before typed sugar exists — hard to change the builder shape later without breaking them | Acceptable and explicitly planned (PROJECT.md Build order constraint) — but freeze the *builder chain shape* (not the runtime internals) as early as possible so early users aren't burned by an API change in v0.5 |
| `entflow.Result[T](ctx, "step")` string-keyed getter instead of generated typed structs | Ships cross-step access without inventing a whole codegen subsystem in v0.1-v0.4 | Runtime panics on typos/renames, decays into the dominant access pattern for flows >4 steps (Pitfall 6) | Acceptable through v0.4 provided codegen-time validation of step-name references lands in v0.5, not deferred indefinitely |
| No lease/heartbeat on claimed-but-in-progress runs in early worker versions | Simpler poller, ships v0.2 faster | Zombie runs (Pitfall 1) go undetected until a real crash mid-Activity happens in production | Never acceptable past v0.3 (Activities) — DB-step-only flows in v0.2 are safe without it because effect+progress commit atomically, but Activities reintroduce the gap |
| Hard-error "every edge must be claimed by a step" validator (simplest version to implement) | Simple binary check, easy to ship first | False positives on admin-only transitions and staged enum evolution train users to disable the whole check (Pitfall 5) | Never acceptable as the shipped v0.5 default — must be warning + fine-grained-suppressible from the first release, since "fix it later" means fixing it after users have already worked around it in ways that are hard to migrate away from |
| Testing crash-resume only via in-process mocked "crashes" | Fast, deterministic, cheap CI | False confidence that the release-gate harness (PROJECT.md constraint) actually proves crash-safety (Pitfall 9) | Acceptable as the *majority* of the test matrix, never acceptable as the *entirety* of what's badged "the crash-simulation harness" for release-gate purposes |
| Postgres-only development/testing of the worker poller | Fastest path to a working v0.2, avoids multi-dialect complexity early | Silent SQLite/MySQL breakage discovered by users, not CI (Pitfall 7) | Acceptable for v0.2 *if* the dialect support matrix is explicitly documented as Postgres-only-for-now rather than left implicit |

## Integration Gotchas

| Integration | Common Mistake | Correct Approach |
|-------------|-----------------|-------------------|
| Stripe (or any idempotent-API provider) | Trusting the framework-supplied key is idempotent forever; retrying a stuck run's Activity after the provider's key-retention window (Stripe: ~24h) lapses | Document the window dependency explicitly; add stale-attempt warnings before blind retry on old runs (Pitfall 3) |
| NATS JetStream | Assuming producer-side `Nats-Msg-Id` dedup means the relay/outbox never needs consumer-side idempotency | Treat JetStream dedup window as a latency/duplicate-reduction optimization, not a correctness guarantee; consumers of flow-chaining events must still dedup (inbox pattern) since dedup window is time-bounded |
| River (if adopted as pluggable backend, per open question 5) | Building the adapter boundary assuming River's job semantics map 1:1 onto entflow's run-row semantics (attempt counting, idempotency keys, state machine) | River is a generic job queue, not a workflow/state-machine engine — the adapter needs to translate entflow's run-row state transitions onto River's job lifecycle, not assume they're the same model; validate this mapping explicitly before committing to the adapter boundary shape |
| ent's own hook/privacy system | Assuming the transitions-enforcing hook composes safely with user-defined hooks on the same field/entity without an explicit ordering contract | Document (and ideally enforce via `gen.Hook` ordering primitives) that the transitions-enforcing hook must run before/after user hooks in a specified, tested order — silent hook-ordering bugs are a classic ent extension pitfall since ent's hook chain order is caller/registration-order-dependent, not automatically prioritized |
| OpenTelemetry | Spans named `workflow.<Flow>.<step>` without also propagating trace context across the "no transaction, untransacted external call" gap in the Activity protocol, losing the causal link between beat (a)'s tx and beat (b)'s external call span | Propagate the run ID / trace ID explicitly (not just via ambient context) across all three beats, since beats (a) and (c) are separate transactions and beat (b) has no tx at all — don't rely on context propagation alone if the worker process could differ between beats after a crash/resume (a *different* worker may execute beat (c) than executed beat (a)) |

## Performance Traps

| Trap | Symptoms | Prevention | When It Breaks |
|------|----------|------------|-----------------|
| Poll-storm from many workers polling the same claimable-run predicate at high frequency with no jitter | CPU/DB load dominated by claim queries rather than actual step execution; contention on the claim query itself | Add jitter to poll intervals; use `LISTEN/NOTIFY` (Postgres) or `tx.OnCommit` hooks (already planned, §5.4) to reduce reliance on tight polling, keeping polling as the correctness backstop only | Becomes visible once worker fleet size grows beyond a handful of instances polling at sub-second intervals |
| Outbox/run table bloat from never pruning terminal-state rows | Claim query (`FOR UPDATE SKIP LOCKED ... WHERE state IN (claimable)`) slows over time even though the *claimable* row count stays flat, because Postgres must still scan/skip dead and terminal-but-unindexed rows; matches River's documented real-world failure mode (millions of dead tuples) | Partial index on claimable states only; explicit archival/pruning job for terminal-state run and outbox rows; monitor table/index bloat, not just claimable-row count | Silent until table size crosses a threshold where sequential/index scan cost dominates — can take months in production before symptoms appear, by which point remediation (bulk delete + vacuum) is itself a heavy operation |
| Long-running run rows (privacy-governed, queryable ops surface, §3.5) held open in an app transaction by an ops dashboard query | Dashboard/reporting queries that join across many run rows without an explicit read-only, short transaction can hold snapshots open, contributing to Pitfall/autovacuum-blocking issues independent of the worker's own transactions | Ensure ops/reporting queries use short, read-committed transactions; never let a dashboard hold a transaction open while rendering (e.g., streaming results outside the tx) | Becomes a production incident only under concurrent write load — invisible in low-traffic dev/staging |
| Connection pool shared between API server and in-process worker (default zero-infra topology, open question 3) | API request latency degrades under worker load spikes, or vice versa — the two workloads have very different connection-hold-time profiles (short API queries vs. worker claim+execute cycles) | Document and default to separate connection pools (even if same DB, same process) for the API-serving path and the worker-polling path, sized independently | Breaks first under any load test that runs API traffic and a busy worker queue concurrently — likely invisible in solo-developer testing where load is never concurrent |

## Security Mistakes

| Mistake | Risk | Prevention |
|---------|------|------------|
| `DenyStatusEscalation` privacy rule's "workflow marker" implemented as a context value settable by any caller, not exclusively by the generated runner | Privilege escalation: any code path that can construct a context with the marker (e.g., a test helper accidentally exported, or a debug endpoint) can bypass transition privacy | Use an unexported marker type in the entflow runtime package, set only inside the generated `Exec`/runner's transaction-opening code path (already the stated design, §5.1) — audit that no public API exposes a way to set it, including in test helpers that ship in the public module |
| Run rows treated as fully privacy-governed but the *input* payload (serialized `In`) containing sensitive data (PII, payment details) not itself subject to field-level redaction in ops dashboards/`Describe()` output | Sensitive data (e.g., a captured card token in a serialized `RefundRequest`) exposed via an ops "failed refunds" dashboard query or via `Describe()`/dry-run output to any viewer with run-read privilege | Treat the serialized input/result JSON blob as needing its own redaction/masking policy, not just row-level ent privacy; document that flow authors are responsible for not putting raw secrets in `In`/`Out` types, and consider a codec-level redaction hook for logging/tracing spans specifically (attempt count, error, state are named span attributes per §6 — ensure error messages don't leak the serialized input by default) |
| Idempotency keys derived purely from `runID:stepName:attempt` (deterministic, framework-supplied) used as the *sole* authorization/authentication signal for a webhook-style callback into the flow (e.g., a provider webhook resuming a run) | If a webhook handler trusts the idempotency key alone to correlate/authorize a callback back into a specific run, and the key derivation is fully deterministic and guessable from public run metadata, this could allow request forgery | Idempotency keys are for *provider-side* dedup only; any inbound callback/webhook path resuming a run must use its own signed/verified correlation token, never the idempotency key as an authZ credential |

## UX Pitfalls

| Pitfall | User Impact | Better Approach |
|---------|-------------|-------------------|
| Cryptic entc generation failures when a `Flows()` builder chain is malformed (e.g., a `Transition()` claim referencing a nonexistent status value) | Users hit a Go template panic or an unhelpful `nil` dereference deep in generated-code internals, rather than a clear "step 'refund' claims transition to 'refuned' but no such status exists — did you mean 'refunded'?" | Validate all builder-chain data (step names, transition claims, entity references) with named, specific error messages *before* any template execution — treat validation as a distinct, user-facing pass, not something templates discover by panicking |
| `Result[T]` type mismatch or missing step name discovered only at runtime, in production, on a rarely-hit conditional branch | Silent correctness bugs or panics that are hard to reproduce, since the failing branch may only trigger under specific `When()` conditions | Move this validation to codegen time (Pitfall 6); until then, make the runtime failure mode a clear, typed error (not a panic) with the requested step name and available step names listed |
| `Describe()`/dry-run output that doesn't clearly distinguish "this edge is claimed by a step" vs "this edge is legal but unclaimed (admin-only)" | Operators debugging a stuck workflow can't tell from `Describe()` alone whether a transition is reachable via automation or requires manual intervention | Explicitly label unclaimed-but-legal edges in `Describe()` output (ties directly into Pitfall 5's suppression-annotation design — if edges can be marked `ManualTransition`, `Describe()` should render that distinction) |
| Admin retry of a `failed:<step>` run silently re-executes an Activity whose idempotency key may be stale (Pitfall 3) with no warning surfaced to the operator clicking "retry" | Operator believes retry is always safe (that's the whole framework promise) and doesn't realize a specific retry, this late, might not be | Surface attempt age / provider-window risk directly in the retry UI/API response, not buried in docs |

## "Looks Done But Isn't" Checklist

- [ ] **Crash-resume:** Often missing lease/heartbeat semantics for runs claimed-but-crashed mid-Activity (not just mid-DB-step) — verify by killing a worker between Activity beats (a) and (c), not just between whole steps.
- [ ] **Exactly-once-outcome:** Often missing explicit handling/documentation of provider idempotency-key TTL expiry on long-stuck runs — verify with a test that simulates a provider "key not found, treating as new request" response on a stale retry.
- [ ] **State-machine cross-validation:** Often missing support for legitimate unclaimed-but-legal edges (admin transitions, staged enum evolution) — verify by adding a status value with a legal edge and no flow step, and confirming generation doesn't hard-fail.
- [ ] **Multi-dialect support:** Often missing an explicit, tested SQLite/MySQL story for the worker specifically (vs. the `Multi`/single-tx v0.1 shape, which is dialect-agnostic) — verify by running the crash-simulation harness (or at least the worker poller) against every dialect ent claims to support, not just Postgres.
- [ ] **Outbox relay:** Often missing pruning/archival for delivered rows — verify by running the relay against a synthetic high-volume workload and watching claim-query latency over time, not just correctness at low volume.
- [ ] **Two-pass bootstrap:** Often missing a tested "brand new project, first `go generate` with `Flows()` already present" path — verify with a from-scratch quickstart integration test in CI, not just incremental-change testing against the maintainer's existing dev project.
- [ ] **`Result[T]` safety:** Often missing any compile-time or generation-time check that referenced step names exist and types match — verify by intentionally renaming a step and confirming generation (not runtime) catches the dangling reference.
- [ ] **Ops/admin retry surface:** Often missing a distinction between "safe to retry" and "retry may duplicate an external effect" in the run-retry UI/API — verify the retry path surfaces attempt age and Activity involvement, not just a generic "retry" button.

## Recovery Strategies

| Pitfall | Recovery Cost | Recovery Steps |
|---------|----------------|------------------|
| Zombie runs discovered in production (Pitfall 1) | MEDIUM | Add lease/heartbeat column + reclaim predicate retroactively (additive migration); backfill a default lease on existing `running`-state rows; add an ops query/alert for runs stuck past N×expected-step-duration as a stopgap before the fix ships |
| State-machine validator false positives already causing users to disable the check globally (Pitfall 5) | HIGH | Requires a breaking-ish change to the suppression mechanism (coarse → fine-grained annotation); must provide a migration guide and possibly a compatibility shim (treat the old global-disable flag as "suppress all, but emit a deprecation warning per unclaimed edge") to avoid a hard break for early adopters who already worked around the noisy checker |
| `Result[T]` runtime panics already shipped without codegen-time validation (Pitfall 6) | MEDIUM | Add the codegen-time check in v0.5 as planned; it's purely additive (turns a subset of previously-possible-but-broken schemas into generation errors) — the main cost is that some existing user schemas may suddenly fail to generate where they previously (silently, riskily) succeeded, so this needs a clear release-notes callout, not a silent tightening |
| Two-pass bootstrap gap discovered post-launch (real users failing on first `go generate`) (Pitfall 4) | HIGH if AST-based extraction wasn't the original design (requires reworking the extension's data-extraction layer); LOW if it was designed AST-first from v0.1 | This is the strongest argument for validating the extraction approach (AST vs. compiled-reflection) as an early v0.1/v0.5 spike rather than discovering the constraint after the extension is built around the wrong assumption |
| Multi-dialect gaps discovered via user bug reports rather than design decision (Pitfall 7) | LOW-MEDIUM | Retroactively document the dialect support matrix (cheap); if SQLite worker support is actually demanded, implement the fallback claim strategy as an additive, dialect-detected code path — does not require redesigning the Postgres path |

## Pitfall-to-Phase Mapping

| Pitfall | Prevention Phase | Verification |
|---------|-------------------|----------------|
| 1. Zombie runs / no lease on claimed-but-crashed Activities | v0.2 (poller design) + v0.3 (Activity protocol) | Crash-simulation kills a worker between Activity beats (a) and (c); assert the run becomes reclaimable within the lease window and no duplicate effect occurs |
| 2. Cardinal-sin tx-held-open reintroduced via DB-step closures or the outbox relay | v0.1 (docs) + v0.4 (relay implementation) + v0.5 (static-analysis warning) | Code review checklist item + connection-hold-duration metric per step type; relay implementation reviewed specifically for claim/deliver/mark transaction boundaries |
| 3. Idempotency-key TTL mismatch on stuck-run retries | v0.3 (Activity attempt protocol) | Crash-simulation / integration test simulating a provider "key expired, treat as new" response on a deliberately stale retry; documentation explicitly scoping the exactly-once-outcome guarantee |
| 4. Two-pass bootstrap chicken-and-egg | v0.5 (entc extension), validated by a v0.1 spike on the builder chain's AST-extractability | From-scratch quickstart integration test in CI: empty project → add entity fields → generate → add `Flows()` → generate again, asserting no manual workaround needed beyond documented steps |
| 5. State-machine validator false positives (unclaimed edges) | v0.5 (flow-graph validation design) | Property/unit tests asserting: (a) a legal-but-unclaimed edge with no annotation produces a warning, not a hard error; (b) an edge marked `ManualTransition`/equivalent produces no warning; (c) a step claiming an illegal edge is still a hard error |
| 6. `Result[T]` stringly-typed decay | v0.1 (document as discouraged) through v0.5 (codegen validation) | Generation-time test: rename a step referenced by `Result[T](ctx, "old-name")` elsewhere in the same schema and assert generation fails with a clear error, not a silent pass |
| 7. Multi-dialect SKIP LOCKED / JSON / enum breakage | v0.2 (worker poller design) | CI matrix explicitly running (or explicitly skipping-with-documented-reason) the worker/crash-simulation suite against each ent-supported dialect; dialect support matrix documented in README before v0.2 ships |
| 8. "Temporal-lite" scope-stall (compensation/timers deferred, first real flow needs them) | Roadmap/planning phase, before v0.1 kickoff; revisited at v0.4→v0.5 transition | Walk 2-3 concrete target flows (payment/refund, shipping-timeout, confirmation-wait) through the MVP shape on paper and document the workaround for each; track adoption-blocking questions in issues/discussions as a leading indicator |
| 9. False-confidence crash-simulation harness | v0.2 (harness design, since it's a release gate from here forward) | Explicit split in the harness's own test output/report between "logical crash-point coverage" (in-process) and "real process-kill coverage" (subprocess + SIGKILL); release-gate sign-off requires both tiers passing, not just the faster in-process tier |

## Sources

Web research (cross-checked, MEDIUM confidence per `classify-confidence --provider websearch --verified`):
- Postgres `FOR UPDATE SKIP LOCKED` job-queue pitfalls and zombie-row failure mode — multiple independent sources (Netdata, Medium/Terris Linenbach on `pg_advisory_xact_lock`, robinverton.de, Vlad Mihalcea)
- Transactional outbox at-least-once/ordering/bloat characteristics — event-driven.io, AWS Prescriptive Guidance, multiple Medium writeups
- Transaction-held-open-during-network-call anti-pattern and connection pool exhaustion — Rock the JVM, USEO Rails anti-pattern catalog, web-alert.io
- Stripe idempotency-key semantics (payload-hash mismatch → 422, ~24h TTL) — Stripe's own idempotency docs/blog, nxtbanking.com, systemdesign.one newsletter
- NATS JetStream at-least-once delivery, dedup window, NAK-vs-backoff behavior — NATS official docs (model deep dive, consumers), dev.to DLQ/retry writeup
- River (Go/Postgres queue) design lessons, specifically table-bloat-from-long-transactions as the dominant real-world failure mode — brandur.org/river (creator's own writeup, ex-Heroku queue experience)
- Temporal non-determinism and workflow-version-in-flight pitfalls (used as an analogous, not directly-applicable, durable-execution pattern) — Temporal's own "Spooky Stories" blog, Bitovi replay-testing writeup, community forum
- Go generics type-inference failure modes and API-design lessons — golang/go issue tracker (#49800, #71789), dev.to 2026 generics retrospective
- `context.WithValue` as a DI anti-pattern in Go — squirly.ca (foundational post on this specific anti-pattern), ahmedalhulaibi.com, Medium "Secret Life of Go"
- Golden-file test brittleness in Go — matttproud.com, ieftimov.com, sebdah/goldie docs
- SQLite/MySQL/Postgres `SKIP LOCKED` support divergence — bigbinary.com (Solid Queue writeup), Vlad Mihalcea, SQLAlchemy mailing list on SQLite locking model
- Postgres long-running-transaction/autovacuum-blocking mechanics — PostgresAI, Stormatics, AWS RDS Postgres autovacuum guide
- Workflow state-machine versioning/migration for in-flight instances — Orkes/Conductor blog, statelyai/xstate discussion #1338

Design-document-derived (HIGH confidence — pitfalls reasoned directly from entflow's own documented architecture):
- `/Users/smintz/go/src/github.com/smintz/entflow/entflow.md` (full design doc — architecture, runtime semantics §5, state-machine cross-validation §3.4, decoupling contract §9)
- `/Users/smintz/go/src/github.com/smintz/entflow/.planning/PROJECT.md` (requirements, constraints, key decisions, open questions)

---
*Pitfalls research for: durable workflow orchestration library for Go (ent schema extension)*
*Researched: 2026-08-08*
