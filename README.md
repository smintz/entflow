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

## Constraints

- entflow depends on `ent` only — never protobuf, Connect, HTTP, or
  descriptor machinery.
- No infrastructure beyond the database ent already uses. The database is
  the queue.
- No public release without the deterministic crash-simulation harness
  passing against PostgreSQL.
