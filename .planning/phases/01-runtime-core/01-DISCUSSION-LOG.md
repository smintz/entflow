# Phase 1: Runtime Core - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-08-08
**Phase:** 1-Runtime Core
**Mode:** `--auto` — every question resolved with the recommended option, no user prompts
**Areas discussed:** Module & package layout, Flow declaration & discovery, Phase-1 execution semantics, Step-kind API breadth, DI registry & Codec surface, Extractability invariant & test strategy

---

## Module and Package Layout

| Option | Description | Selected |
|--------|-------------|----------|
| Single module, satellites added later | `github.com/smintz/entflow` at root; NATS/River adapters become separate modules when their phases arrive | ✓ |
| Multi-module from day one | Split core/worker/entc into separate modules immediately | |
| Bundled contrib-style monorepo | One module containing every adapter, entgo.io/contrib style | |

**Choice:** Single module, satellites later.
**Notes:** The "entflow depends on `ent` only" constraint is only literally true if adapters live outside the core `go.mod`. `ariga.io/entcache` is the precedent for an independently-versioned satellite; `entgo.io/contrib` is the counter-precedent (bundled) but it does not carry entflow's dependency constraint.

| Option | Description | Selected |
|--------|-------------|----------|
| `go list -deps` test | Assert the non-test build's package set contains only stdlib + ent | ✓ |
| `depguard` lint config | Enforce import restrictions via golangci-lint | |
| Convention + code review | Document the rule, rely on reviewers | |

**Choice:** `go list -deps` test.
**Notes:** No extra CI tooling; fails inside the normal `go test ./...` run.

---

## Flow Declaration and Discovery

| Option | Description | Selected |
|--------|-------------|----------|
| entflow defines its own `entflow.Flow` | Self-contained; resolves design-doc open question #1 for v0 | ✓ |
| Contribute `ent.Flow` upstream first | Cleanest naming, but blocks Phase 1 on an external process | |

**Choice:** entflow defines its own interface.
**Notes:** Open question #1 in `entflow.md` §10 explicitly names the self-contained option as the v0 assumption.

| Option | Description | Selected |
|--------|-------------|----------|
| Explicit registration in `main.go`/tests | App registers `Flows()` output; `FlowsOf()` helper does the type assertion | ✓ |
| Reflection over the schema package | Runtime walks schema types to find `Flows()` automatically | |
| `init()`-based self-registration | Flows register themselves at package init, `ent/runtime` style | |

**Choice:** Explicit registration.
**Notes:** Phase 1's whole purpose is proving the model works by hand. Reflection is the thing codegen replaces in Phase 5 — building it now would be work thrown away, and `init()` self-registration hides wiring in a way that fights the "schema is the reviewable constitution" thesis.

---

## Phase-1 Execution Semantics

| Option | Description | Selected |
|--------|-------------|----------|
| `Exec(ctx, tx, in)` primitive + `RunInTx` sugar | Caller owns the tx; convenience wrapper opens/commits | ✓ |
| `RunInTx` only | Library always owns the transaction | |
| `Exec` only | Caller always supplies the tx | |

**Choice:** Both, with `Exec` as the primitive.
**Notes:** `Exec` is the seam Phase 2's worker calls into — it must exist as a first-class entry point. `RunInTx` keeps the hand-written demo short.

| Option | Description | Selected |
|--------|-------------|----------|
| Snapshot status at flow entry | `SelfWas` reads a pre-flow snapshot taken inside the tx | ✓ |
| Live re-read per predicate | Each `SelfWas` re-queries current status | |
| Post-mutation value | `SelfWas` sees the value after prior steps ran | |

**Choice:** Snapshot at flow entry.
**Notes:** Chosen specifically because it is the semantics that survives into Phase 2, where the snapshot becomes a persisted run-row column. The other two options would mean `SelfWas` quietly means something different once runs are durable.

| Option | Description | Selected |
|--------|-------------|----------|
| `Result[T]` returns `(T, error)` | Distinct sentinels for unknown-step and type-mismatch | ✓ |
| `Result[T]` panics | Matches a "programmer error" framing, terser call sites | |

**Choice:** `(T, error)`.
**Notes:** PITFALLS.md Pitfall 6 predicts this stringly-typed accessor decays into runtime panics as flows grow. Returning errors keeps the failure inside the run's normal error path; Phase 6's GEN-06 adds the generation-time check on top.

---

## Step-Kind API Breadth

| Option | Description | Selected |
|--------|-------------|----------|
| Full 7-constructor set now | All of `Step`, `CreateSelf`, `UpdateSelf`, `Create`, `Update`, `Query`, `Check` | ✓ |
| Minimal core, sugar later | Ship `Step` + `UpdateSelf` + `CreateSelf`, add the rest on demand | |

**Choice:** Full set.
**Notes:** CORE-03 names them all, and they are thin wrappers over one internal representation — low cost now, and renaming after user schemas exist is a breaking change.

