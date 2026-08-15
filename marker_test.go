package entflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
)

// listPkg is the subset of `go list -json`'s per-package output this file
// needs: Dir (absolute package directory) and GoFiles (the package's
// buildable non-test .go files — `go list` already excludes _test.go files
// from this field, so no separate filtering is needed here).
type listPkg struct {
	Dir     string
	GoFiles []string
}

// moduleRoot returns entflow's own module root directory, so this file's
// `go build`/`go list` invocations resolve correctly regardless of the test
// binary's working directory — the same need activity_test.go's repoRoot
// serves for package entflow's internal tests, duplicated here in its own
// small form because that helper lives in the internal test package
// (activity_test.go is `package entflow`) and this file is the external
// `entflow_test` package deps_test.go also uses.
func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").CombinedOutput()
	require.NoError(t, err, "go list -m: %s", out)
	return strings.TrimSpace(string(out))
}

// nonTestGoFiles returns the absolute path of every non-test .go file in
// entflow's module, computed by `go list -json ./...` — which, per
// TestDependenciesExcludeTestdataFixture (deps_test.go), already excludes
// every path with a "testdata" path segment from wildcard matching. This is
// the same "derived, never hand-curated" style deps_test.go established
// (02-CONTEXT.md's "machine-checked invariants over documented ones").
func nonTestGoFiles(t *testing.T) []string {
	t.Helper()
	root := moduleRoot(t)
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err, "go list -json ./...")

	dec := json.NewDecoder(bytes.NewReader(out))
	var files []string
	for dec.More() {
		var p listPkg
		require.NoError(t, dec.Decode(&p))
		for _, f := range p.GoFiles {
			files = append(files, filepath.Join(p.Dir, f))
		}
	}
	sort.Strings(files)
	return files
}

// relPaths converts absolute file paths (as nonTestGoFiles returns) into
// module-root-relative, slash-separated paths, for stable assertions that
// don't embed the checkout's absolute location.
func relPaths(t *testing.T, files []string) []string {
	t.Helper()
	root := moduleRoot(t)
	out := make([]string, len(files))
	for i, f := range files {
		rel, err := filepath.Rel(root, f)
		require.NoError(t, err)
		out[i] = filepath.ToSlash(rel)
	}
	return out
}

// importAlias returns the local identifier node's non-test .go files use to
// refer to the package imported from path — the import's explicit name if
// aliased, otherwise the path's final segment (Go's own default alias
// rule). Empty when the file does not import path at all.
func importAlias(node *ast.File, path string) string {
	for _, imp := range node.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		parts := strings.Split(p, "/")
		return parts[len(parts)-1]
	}
	return ""
}

// TestWorkflowMarkerHasExactlyOneSetter is D-44's machine-checked invariant,
// in deps_test.go's own style: it parses every non-test Go file in the
// module outside internal/testdata with go/parser, collects every call site
// of internal/wfmarker's Set, and asserts there is exactly one — in
// worker/dbstep.go. A second setter appearing anywhere, including a
// convenience test helper later added to non-test source, fails this
// immediately, rather than relying on code review to notice.
func TestWorkflowMarkerHasExactlyOneSetter(t *testing.T) {
	const wfmarkerPath = "github.com/smintz/entflow/internal/wfmarker"

	var setterFiles []string
	for _, f := range nonTestGoFiles(t) {
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, err, "parsing %s", f)

		alias := importAlias(node, wfmarkerPath)
		if alias == "" {
			continue
		}

		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != alias || sel.Sel.Name != "Set" {
				return true
			}
			setterFiles = append(setterFiles, f)
			return true
		})
	}

	require.Equal(t, []string{"worker/dbstep.go"}, relPaths(t, setterFiles),
		"internal/wfmarker.Set must be called from exactly one non-test, non-testdata file")
}

// TestIsWorkflowFalseForEveryPublicContextPath asserts no public entflow
// API can compose a context that satisfies IsWorkflow: a background
// context reports false, and so does the context returned by every
// exported entflow function that both accepts and returns a
// context.Context — currently just WithRegistry (registry.go). If a future
// public function gains that shape, this test's own doc comment is the
// prompt to extend it, not a silent gap.
func TestIsWorkflowFalseForEveryPublicContextPath(t *testing.T) {
	require.False(t, entflow.IsWorkflow(context.Background()))

	reg := entflow.NewRegistry()
	ctx := entflow.WithRegistry(context.Background(), reg)
	require.False(t, entflow.IsWorkflow(ctx), "WithRegistry must not be composable into a marked context")
}

// TestWorkflowMarkerKeyCompileFail proves D-44 is enforced by the compiler,
// not only by TestWorkflowMarkerHasExactlyOneSetter's test-time AST scan:
// internal/testdata/compilefail/wfmarker_internal.go tries to forge a
// marked context by directly constructing internal/wfmarker's context-key
// type instead of calling Set, and building it in isolation (a lone file
// argument to `go build`, ignoring activity_tx.go's own, unrelated compile
// failure in the same directory) must fail, naming the unexported key.
func TestWorkflowMarkerKeyCompileFail(t *testing.T) {
	cmd := exec.Command("go", "build", "./internal/testdata/compilefail/wfmarker_internal.go")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "expected wfmarker_internal.go to fail to compile, output: %s", out)
	require.Contains(t, string(out), "ctxKey")
	require.Contains(t, string(out), "not exported")
}

// TestRawQueryConfinedToClaim closes threat T-02-02's remaining half
// mechanically: the raw-SQL escape hatch entflow.RawQuerier exposes
// (QueryContext/ExecContext, which bypass ent's hooks, privacy rules and
// validators by design) must be called from exactly one non-test,
// non-testdata file in the module — worker/claim.go, the one place that
// bypass is correct (the claim query itself). This is the mechanical
// version of the scoping discipline plans 02-01 and 02-02 stated only in
// prose.
func TestRawQueryConfinedToClaim(t *testing.T) {
	var callerFiles []string
	for _, f := range nonTestGoFiles(t) {
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, err, "parsing %s", f)

		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name != "QueryContext" && sel.Sel.Name != "ExecContext" {
				return true
			}
			callerFiles = append(callerFiles, f)
			return true
		})
	}

	sort.Strings(callerFiles)
	// De-duplicate: a file with multiple call sites must still count once.
	var uniq []string
	for i, f := range callerFiles {
		if i == 0 || f != callerFiles[i-1] {
			uniq = append(uniq, f)
		}
	}

	require.Equal(t, []string{"worker/claim.go"}, relPaths(t, uniq),
		"QueryContext/ExecContext must be called from exactly one non-test, non-testdata file: worker/claim.go")
}
