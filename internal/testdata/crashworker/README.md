# crashworker

`crashworker` is a small, hand-written `main` package with two roles that
are deliberately the SAME binary, not two:

1. **The DUR-09 worked example.** It is what "run entflow's worker as a
   dedicated process" looks like: open a Postgres `*ent.Client`, construct
   the exact `worker.New` an in-process caller constructs, run it, and shut
   it down gracefully on a termination signal. Copy this file's shape into
   a real deployment and it is the whole worker binary.
2. **Plan 02-08's tier-2 crash-simulation kill target.** The
   crash-simulation release gate `SIGKILL`s this exact binary at real crash
   points against a real Postgres. A worked example nobody actually runs is
   a worked example that rots — the documented topology being the one under
   test is the point (see `topology_test.go` at the module root).

Placing it under `internal/testdata` is load-bearing, not incidental: the Go
tool excludes any path segment named `testdata` from wildcard package
matching (`go list -deps ./...`), so the `github.com/jackc/pgx/v5/stdlib`
driver this binary imports never enters entflow's non-test dependency graph
— `TestNoTransportDeps` stays green. See
`internal/testdata/entclient`'s own doc comment for the same pattern applied
to the module's SQLite test client.

## Building

```sh
go build -o /tmp/crashworker ./internal/testdata/crashworker
```

Never commit the compiled binary — it is a build artifact, not source.

## Environment variables

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `ENTFLOW_CRASHWORKER_DSN` | Yes | — | A Postgres connection string, passed straight to `sql.Open("pgx", ...)`. Never logged — only the variable's NAME appears in any error message this binary emits (T-02-19), never its value. |
| `ENTFLOW_CRASHWORKER_FLOWS` | No | every registered flow | Comma-separated flow names, forwarded to `worker.Options.Flows` to restrict which flows this worker claims. |
| `ENTFLOW_CRASHWORKER_CONCURRENCY` | No | `worker.DefaultConcurrency` (4) | Forwarded to `worker.Options.Concurrency`. |
| `ENTFLOW_CRASHWORKER_POLL_INTERVAL` | No | `worker.DefaultPollInterval` (1s) | A Go duration string (e.g. `50ms`), forwarded to `worker.Options.PollInterval`. |
| `ENTFLOW_CRASHWORKER_DRAIN_TIMEOUT` | No | `worker.DefaultDrainTimeout` (30s) | A Go duration string, forwarded to `worker.Options.DrainTimeout` — how long `Shutdown` waits for an in-flight step to commit before cancelling it (D-49). |

An unparseable `POLL_INTERVAL`/`DRAIN_TIMEOUT` or `CONCURRENCY` value falls
back to its default silently — this is a worked example's convenience, not
a production-hardened flag parser.

## Flows

The binary registers every flow `entflow.FlowsOf(schema.Order{})` returns —
the exact call `flow.go`'s own doc comment tells a hand-written in-process
caller to make. Today that is `CancelOrder`; a flow added to
`order_flows.go` in a later plan is picked up automatically, with no edit to
this file.

## Termination

A `SIGTERM` or `SIGINT` drives `worker.Worker.Shutdown` with the configured
drain window — never an abrupt process exit. Plan 02-08's tier-2 harness
additionally arms this binary to sit at a named crash point and then
terminates it with an uncatchable signal (`SIGKILL`), which Shutdown cannot
observe by design — that is the whole point of the crash-simulation gate:
proving the SAME durability guarantee holds whether the process exits
politely or not at all.
