package entflow_test

import (
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// entflowModulePrefix is entflow's own module path — any package under this
// prefix is entflow's own code, always allowed regardless of the derived
// allowlists below.
const entflowModulePrefix = "github.com/smintz/entflow"

// otelPackagePrefix is the package-path prefix TestNoTransportDeps derives
// its tracing allowlist from — see that test's doc comment for why this is
// a derivation seed, not an allowlist entry in itself.
const otelPackagePrefix = "go.opentelemetry.io/otel"

// TestNoTransportDeps is META-02's dependency gate (D-04): entflow's
// non-test dependency graph — computed by running `go list -deps ./...` —
// must contain only the standard library, entflow's own module, the ent
// module with its transitive requirements, and (D-57, plan 02-06) the
// go.opentelemetry.io/otel/trace tracing API with ITS transitive
// requirements. No protobuf, no transport, no messaging client, no
// unrelated tracing surface.
//
// Both allowed sets are derived programmatically, never hand-curated:
// 01-RESEARCH.md verified that a naive hand-written allowlist ("only
// packages literally prefixed entgo.io/ent/") is red on day one, because
// ariga.io/atlas, hashicorp/hcl, zclconf/go-cty*, and others are legitimate
// transitive requirements of entgo.io/ent itself, pulled in the instant
// internal/testdata/ent exists as generated, non-test source. This test
// takes whatever entgo.io/ent packages actually appear in entflow's
// dependency graph and runs `go list -deps` on THEM to compute the
// legitimate transitive requirement set — so the gate self-maintains across
// every future ent version bump.
//
// D-57's amendment (plan 02-06) extends the identical technique to tracing:
// whatever go.opentelemetry.io/otel-prefixed packages entflow's own code
// actually imports (go.opentelemetry.io/otel/trace, plus its own
// attribute/codes/noop dependencies — never the root go.opentelemetry.io/
// otel convenience package; see TestTracingDependencyNarrowness) get run
// through `go list -deps` too, and the result is unioned into the allowed
// set. At the pinned v1.45.0 tag this closure includes exactly one external
// module beyond go.opentelemetry.io/otel/* itself — github.com/cespare/
// xxhash/v2, already a pre-existing (test-only, until now) indirect
// dependency of this project via testcontainers-go, pulled in by otel/
// attribute's own hashing helper. That single addition is why this test
// derives the tracing allowlist exactly as it derives the ent one, rather
// than reusing a hand-picked constant: a hand-picked allowlist would have
// been wrong about xxhash from the moment this plan landed.
//
// This is a deliberate, narrow amendment to a Phase 1 decision (D-04/
// META-02), not a reopening of it: META-02's actual intent is "no
// transport, protobuf, or descriptor machinery," and a tracing API is none
// of those — 02-RESEARCH.md recommends exactly this boundary. The
// alternative considered and rejected was a bespoke entflow-authored tracer
// interface behind a satellite module (mirroring the NATS relay-target
// pattern): that would give every entflow user a worse, hand-rolled API for
// instrumenting a library that go.opentelemetry.io/otel/trace already
// exists to instrument, for no gain in dependency safety this derived,
// self-maintaining gate doesn't already provide. That satellite-module
// shape remains available to a future maintainer who wants to revisit this
// — and only gets more expensive to retrofit with every phase that ships
// against the direct dependency instead.
func TestNoTransportDeps(t *testing.T) {
	actual := runGoListDeps(t, "./...")

	var entPackages []string
	for _, pkg := range actual {
		if strings.HasPrefix(pkg, "entgo.io/ent") {
			entPackages = append(entPackages, pkg)
		}
	}
	require.NotEmpty(t, entPackages, "expected at least one entgo.io/ent package in the non-test build graph")

	var tracingPackages []string
	for _, pkg := range actual {
		if strings.HasPrefix(pkg, otelPackagePrefix) {
			tracingPackages = append(tracingPackages, pkg)
		}
	}
	require.NotEmpty(t, tracingPackages, "expected at least one go.opentelemetry.io/otel package in the non-test build graph (D-57)")

	allowedSet := make(map[string]bool)
	for _, pkg := range runGoListDeps(t, entPackages...) {
		allowedSet[pkg] = true
	}
	for _, pkg := range runGoListDeps(t, tracingPackages...) {
		allowedSet[pkg] = true
	}

	var offenders []string
	for _, pkg := range actual {
		if isStdlib(pkg) || strings.HasPrefix(pkg, entflowModulePrefix) {
			continue
		}
		if !allowedSet[pkg] {
			offenders = append(offenders, pkg)
		}
	}
	sort.Strings(offenders)
	require.Empty(t, offenders,
		"entflow's non-test dependency graph must contain only the standard library, its own module, the ent module, and the go.opentelemetry.io/otel/trace tracing API, each with their transitive requirements — found: %s",
		strings.Join(offenders, ", "))
}

// forbiddenAmbientGlobalPackages are the five packages 02-RESEARCH.md's
// "Load-Bearing Correction to D-57" measured the ambient-global convenience
// API (otel.Tracer(...)/otel.GetTracerProvider(), reached by importing the
// root go.opentelemetry.io/otel package instead of go.opentelemetry.io/
// otel/trace directly) would additionally pull into entflow's non-test
// dependency graph, over and above the trace API's own dependency-free
// closure: a logging facade and its standard-logger binding, a metrics API,
// a context-propagation package, and an auto-instrumentation SDK. Their
// presence here means D-57's exception silently widened.
var forbiddenAmbientGlobalPackages = []string{
	"github.com/go-logr/logr",
	"github.com/go-logr/stdr",
	"go.opentelemetry.io/otel/metric",
	"go.opentelemetry.io/otel/propagation",
	"go.opentelemetry.io/auto/sdk",
}

// TestTracingDependencyNarrowness makes D-57's exception narrowness a fact
// this test enforces, not a claim TestNoTransportDeps' derived allowlist
// could quietly stop enforcing (a derived allowlist is only as narrow as
// what entflow's code happens to import — this test is the independent
// check that what it imports stays narrow). entflow's non-test build graph
// must contain none of forbiddenAmbientGlobalPackages, and must not contain
// the tracing SDK (go.opentelemetry.io/otel/sdk) itself — D-57 sanctions
// the trace API only, never the SDK, and D-58's "defaults to the no-op
// provider from the trace module itself, never the ambient global" is
// exactly the boundary this test machine-checks.
//
// This test intentionally does NOT grant an unconditional prefix exemption
// to go.opentelemetry.io/otel* the way it might be tempting to: a prefix
// exemption would admit the whole family, including the SDK and every
// exporter, which is precisely what this test exists to catch.
func TestTracingDependencyNarrowness(t *testing.T) {
	actual := runGoListDeps(t, "./...")
	actualSet := make(map[string]bool, len(actual))
	for _, pkg := range actual {
		actualSet[pkg] = true
	}

	for _, forbidden := range forbiddenAmbientGlobalPackages {
		require.False(t, actualSet[forbidden],
			"entflow's non-test dependency graph must not contain %q — its presence means the ambient global tracer (otel.GetTracerProvider) was used instead of go.opentelemetry.io/otel/trace directly, silently widening D-57's exception", forbidden)
	}

	for _, pkg := range actual {
		require.False(t, strings.HasPrefix(pkg, "go.opentelemetry.io/otel/sdk"),
			"entflow's non-test dependency graph must not contain the tracing SDK (%q) — D-57 sanctions the trace API only, never the SDK", pkg)
	}
}

// TestDependenciesExcludeTestdataFixture documents, as an executable
// assertion rather than only a comment in a plan, the scoping decision
// 01-RESEARCH.md flagged for this gate: TestNoTransportDeps runs against
// bare `go list -deps ./...` with no manual filtering, and the generated
// fixture client under internal/testdata/ent never enters this set because
// the Go tool itself excludes any path segment named "testdata" from
// wildcard matching — not because a test filters it out after the fact.
func TestDependenciesExcludeTestdataFixture(t *testing.T) {
	for _, pkg := range runGoListDeps(t, "./...") {
		for _, segment := range strings.Split(pkg, "/") {
			require.NotEqual(t, "testdata", segment,
				"package %q must not appear in `go list -deps ./...`'s output — the Go tool is expected to exclude testdata path segments from wildcard matching", pkg)
		}
	}
}

// isStdlib classifies pkg as standard library using the canonical rule: a
// package path's first segment contains no dot. Any other package path has
// a dotted host segment (e.g. "entgo.io", "github.com") naming a module.
func isStdlib(pkg string) bool {
	first := strings.SplitN(pkg, "/", 2)[0]
	return !strings.Contains(first, ".")
}

// runGoListDeps runs `go list -deps` against the given package patterns —
// entflow's non-test build graph, per D-04 — and returns the resulting
// package paths.
func runGoListDeps(t *testing.T, pkgs ...string) []string {
	t.Helper()
	args := append([]string{"list", "-deps"}, pkgs...)
	out, err := exec.Command("go", args...).CombinedOutput()
	require.NoError(t, err, "go %s: %s", strings.Join(args, " "), out)
	return nonEmptyLines(string(out))
}

// nonEmptyLines splits s on newlines, dropping empty lines.
func nonEmptyLines(s string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
