# Phase 1: Runtime Core - Context

**Gathered:** 2026-08-08
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 1 delivers the **hand-usable runtime library**: the flow/step builder API, DB-only execution inside a single caller-supplied transaction, the DI registry, the input codec, the transitions annotation type, `Describe()`, and the `meta` metadata package. Zero codegen. Zero persistence. No run row, no worker, no claim query.

The phase is proven when a developer can write a `Flows()` method on an ent schema, execute it against a real ent client, and get correct all-or-nothing behavior — with the DB/external-call boundary enforced by the Go type system rather than by convention.

**Explicitly NOT in this phase:** run-row persistence and `flow.Start()` (Phase 2), the Activity three-beat protocol and idempotency keys (Phase 3), outbox delivery (Phase 4), any code generation (Phases 5-6).

</domain>

<decisions>
## Implementation Decisions

### Module and Package Layout

- **D-01:** Single Go module `github.com/smintz/entflow` at the repository root. Satellite modules (`entflow/relay/nats`, a River adapter) are added only when their phases arrive — never folded into core. This is what keeps META-02 ("`ent` only") literally true rather than aspirationally true. — **Reversibility:** one-way — the module path becomes the published import path; changing it after any external user imports it breaks every consumer and requires a major-version path suffix.
- **D-02:** Package split for Phase 1: root package `entflow` (flow/step builders, `Codec`, DI registry, retry policy types, `Result[T]`, `Describe`), `meta` (the read-only metadata contract consumed by entconnect and later by codegen), and `internal/testdata/ent` (a demo `Order` schema with a status enum + transitions annotation, used by tests and as the worked example). `worker/` and `entc/` packages do not exist yet. — **Reversibility:** costly — moving exported symbols between `entflow` and `meta` after publication breaks imports at every call site.
- **D-03:** `go.mod` declares `go 1.25` and pins `entgo.io/ent` to an exact tagged version (v0.14.6 at time of writing), never a floating or pseudo-version. Ent's extension API is stable-in-practice but not stable-in-promise; every ent minor bump is a deliberate, tested upgrade.
- **D-04:** META-02 is enforced by a test, not a convention: a Go test shells `go list -deps ./...` (non-test build) and asserts the resulting package set contains only stdlib, `entgo.io/ent/...`, and its transitive requirements. No protobuf, no transport, no messaging client. Preferred over a `depguard` lint config because it needs no extra tooling in CI and fails loudly in `go test ./...`.

### Flow Declaration and Discovery

- **D-05:** entflow defines its own `entflow.Flow` interface, returned from `Flows() []entflow.Flow` on the schema type. This resolves open question #1 in the design doc for v0 — do not wait on an upstream `ent.Flow` interface, and do not block Phase 1 on an ent contribution. — **Reversibility:** one-way — this is the published contract every user schema is written against; renaming it later is a breaking change to every consumer's schema files.
- **D-06:** In Phase 1 (no codegen), flows reach the runtime by **explicit registration**, not reflection over the schema package: the application constructs a registry in `main.go` or test setup and registers the flows returned by each schema's `Flows()` method. An `entflow.FlowsOf(schema any) []Flow` helper performs the `interface{ Flows() []Flow }` type assertion, mirroring how ent itself discovers `Hooks()`. Automatic discovery is codegen's job (Phase 5). Rationale: Phase 1's entire purpose is proving the model works *by hand*; explicit registration is the honest hand-written equivalent that codegen will later automate away.
- **D-07:** `entflow.New[In]("Name")` returns `*Flow[In]`. Input type comes from the type parameter only — there is no `entflow.Input(...)` builder method. Step closure signatures are compile-checked against `In`.

### Phase-1 Execution Semantics

