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

## Constraints

- entflow depends on `ent` only — never protobuf, Connect, HTTP, or
  descriptor machinery.
- No infrastructure beyond the database ent already uses. The database is
  the queue.
- No public release without the deterministic crash-simulation harness
  passing against PostgreSQL.
