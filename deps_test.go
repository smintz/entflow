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
// ent allowlist.
const entflowModulePrefix = "github.com/smintz/entflow"

// TestNoTransportDeps is META-02's dependency gate (D-04): entflow's
// non-test dependency graph — computed by running `go list -deps ./...` —
// must contain only the standard library, entflow's own module, and the ent
// module with its transitive requirements. No protobuf, no transport, no
// messaging client.
//
// The allowed set is derived programmatically, never hand-curated:
// 01-RESEARCH.md verified that a naive hand-written allowlist ("only
// packages literally prefixed entgo.io/ent/") is red on day one, because
// ariga.io/atlas, hashicorp/hcl, zclconf/go-cty*, and others are legitimate
// transitive requirements of entgo.io/ent itself, pulled in the instant
// internal/testdata/ent exists as generated, non-test source. Instead this
// test takes whatever entgo.io/ent packages actually appear in entflow's
// dependency graph and runs `go list -deps` on THEM to compute the
// legitimate transitive requirement set — so the gate self-maintains across
// every future ent version bump.
func TestNoTransportDeps(t *testing.T) {
	actual := runGoListDeps(t, "./...")

	var entPackages []string
	for _, pkg := range actual {
		if strings.HasPrefix(pkg, "entgo.io/ent") {
			entPackages = append(entPackages, pkg)
		}
	}
	require.NotEmpty(t, entPackages, "expected at least one entgo.io/ent package in the non-test build graph")

	allowedArgs := append([]string{"list", "-deps"}, entPackages...)
	allowedOut, err := exec.Command("go", allowedArgs...).CombinedOutput()
	require.NoError(t, err, "go %s: %s", strings.Join(allowedArgs, " "), allowedOut)
	allowed := nonEmptyLines(string(allowedOut))

	allowedSet := make(map[string]bool, len(allowed))
	for _, pkg := range allowed {
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
		"entflow's non-test dependency graph must contain only the standard library, its own module, and the ent module with its transitive requirements — found: %s",
		strings.Join(offenders, ", "))
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