- **D-08:** Two entry points. `flow.Exec(ctx, tx *ent.Tx, in In)` is the primitive — it runs every step in the caller's transaction and neither opens nor commits. `flow.RunInTx(ctx, client *ent.Client, in In)` is sugar that opens a transaction, calls `Exec`, and commits or rolls back. `Start()` does **not** exist in Phase 1 — it is DUR-01 and arrives with the run row in Phase 2. — **Reversibility:** costly — `Exec`'s signature is the seam Phase 2's worker calls into; changing it later touches the worker, the codegen'd runner, and every test.
- **D-09:** Any step error aborts the flow and rolls back the entire transaction — Ecto.Multi semantics, no partial commit. The returned error is a `*entflow.StepError` carrying the failing step's name and kind, wrapping the underlying cause so `errors.Is`/`errors.As` work through it.
- **D-10:** `SelfWas("paid")` reads a **snapshot of the owning entity's status taken at flow entry**, inside the same transaction, before any step runs. Not a live re-read, and not the post-mutation value. This is deliberately the semantics that survives into Phase 2, where the same snapshot becomes a persisted column on the run row — so Phase 1 and Phase 2 agree on what `SelfWas` means. — **Reversibility:** costly — changing the snapshot point later silently changes the behavior of every conditional step already written against it.
- **D-11:** `entflow.Result[T](ctx, "step")` returns `(T, error)` — never panics. Distinct sentinel errors for unknown-step and for type-mismatch, so callers and (later) codegen validation can tell them apart. Phase 1 backs it with an in-memory per-execution map carried on the context, keyed by step name; Phase 2 swaps the backing store to the run row with **no signature change**. — **Reversibility:** costly — the `(T, error)` shape appears in every multi-step flow body; converting to a panicking single-return later would rewrite every call site.

### Step-Kind API Breadth

