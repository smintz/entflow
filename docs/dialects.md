# Dialect support matrix

entflow's worker claims a run row with a query that cannot be written
portably across SQL dialects: `SELECT ... FOR UPDATE SKIP LOCKED` on
PostgreSQL and MySQL, and a plain `SELECT` inside an immediate-mode
transaction on SQLite. This document ratifies (D-35) exactly which
dialects entflow supports, what "supported" means for each one, and where
the mutual-exclusion guarantee the whole worker/queue story rests on comes
from. `worker.New` reads this table at construction — see "Construction-time
validation" below — so a mismatch is a loud, named error, never a run
silently picked up twice.

| Dialect | Status | Claim mechanism | Certified by the crash-simulation release gate? |
|---|---|---|---|
| **PostgreSQL 12+** | First-class | `SELECT ... FOR UPDATE SKIP LOCKED` | **Yes** — the only dialect the release gate runs against. |
| **MySQL 8.0.1+** | Compatible, uncertified | `SELECT ... FOR UPDATE SKIP LOCKED` | No. The claim query is valid and `worker.StrategyForDialect("mysql")` returns a working strategy, but the release gate never runs against MySQL. "Works, not certified at scale." |
| **SQLite** | Development and test only, single worker | Plain `SELECT`, relying on a `BEGIN IMMEDIATE` transaction | No — and never will be presented as a production multi-worker option. |
| Anything else (including Gremlin, and any dialect string `worker.New` does not recognize) | Refused at startup | — | — |

## PostgreSQL — first-class

`FOR UPDATE SKIP LOCKED` has been in PostgreSQL since 9.5. Every serious
Postgres-backed job queue (River, gue, Solid Queue, good_job) builds on
exactly this primitive, and it is the only dialect entflow's deterministic
crash-simulation harness (the project's release gate) runs against. When
this document or the project says "the crash-simulation harness passes,"
that claim is being made about PostgreSQL and nothing else.

## MySQL — compatible, uncertified

MySQL 8.0.1+ supports `SELECT ... FOR UPDATE SKIP LOCKED` too, and
`worker.StrategyForDialect("mysql")` builds the same query shape with
MySQL's own `?` placeholder syntax. What MySQL does **not** have is a
usable equivalent of PostgreSQL's partial index.

**Concrete limitation, named plainly: MySQL has no partial index.**
`entflow.RunMixin`'s claim-predicate index (`state`, `retry_after`) is
declared with `entsql.IndexWhere`, restricting it to the two claimable
states so the worker's claim query never scans a terminal row. MySQL has no
equivalent construct — the schema ships no MySQL-specific index variant, so
on MySQL the claim query's performance degrades faster as terminal
(`done`/`cancelled`/`failed:<step>`) rows accumulate, because every row in
the table is in that index whether it's claimable or not. This is
documented operational guidance, not an oversight: if you run entflow
against MySQL at any real volume, plan for terminal-row pruning sooner than
you would on PostgreSQL.

Because of this gap, and because the release gate has never run against it,
MySQL is "works" — not "works exactly like PostgreSQL."

## SQLite — development and test only, single worker

SQLite has no row-level locking at all: no `FOR UPDATE`, no `SKIP LOCKED`.
`worker.SQLiteStrategy()` claims a run with a plain `SELECT`, correct **only
because** the surrounding transaction is opened in immediate mode
(`_txlock=immediate` on the connection DSN, or an equivalent `BEGIN
IMMEDIATE`). SQLite's whole-database write serialization under an immediate
transaction supplies the same mutual exclusion `SKIP LOCKED` supplies
elsewhere — but only for a single claiming goroutine. A second, concurrent
claim goroutine gets no additional protection from this strategy: it would
simply block behind (or fail against) the first goroutine's write lock,
which is not the concurrent-claim behavior the ratified matrix promises for
Postgres and MySQL.

**`worker.New` refuses `Concurrency > 1` on SQLite**, returning an error
wrapping `worker.ErrSQLiteConcurrency`, rather than silently clamping the
value to one. A degraded dialect must produce a named, loud startup error —
never an implicit downgrade a developer discovers only from duplicated
effects in production.

SQLite is supported at all so that ent's own quickstart audience — anyone
following ent's standard in-memory SQLite tutorial path — meets a clear,
documented single-worker story instead of an opaque SQL error the first time
they try to run entflow's worker against it. It is never presented, here or
anywhere else in entflow's documentation, as a production multi-worker
option.

## Everything else — refused at startup

Any dialect string `worker.New` does not recognize — including Gremlin, or
a simple typo in the dialect string — is refused at construction with an
error wrapping `worker.ErrUnsupportedDialect`, naming the offending dialect
string and pointing back at this document. See "Construction-time
validation" below for why this happens at `New`, never lazily.

## Construction-time validation

`worker.New` resolves `Options.Dialect` into a `worker.ClaimStrategy`
**at construction**, never lazily at the first claim (D-37). Two refusals
are named, distinct sentinel errors:

- An unrecognized dialect returns an error wrapping `worker.ErrUnsupportedDialect`.
- SQLite with `Concurrency > 1` returns an error wrapping `worker.ErrSQLiteConcurrency`.

Both checks are pure — neither makes a database round trip — so a
misconfigured worker fails the moment it is constructed, at the call site a
developer is already looking at, rather than resurfacing as a mysterious
first-claim error deep in a poll loop's logs.

## The worker is a pool, not a singleton

The unit of ownership in entflow's durable-run model is the **run claim**,
not the worker. A run belongs to whichever transaction currently holds its
row lock, for the duration of exactly one step, and to nothing else in
between. No code path may key behavior on worker identity: there is no
worker-ID column on the run row, no in-memory run registry, and no
assumption anywhere that the process which started a run is the process
that finishes it — a run started by one worker process can be, and often
will be, finished by a completely different one after a crash or a restart.

`worker.Worker` is one process-local pool of `Options.Concurrency`
interchangeable claim goroutines. "One worker" is simply the
`Concurrency: 1` case — exactly what SQLite is forced into by
`ErrSQLiteConcurrency` above. Running the worker in-process alongside an API
server (`go w.Run(ctx)` in the same binary) and running it as a dedicated
worker binary are two call sites of the exact same constructor, with no
behavioral difference between them; neither topology is privileged in the
API, in this document, or in entflow's tests.

## See also

- [`entflow.md`](../entflow.md) §5.2 — the worker loop design, and the
  "advance state in the same transaction" rule this matrix's claim
  mechanisms all implement.
- [`README.md`](../README.md) — project overview.
