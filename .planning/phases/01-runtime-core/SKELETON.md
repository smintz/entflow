# Walking Skeleton — entflow

**Phase:** 1 (Runtime Core)
**Generated:** 2026-08-08

> **Adaptation note.** The standard GSD skeleton template is written for web applications
> (routing, UI interaction, dev deployment). entflow is a **Go library** with no HTTP surface,
> no UI, and no deployment target. The rows below are the honest library equivalents, not a
> web-app vocabulary mapped onto a library. Fabricated routing/UI/deployment rows were
> deliberately omitted rather than padded.

## Capability Proven End-to-End

A developer declares a flow on an ent schema via `Flows()`, and that flow — executed against a
real in-memory SQLite ent client inside a caller-supplied `*ent.Tx` — actually commits its
mutation to the database.

Concretely, the thinnest slice that touches every layer of this phase:

```
go mod init  →  fixture ent schema (status enum + Transitions annotation + Flows())
             →  ent codegen (two-pass bootstrap)
             →  entflow.New[In] / entflow.UpdateSelf / Flow interface / FlowsOf
             →  flow.Exec(ctx, tx, in)
             →  real modernc.org/sqlite ent client
             →  assertion that Order.status is 'cancelled' AFTER commit
```

Everything else in Phase 1 — the remaining six DB-step constructors, Activity/Emit
declaration, the DI registry, `Codec`, `Result[T]`, `Describe()`, the `meta` package, the
extractability invariant tests, the builder-chain golden snapshot, and the META-02 dependency
gate — is expansion built out from this proven spine.

## Architectural Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Module path | `github.com/smintz/entflow`, single module at repo root | D-01. Becomes the published import path. Satellite modules (`relay/nats`, River adapter) added only when their phases arrive, never folded into core — this is what keeps META-02 literally true. |
| Language / toolchain | Go 1.25 (`go` directive), verified against local Go 1.25.1 | D-03 / STACK.md. Generics are load-bearing for `New[In]`, `Use[T]`, `Codec[In]`, `Result[T]`. |
| ORM / platform | `entgo.io/ent` pinned to exact tag `v0.14.6` | D-03. Never floating, never a pseudo-version. ent's extension API is stable-in-practice, not stable-in-promise; every minor bump is a deliberate, tested upgrade. |
| Package layout | root `entflow` + `meta/` + `internal/testdata/ent` | D-02. Root has zero codegen dependency. `meta/` is stdlib-only — it is the one package a *different* module (entconnect) imports, so its isolation is mechanical, not conventional. `worker/` and `entc/` do not exist yet. |
| Fixture location | `internal/testdata/ent` (under a `testdata` path segment) | **VERIFIED this session:** the Go tool skips `testdata` directories when matching `./...`, yet packages inside remain importable by explicit path from `_test.go`. This means the generated ent client (and its unavoidable `ariga.io/atlas` transitive tree) is automatically excluded from `go list -deps ./...`, which resolves META-02's scoping question structurally instead of by hand-filtering. |
| Builder surface shape | Package-level generic functions taking `*FlowOf[In]` as the first argument; step closure third; variadic `StepOption` last | Forced by Go's "method must have no type parameters" rule. **VERIFIED by local compilation:** DB-step constructors need `TX` and `Ent` type parameters independent of `In`, and `Activity` needs `Out` — so *no* step constructor can be a method on `*FlowOf[In]`. Gated by a `checkpoint:decision` in Plan 01 because it is the published surface every user schema is written against. |
| Flow type naming | `entflow.Flow` = the schema-facing interface; `entflow.FlowOf[In]` = the generic builder returned by `New[In]` | D-05 and D-07 collide: one package cannot hold both an interface `Flow` and a generic struct `Flow[In]`. Resolved in favour of D-05's literal `Flows() []entflow.Flow`, because that line appears in every user schema and must read as a peer of `Hooks() []ent.Hook`. Gated in the same checkpoint. |
| Transaction ownership | `Exec(ctx, tx, in)` neither opens nor commits; `RunInTx(ctx, f, client, in)` is the sugar that does | D-08. `Exec` is the seam Phase 2's worker calls into — treat its signature as a contract with the next phase, not a private detail. |
| Test database | in-memory SQLite via `modernc.org/sqlite` (CGo-free), **test-only** | D-21. Driver registers as `"sqlite"`, NOT `"sqlite3"`. Postgres + testcontainers arrive in Phase 2 with the worker. Per D-22 this is explicitly **not** a statement about production dialect support. |
| Flow discovery | Explicit registration via `entflow.FlowsOf(schema any) []Flow` | D-06. Mirrors what ent's generated `ent/runtime` package does automatically for `Hooks()`. Automatic discovery is codegen's job (Phase 5). |
| Golden-file tooling | Hand-rolled `-update` flag + `os.WriteFile` (~15 lines), no `sebdah/goldie` dependency | Claude's Discretion per RESEARCH.md. Adding a test-only dependency for something this small works against META-02's posture. Revisit in Phase 6 if TEST-04's suite justifies it. |