- **D-12:** Ship the **full DB-step constructor set** named in CORE-03 (`Step`, `CreateSelf`, `UpdateSelf`, `Create`, `Update`, `Query`, `Check`) in Phase 1, implemented as thin typed wrappers over one internal `dbStep` representation. They are sugar over a single mechanism, not seven mechanisms, so the cost is low — and locking the surface now avoids renaming churn once user schemas exist in the wild. — **Reversibility:** costly — these constructor names appear in every user schema file; renaming after release breaks all of them.
- **D-13:** `Activity` and `Emit` are **declarable but not executable** in Phase 1. Their builder methods, closure signatures, and metadata all ship (CORE-04's compile-time `*ent.Tx` exclusion must be provable now, and CORE-11 requires emit topics in the builder data). But `Exec` returns a clear `ErrRequiresDurableRun` if the flow contains an Activity or Emit step. Rationale: shipping a naive in-line Activity path with no idempotency key and no persistence would create exactly the unsafe route this project exists to eliminate — someone would build on it. An honest, loud refusal is better than a working-but-wrong path. Activities become executable in Phase 3, Emits in Phase 4.
- **D-14:** `entflow.Transitions(map[string][]string{...})` ships in Phase 1 as an annotation type plus its accessor — **declaration only**. The enforcing hook (SM-02) and the cross-validator (SM-03/SM-04) are codegen and land in Phase 6. Phase 1 must make the annotation readable from the schema graph so Phase 6 has something to validate against.

### DI Registry and Codec Surface

- **D-15:** The DI registry is **scoped, not global**: `reg := entflow.NewRegistry()`, `entflow.Provide(reg, client)`, and the executor injects it into the context (`entflow.WithRegistry(ctx, reg)`). No package-level global registry. Rationale: parallel tests need independent mock sets, and a process-global registry would muddy Phase 2's multi-worker story.
- **D-16:** `entflow.Use[T](ctx) T` panics when no provider is registered — matching the design document's own example ergonomics (`entflow.Use[*stripe.Client](ctx).Refunds.Create(...)`). `entflow.TryUse[T](ctx) (T, error)` is available for callers who want to handle it. **The executor recovers panics at every step boundary and converts them into a `*entflow.StepError`**, so a DI wiring bug fails the run rather than killing the worker process. This recover behavior is not optional garnish — it is what makes the ergonomic panic safe in a long-running worker. — **Reversibility:** costly — flipping `Use` to a two-value return later rewrites every activity body.
- **D-17:** `Codec[In]` is supplied as a **construction option, not an interface the input type must implement**: `entflow.New[In]("Name")` defaults to a JSON codec shipped in core; `entflow.New[In]("Name", entflow.WithCodec(c))` overrides it. Rationale: §3.6 requires that inputs may originate from RPC, cron, CLI, tests, or another flow's outbox event — an interface-on-the-type design would be unimplementable for protobuf messages and third-party structs the user does not own. — **Reversibility:** one-way — this is `New`'s published signature; every flow declaration calls it.
- **D-18:** Phase 1 exercises the codec round-trip in tests even though nothing is persisted yet, so the interface is locked and proven before Phase 2 depends on it for run-row storage.

### Extractability Invariant and Test Strategy

- **D-19:** CORE-11 becomes a **machine-checked invariant in Phase 1**, not a design intent. Two mechanisms: (a) the builder stores step data in an immutable `meta.FlowMeta` snapshot produced by `flow.Meta()`, with closures held in a parallel structure the metadata path structurally cannot reach; (b) a test that builds flows from the fixture schema, calls `Meta()` and `Describe()`, and asserts a closure-invocation counter is exactly zero. This is the single assumption Phase 5's entity-injection spike depends on — STATE.md carries it as a cross-phase blocker.
- **D-20:** Phase 1 additionally writes a **golden snapshot of the builder-chain call shapes** (which methods are chained, in what order, with what argument kinds). Phase 5 must AST-parse exactly this surface from unparsed source; the snapshot tells it precisely what it has to handle and fails loudly if Phase 1's surface drifts afterward. Phase 1 does **not** attempt AST extraction itself — it proves only the runtime half (metadata available without evaluating closures).
- **D-21:** Phase 1's DB-step tests run against **in-memory SQLite** (`modernc.org/sqlite`, CGo-free, test-only dependency). Postgres and testcontainers arrive in Phase 2 with the worker. Rationale: Phase 1 has no claim query, so SQLite's total absence of `SKIP LOCKED` is irrelevant here, and a CGo-free driver keeps CI fast. The test-only status must be preserved by D-04's dependency check (which inspects the non-test build only).
- **D-22:** Phase 1 does **not** ratify the production dialect support matrix — that is a Phase 2 decision (DUR-08), made when the claim query is designed. Using SQLite for Phase 1 tests must not be read as a statement about production support.

### Claude's Discretion

Auto mode selected the recommended option for every question above; the user did not constrain any of them. Areas where the planner retains latitude:

- Internal representation of the step graph (adjacency list vs edge set) — invisible to the public API.
- Exact `Describe()` output formatting, as long as it is data-shaped and covers step, kind, deps, transition claims, emit topics, and retry policy.
- Error type hierarchy beneath `*entflow.StepError`.
- Whether `internal/testdata/ent` uses the design document's `Order`/`CancelOrder` example verbatim or a trimmed variant — the example in `entflow.md` §3.1 is the reference.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project intent (ground truth)
- `entflow.md` — the full design document. §3.1 (flow declaration, the four design commitments), §3.2 (step kinds and their safety rules), §3.3 (dependencies and conditions), §3.6 (input codec), §3.7 (dependency injection) are directly in Phase 1 scope. §9 defines the entconnect decoupling contract behind META-01/META-02.
- `.planning/PROJECT.md` — Core Value, Constraints, and the Key Decisions table.
- `.planning/REQUIREMENTS.md` — CORE-01 through CORE-12, SM-01, META-01, META-02 are this phase's contract.
- `.planning/ROADMAP.md` §"Phase 1: Runtime Core" — goal and the five success criteria.

### Research (read before planning)
- `.planning/research/SUMMARY.md` — §"Implications for Roadmap" → Phase 1 entry; §"Gaps to Address" (AST-extractability is named as an unproven mechanism this phase must guard).
- `.planning/research/STACK.md` — Go version, exact ent pin, `text/template` vs alternatives, testcontainers-vs-SQLite guidance, module-layout precedent (entcache standalone vs contrib bundled), `pgregory.net/rapid` for later property tests.
- `.planning/research/ARCHITECTURE.md` — component decomposition and the seams; the validation of ent's `Hooks()`/`Policy()` data-code split that D-19 depends on; the explicit invariant "codegen must never infer facts by inspecting closure bodies."
- `.planning/research/PITFALLS.md` — Pitfall 4 (two-pass bootstrap / AST-vs-reflection extraction, mapped to this phase as a design constraint) and Pitfall 6 (`Result[T]` stringly-typed decay, which D-11's error returns and Phase 6's GEN-06 together mitigate).
- `.planning/research/FEATURES.md` — the anti-features list, which bounds what must NOT creep into the builder API (no DSL, no unlimited retry knobs).

### Upstream source (the de-facto documentation)
- `entgo.io/ent` — `entc/gen` (Template, Graph, Type, Field), `schema/field`, `schema/mixin`, and the `ent/runtime` cycle-breaking package. Ent's extension API is under-documented; entgql's and entproto's source is the real reference.
- `entgo.io/contrib/entgql/extension.go` and `entgo.io/contrib/entproto/extension.go` — read for extension-shape precedent. **Note the correction from research:** entproto does NOT inject entities; it only post-processes an already-built graph. Do not model Phase 5 on it.

### External docs
- No external ADRs or specs exist for this project — `entflow.md` is the sole design artifact, and the decisions above supplement it.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

None — this is a genuinely greenfield repository. At the time of this discussion the repo contains `entflow.md`, `.planning/`, and `.claude/`. There is no `go.mod`, no Go source, and no `.planning/codebase/` map (nothing to map yet).

Phase 1 therefore includes project bootstrap: `go mod init`, the initial package skeleton, the fixture ent schema, and CI wiring for `go test ./...`.

### Established Patterns

No in-repo patterns yet. The patterns Phase 1 must **follow from upstream** rather than invent:

- **ent's `Hooks()` / `Policy()` closure-return shape** — `Flows()` deliberately mirrors it. Whatever ent does for hook registration and `ent/runtime` cycle-breaking is the model for how flows bind at runtime.
- **ent's two-pass bootstrap** — schema files importing generated packages is normal and expected in ent; it is not a smell to design around.
- **The Ecto.Multi shape** — the design document's stated inspiration for Phase 1's all-or-nothing single-transaction execution.

### Integration Points

- **`*ent.Tx` / `*ent.Client`** — the only surface Phase 1 touches. Every mutation flows through the transactional client so entity hooks and privacy policies fire normally.
- **`meta` package** — the one-directional, one-way boundary to entconnect (META-01). Phase 1 establishes it; nothing consumes it yet, so its shape is set purely by what codegen (Phase 5/6) and entconnect will need: flow name, owning entity, `In`/`Out` types, step topology.
- **The `Exec(ctx, tx, in)` seam** — Phase 2's worker calls into exactly this. Treat its signature as an internal contract with the next phase, not a private detail.

</code_context>

<specifics>
## Specific Ideas

- The worked example is the `Order` / `CancelOrder` flow from `entflow.md` §3.1, verbatim where practical — `UpdateSelf("cancel", Transition("cancelled"), ...)`, `Activity("refund", When(SelfWas("paid")), Retry(Backoff(5, time.Second, time.Minute)), ...)`, `Emit("order.cancelled", After("cancel"))`. Under D-13 the Activity and Emit steps in this example are declarable and appear in `Describe()` output, but executing the flow returns `ErrRequiresDurableRun` until Phase 3/4. That is the intended Phase 1 demo: *the declaration compiles and describes correctly, the unsafe execution path does not exist yet.*
- `Describe()` doubles as the dry-run surface (design doc §6). Its output should read as data a human can diff, since it is also the artifact Phase 5's golden tests will snapshot.
- The "vibe-coding guardrail" framing from `entflow.md` §2 is the acceptance lens for API design decisions in this phase: for each API choice, ask whether wrong generated code could still commit. If yes, the design is wrong.

</specifics>

<deferred>
## Deferred Ideas

- **`entflowtest` package** — synchronous flow execution against a test DB with mocked DI, for ordinary unit-test ergonomics distinct from the crash-simulation release gate. Research flagged this as a real gap. Tracked as DX-01 in REQUIREMENTS.md v2; do not build it in Phase 1 beyond whatever the phase's own tests need.
- **Generated per-flow typed result structs** — the replacement for `Result[T](ctx, "step")`. Open question #2 in the design doc, DX-03 in v2. Phase 1 locks the stringly-typed shape deliberately; revisit only after real flows show it insufficient.
- **Mermaid diagram emission from `Describe()` data** — DX-02, cosmetic, no urgency.
- **Durable timers / `entflow.Sleep`** — TIME-01 in v2. Research recommends it as the first fast-follow after v1, not part of any current phase.
- **Automatic flow discovery via reflection over the schema package** — deliberately deferred to codegen (Phase 5) per D-06. If it turns out hand-registration is painful enough to hurt Phase 1 adoption, that is a signal to reconsider, not something to pre-build.
- **Production dialect support matrix** — Phase 2 (DUR-08), not Phase 1. See D-22.
- **Activity lease / heartbeat mechanism** — unspecified in the design document; STATE.md carries it as a Phase 3 blocker requiring its own design pass.

</deferred>

---

*Phase: 1-Runtime Core*
*Context gathered: 2026-08-08*
