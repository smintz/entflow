# entflow

entflow is an [ent](https://entgo.io) extension that makes durable workflows
a schema concern. Workflows are declared inside ent schema files via a
`Flows()` method — a peer of `Fields()`, `Hooks()`, and `Policy()` — with
step bodies written as typed closures inline in the declaration. The
extension injects a persisted "run" entity per flow, generates a worker that
executes runs with crash-resume semantics, and cross-validates workflow
steps against the entity's declared state machine at codegen time.

Positioning: **Temporal-lite for ent** — durable, resumable,
exactly-once-outcome workflows with no additional infrastructure beyond the
database ent already uses.

See [`entflow.md`](entflow.md) for the full design document.

## Status

Runtime-first, codegen-last. Phase 1 (Runtime Core) ships the hand-usable
flow builder API. Phase 2 (Durability) — in progress — adds a persisted run
row, a worker that claims and advances runs with crash-resume semantics, and
the ratified dialect support matrix below.

## Database dialect support

entflow's worker needs a claim query that cannot be written portably across
SQL dialects. See [`docs/dialects.md`](docs/dialects.md) for the ratified
support matrix — which dialects are first-class, which are compatible but
uncertified, and which are development/test only.

## Deploying the worker

The unit of ownership in entflow's durable-run model is the **run claim**,
not the worker (see "The worker is a pool, not a singleton" in
[`docs/dialects.md`](docs/dialects.md)). The general shape is N
interchangeable claim goroutines against a shared run table, in one or more
processes — everything else is a special case of that:

- **In-process, alongside an API server:** `go w.Run(ctx)` in the same
  binary that serves requests. `entflow.Start` and the worker share the
  same `*entflow.Engine`, so a run started by a request handler is claimed
  by the same process's own worker (or another process's) with no extra
  wiring.
- **A dedicated worker binary:** a short `main` that constructs the exact
  same `worker.New`, running as its own deployment. See
  [`internal/testdata/crashworker`](internal/testdata/crashworker) for a
  complete worked example — it is also plan 02-08's crash-simulation kill
  target, so the topology it documents is the one actually under test.

Neither topology is "the default" and neither is "the advanced option": no
code path anywhere keys behavior on worker identity. A run belongs to
whichever transaction currently holds its row lock, for exactly one step,
and to nothing else in between — a run started by one process can be, and
often will be, finished by a completely different one.

**Connection pool:** when running the worker in-process alongside an API
server, give the worker its own database client/connection pool rather than
sharing the request-handling path's pool — the two workloads have very
different connection-hold-time profiles (short request-scoped queries vs. a
worker's longer claim-execute-advance transactions), and `worker.Options`
accepts a client the same way any other caller does. Sharing one pool
between them is a latency trap under load, not a correctness problem.

## The crash-simulation release gate

"No public release without the deterministic crash-simulation harness
passing" — this is the sentence every later phase quotes, so it is worth
being precise about what it certifies.

The harness has two tiers, both certifying PostgreSQL only:

- **Tier 1** (`TestCrashSimTier1`, `crashsim_test.go`) — fast, in-process,
  deterministic. It enumerates every logical crash point the claim path
  produces from the multi-step `ProcessOrder` fixture flow's own step graph
  (`internal/crashpoint.Matrix`, D-56 — never hand-listed, so a step added
  to the flow extends coverage automatically) and, for each one, installs a
  hook (`internal/crashpoint.Install`) that aborts the claim exactly as a
  crash would, asserts the step's effect and its progress record are
  either both present or both absent (never one without the other), then
  resumes the run with a different worker instance and asserts its
  terminal state, owning entity, and effect counter all equal an uncrashed
  baseline EXACTLY — the exact-equality check on the effect counter is what
  catches a run that duplicated an effect and reached the right state
  anyway, which terminal-state equality alone would wave through.
- **Tier 2** (`TestCrashSimTier2`, `crashsim_tier2_test.go`) — fewer cases,
  but the only tier that tests the claim the release gate actually makes:
  it terminates a REAL `internal/testdata/crashworker` subprocess with an
  uncatchable signal (`SIGKILL`), against a REAL Postgres, at a small,
  explicitly chosen set of crash points (the first step's
  post-effect-pre-commit boundary, the last step's, and one mid-flow
  boundary). Synchronization is on two observed facts together — a
  sentinel file the subprocess writes atomically right before it blocks at
  the armed crash point, and the run's own committed database state — never
  a sleep (D-53).

Both tiers run against a real Postgres container via
`internal/testdata/pgtest` (`testcontainers-go`). When Docker is
unavailable, both tiers skip with a message naming Docker; the
`ENTFLOW_REQUIRE_CRASHSIM=1` environment variable — the release-gate CI
job's own setting — turns that skip into a failure instead, so a
Docker-absent environment can never masquerade as "the harness passed"
(D-54). A run that executes zero crash points also fails outright, for the
identical reason.

```sh
# Ordinary local run: skips loudly if Docker is unavailable.
go test ./... -run 'TestCrashSimTier1|TestCrashSimTier2' -v

# The release-gate job's own invocation: a Docker-absent environment fails
# instead of skipping.
ENTFLOW_REQUIRE_CRASHSIM=1 go test ./... -run 'TestCrashSimTier1|TestCrashSimTier2' -v
```

Reading "the crash-simulation harness passes" as a broader claim than this
is exactly the failure mode the harness's own `t.Logf` report exists to
prevent: its final line names which tiers ran, against which dialect, how
many crash points were enumerated and executed, how many were skipped and
why, and states explicitly that the gate certifies PostgreSQL only — never
MySQL or SQLite (see "Database dialect support" above).

## Constraints

- entflow depends on `ent` only — never protobuf, Connect, HTTP, or
  descriptor machinery.
- No infrastructure beyond the database ent already uses. The database is
  the queue.
- No public release without the deterministic crash-simulation harness
  passing against PostgreSQL.