| Option | Description | Selected |
|--------|-------------|----------|
| Declarable, execution refused | `Activity`/`Emit` build and describe; `Exec` returns `ErrRequiresDurableRun` | ✓ |
| Declarable and naively executed | Run activities in-line, single attempt, no idempotency key | |
| Not declarable at all in Phase 1 | Builder rejects them until Phase 3/4 | |

**Choice:** Declarable, execution refused.
**Notes:** The naive path is the dangerous option — it would work in a demo and then get shipped. Refusing execution keeps CORE-04's compile-time guarantee provable and CORE-11's builder data complete, without ever creating a route where an un-idempotent external call can run. Rejecting declaration outright would break `Describe()` completeness and the worked example.

---

## DI Registry and Codec Surface

| Option | Description | Selected |
|--------|-------------|----------|
| Scoped registry injected into ctx | `NewRegistry()` per app/test, `WithRegistry(ctx, reg)` | ✓ |
| Package-level global registry | Single process-wide registry, simplest call sites | |

**Choice:** Scoped.
**Notes:** Parallel tests need independent mock sets; a global would also complicate Phase 2's multi-worker story.

| Option | Description | Selected |
|--------|-------------|----------|
| `Use[T]` panics + `TryUse[T]` + executor recovers | Matches the design doc's example ergonomics, made safe by step-boundary recovery | ✓ |
| `Use[T]` returns `(T, error)` | Uniform with other error paths, but breaks the doc's example | |
| `Use[T]` panics, no recovery | Terse, but a wiring bug kills the worker process | |

**Choice:** Panic + `TryUse` + executor recovery.
**Notes:** The recovery half is what makes this safe — without it, a missing provider would take down a long-running worker. Recorded as non-optional in CONTEXT.md D-16.

| Option | Description | Selected |
|--------|-------------|----------|
| Codec as a construction option | `WithCodec(c)`, JSON default | ✓ |
| Codec as an interface on the input type | `In` implements `MarshalFlow`/`UnmarshalFlow` | |

**Choice:** Construction option.
**Notes:** §3.6 requires inputs from RPC, cron, CLI, tests, and outbox events. An interface-on-the-type design is unimplementable for protobuf messages and third-party structs the user does not own — it would quietly reintroduce the proto coupling the design explicitly rejects.

---

## Extractability Invariant and Test Strategy

| Option | Description | Selected |
|--------|-------------|----------|
| Machine-checked in Phase 1 | `meta.FlowMeta` snapshot + zero-closure-invocation assertion test | ✓ |
| Documented design intent only | Note the rule, verify during Phase 5 | |

**Choice:** Machine-checked now.
**Notes:** STATE.md carries this as an explicit Phase 1 → Phase 5 blocker. If it is only an intent, Phase 5 discovers the violation after four phases of code depends on it.

| Option | Description | Selected |
|--------|-------------|----------|
| Golden snapshot of builder-chain shapes | Phase 1 records the surface Phase 5 must AST-parse | ✓ |
| Attempt AST extraction in Phase 1 | Prove the whole mechanism early | |
| Neither | Leave it entirely to Phase 5 | |

**Choice:** Golden snapshot.
**Notes:** Attempting AST extraction now would drag Phase 5's highest-risk work into Phase 1 without the run row or codegen context that makes it tractable. The snapshot is the cheap middle ground: Phase 5 learns exactly what surface it faces, and drift fails loudly.

| Option | Description | Selected |
|--------|-------------|----------|
| In-memory SQLite (`modernc.org/sqlite`) | CGo-free, fast, test-only dependency | ✓ |
| Postgres via testcontainers | Maximum fidelity, matches Phase 2's harness | |

**Choice:** SQLite for Phase 1.
**Notes:** Phase 1 has no claim query, so SQLite's total absence of `SKIP LOCKED` is irrelevant here. Explicitly recorded (D-22) that this is *not* a statement about production dialect support — DUR-08 decides that in Phase 2.

---

## Claude's Discretion

Auto mode selected the recommended option for all fifteen questions above; the user constrained none of them. Latitude explicitly left to the planner:

- Internal step-graph representation (adjacency list vs edge set).
- Exact `Describe()` output formatting, subject to covering step, kind, deps, transition claims, emit topics, and retry policy.
- Error type hierarchy beneath `*entflow.StepError`.
- Whether the fixture schema uses the design document's `Order`/`CancelOrder` example verbatim or trimmed.

## Deferred Ideas

- `entflowtest` package (DX-01, v2)
- Generated per-flow typed result structs (DX-03, v2; design-doc open question #2)
- Mermaid diagram emission (DX-02, v2)
- Durable timers / `entflow.Sleep` (TIME-01, v2 — research recommends first fast-follow)
- Reflection-based automatic flow discovery (deferred to Phase 5 codegen)
- Production dialect support matrix (Phase 2, DUR-08)
- Activity lease/heartbeat mechanism (Phase 3 blocker, needs its own design pass)
