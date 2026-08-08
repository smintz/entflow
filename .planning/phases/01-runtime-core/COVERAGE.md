# Phase 1 — API Coverage Declaration

**Generated:** 2026-08-08 (planning)
**Phase:** 1 — Runtime Core

## Declaration

**No external API integration: Phase 1 builds entflow's own core library and integrates no
third-party service API.**

The `workflow.api_coverage_gate` config key is enabled project-wide, so this declaration is
recorded rather than a coverage matrix being fabricated.

### Why the gate does not apply here

- Phase 1 has **no network surface at all** — no HTTP client, no gRPC client, no message-broker
  client, no webhook receiver, no outbound request of any kind. The full non-test dependency
  graph is asserted by Plan 04's `deps_test.go` to contain only the standard library, entflow's
  own module, and `entgo.io/ent` with its derived transitive requirements.
- The one place a third-party service would normally enter — an Activity step calling an external
  provider — is **declarable but not executable** in Phase 1 per decision D-13. `Exec` returns
  `ErrRequiresDurableRun` for any flow containing an Activity or an Emit, before executing any
  step. There is therefore no external call path to cover.
- The only libraries Phase 1 consumes are `entgo.io/ent` (the platform being extended, not an
  integrated service), `modernc.org/sqlite` (test-only database driver), and
  `github.com/stretchr/testify` (test assertions). None exposes a remote API whose surface would
  warrant a coverage matrix.

### Where this changes

| Phase | External API entering scope | Gate becomes applicable |
|---|---|---|
| Phase 3 — Activities | Stripe (and compliant providers generally), via the three-beat protocol and `att.IdempotencyKey()` | Yes — ACT-02/ACT-03 depend on provider-side idempotency-key semantics, including TTL windows |
| Phase 4 — Outbox | NATS JetStream, via the relay's `RelayTarget` interface | Yes — OUT-03 makes JetStream a first-class delivery target, and `Nats-Msg-Id` dedup-window semantics need explicit coverage |

Re-run the API coverage gate when planning Phases 3 and 4. It is correctly inapplicable to
Phase 1.

## Related exclusions recorded during Phase 1 planning

- **Package legitimacy gate** — not applicable. The gate is scoped to npm, pip, and cargo
  ecosystems. Phase 1's dependencies are Go modules, governed instead by exact version pinning
  (D-03), `go.sum` checksums verified against the Go checksum database, and CI's
  `GOFLAGS: -mod=readonly`. Recorded as threat `T-01-SC` in every plan's `<threat_model>`.
- **Schema-push gate** — not applicable. The gate scans for TypeScript/Node ORM patterns
  (Prisma, Drizzle, Payload, Supabase, TypeORM). This project is Go/ent.
- **Assumption-delta detector** — ran during planning, `detected: false`. No action taken.
