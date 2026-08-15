package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	// Registers the "pgx" database/sql driver name — the only way entflow's
	// worker (through sql/execquery's generated ExecContext/QueryContext)
	// and ent's own generated builders share one database/sql connection
	// pool: ent has no non-database/sql driver path (RESEARCH.md).
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/smintz/entflow/internal/testdata/ent"
	// Blank-imported so its init() wires the schema-stitching hook into the
	// generated package's exported Hooks array — mirrors entclient.New's own
	// blank import for the identical reason (order_update.go's withHooks
	// call would otherwise invoke a nil function).
	_ "github.com/smintz/entflow/internal/testdata/ent/runtime"
)

// postgresImage is pinned to the tag already cached on entflow's CI/dev
// hosts (see the executor environment notes) so Start never needs a fresh
// pull. Bump deliberately, not incidentally.
const postgresImage = "postgres:16-alpine"

// sharedContainer holds the one Postgres container Start shares across every
// test in a package (D-54: one container per package, fresh schema per
// test). container/connErr are set exactly once, by containerOnce.Do.
var (
	containerOnce sync.Once
	container     *postgres.PostgresContainer
	containerErr  error
	baseDSN       string
	adminDB       *sql.DB

	schemaCounter atomic.Uint64
)

// startContainer runs the shared Postgres container exactly once per test
// binary, using BasicWaitStrategies (log-then-port) rather than a sleep, and
// opens the one admin *sql.DB every test's schema-create/drop pair reuses.
func startContainer(t *testing.T) {
	t.Helper()
	containerOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// Deliberately no testcontainers.WithLogger(log.TestLogger(t)) here:
		// the container is shared across every test in the package via
		// containerOnce, so binding its logger to whichever test happens to
		// win the Do race would call that test's t.Log after that test has
		// already returned — a documented "Log in goroutine after Test has
		// completed" panic risk. The package-default logger is used instead.
		c, err := postgres.Run(ctx, postgresImage, postgres.BasicWaitStrategies())
		if err != nil {
			containerErr = fmt.Errorf("pgtest: starting postgres container: %w", err)
			return
		}
		container = c

		dsn, err := c.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			containerErr = fmt.Errorf("pgtest: resolving connection string: %w", err)
			return
		}
		baseDSN = dsn

		db, err := sql.Open("pgx", baseDSN)
		if err != nil {
			containerErr = fmt.Errorf("pgtest: opening admin connection: %w", err)
			return
		}
		if err := db.PingContext(ctx); err != nil {
			containerErr = fmt.Errorf("pgtest: pinging admin connection: %w", err)
			return
		}
		adminDB = db
	})
}

// Start returns a ready *ent.Client backed by a fresh Postgres schema inside
// the package's one shared container: one container per test package (D-54),
// a fresh schema per test for isolation. The schema is migrated
// (client.Schema.Create) before Start returns, and both the schema and the
// client are torn down via t.Cleanup.
//
// Callers should gate on SkipUnlessDockerAvailable (or call it themselves)
// before Start — Start itself does not skip; a caller that wants the D-54
// skip-vs-fail policy applied must ask for it explicitly, which
// SkipUnlessDockerAvailable does. Start still fails the test outright (never
// silently skips) if Docker was reported available but the container
// nonetheless fails to start, since that is a real infrastructure failure,
// not an absence of Docker.
func Start(t *testing.T) *ent.Client {
	t.Helper()

	startContainer(t)
	if containerErr != nil {
		t.Fatalf("pgtest: %v", containerErr)
	}

	ctx := context.Background()
	schemaName := freshSchemaName(t)

	if _, err := adminDB.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA %s`, quoteIdent(schemaName))); err != nil {
		t.Fatalf("pgtest: creating schema %s: %v", schemaName, err)
	}
	t.Cleanup(func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminDB.ExecContext(dropCtx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, quoteIdent(schemaName))); err != nil {
			t.Logf("pgtest: dropping schema %s: %v", schemaName, err)
		}
	})

	// search_path is passed as a pgx runtime (startup) parameter: every new
	// physical connection this *sql.DB opens against the pool sends it at
	// connect time, so every statement — including sql/execquery's raw
	// QueryContext claim query, which never sees an explicit schema
	// qualifier — resolves against this test's own isolated schema.
	testDSN := baseDSN + "&search_path=" + schemaName

	db, err := sql.Open("pgx", testDSN)
	if err != nil {
		t.Fatalf("pgtest: opening test connection: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	drv := entsql.OpenDB(dialect.Postgres, db)
	client := ent.NewClient(ent.Driver(drv))
	t.Cleanup(func() {
		_ = client.Close()
	})

	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("pgtest: migrating schema %s: %v", schemaName, err)
	}

	return client
}

// freshSchemaName derives a unique, valid Postgres identifier from t's name
// plus a monotonically increasing counter, so two tests (or two subtests
// sharing a prefix) never collide even when run in parallel.
func freshSchemaName(t *testing.T) string {
	n := schemaCounter.Add(1)
	return fmt.Sprintf("pgtest_%s_%d", sanitizeIdent(t.Name()), n)
}

// sanitizeIdent lowercases s and replaces every rune that cannot appear in
// an unquoted Postgres identifier with an underscore, collapsing the result
// to a bounded length so it never exceeds Postgres's 63-byte identifier
// limit once the "pgtest_" prefix and numeric suffix are added.
func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	const maxLen = 40
	if len(out) > maxLen {
		out = out[:maxLen]
	}
	return out
}

// quoteIdent double-quotes a Postgres identifier, doubling any embedded
// quote — schemaName is entflow-generated (sanitizeIdent's own output plus
// digits and underscores), never user input, but this stays defensive
// rather than trusting that invariant silently.
func quoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}