## Stack Touched in Phase 1

Library-appropriate substitutions for the template's web-app checklist:

- [ ] Module scaffold — `go mod init`, exact ent pin, `go build`/`go vet` clean
- [ ] Schema layer — a real ent schema with a status enum carrying a `Transitions` annotation
- [ ] Codegen bootstrap — ent's two-pass flow (generate, then add `Flows()` referencing generated types, then regenerate)
- [ ] Declaration layer — `Flows()` on the fixture schema returning a real `entflow.Flow`
- [ ] Execution layer — `Exec` running a DB step inside a caller-supplied `*ent.Tx`
- [ ] Database — one real write, committed, then read back and asserted
- [ ] CI — `go build ./... && go vet ./... && go test ./...` green on push (this is the library's equivalent of "running on a dev environment")

## Out of Scope (Deferred to Later Slices)

Explicit, so later phases do not re-litigate Phase 1's minimalism:

- Run-row persistence, `flow.Start()`, run handles — Phase 2 (DUR-01/DUR-02)
- Worker poller, `FOR UPDATE SKIP LOCKED` claim query, crash-resume, crash-simulation harness — Phase 2
- Executing Activities (three-beat protocol, idempotency keys, leases) — Phase 3. Activities are **declarable** in Phase 1; `Exec` refuses with `ErrRequiresDurableRun` (D-13)
- Executing Emits (outbox row, relay, NATS) — Phase 4. Same declarable-not-executable treatment
- Any code generation — the `entc/` package does not exist in Phase 1 (Phases 5-6)
- The transitions-enforcing hook and the bidirectional cross-validator — Phase 6 (SM-02/SM-03/SM-04). Phase 1 ships the annotation *type* and proves it is readable off a loaded `gen.Graph` (D-14)
- AST extraction of the builder chain — Phase 5. Phase 1 proves only the runtime half of D-20 (metadata available without evaluating closures) and snapshots the call shapes Phase 5 must parse
- Postgres, testcontainers, and the production dialect support matrix — Phase 2 (DUR-08, D-22)
- `entflowtest` ergonomics package — DX-01, v2

## Subsequent Slice Plan

Each later phase adds one vertical slice on top of this skeleton without altering its
architectural decisions:

- **Phase 2 — Durability:** the same flow, now started as a persisted run row that survives a
  worker `SIGKILL` and resumes to the same terminal state against real Postgres.
- **Phase 3 — Activities:** the `refund` Activity in the fixture flow stops returning
  `ErrRequiresDurableRun` and actually calls an external service exactly-once-in-outcome.
- **Phase 4 — Outbox:** the `order.cancelled` Emit stops returning `ErrRequiresDurableRun` and
  actually delivers to NATS JetStream, and can start another flow.
- **Phase 5 — Codegen (injection spike):** the hand-written run schema of Phase 2 is injected
  automatically from the same `Flows()` declaration this skeleton established.
- **Phase 6 — Codegen (cross-validation + runner):** the `Transitions` annotation this skeleton
  declares becomes an enforced hook and a bidirectional cross-validator.
