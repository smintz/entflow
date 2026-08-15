// Package wfmarker holds the workflow-execution marker (D-44): a context
// value distinguishing a context derived inside the worker's own claim
// transaction from any other context in the process. It exists so a
// privacy Policy() can tell "this write originates inside a claim the
// worker itself owns" apart from an ordinary application-supplied context,
// however privileged that context's own viewer may be (T-02-04).
//
// This package lives under internal/, so Go's own import-visibility rule
// makes it unreachable from any module other than entflow's own
// (github.com/smintz/entflow) — that boundary is the compiler's, not a
// comment's. Both Set and Is are exported *within this package* because two
// separate entflow packages need them: entflow (workflowmarker.go, the
// public IsWorkflow read accessor) and entflow/worker (worker/dbstep.go,
// the one legitimate write site). "Exported from an internal package" only
// ever means "reachable by other packages inside this module" — no package
// outside github.com/smintz/entflow can import this path at all.
package wfmarker

import "context"

// ctxKey is the unexported context-key type the marker is stored under.
// Because it is unexported, no package — inside this module or out — can
// construct a context.Value using this exact key type except through Set:
// even a same-module caller cannot forge
// context.WithValue(ctx, ctxKey{}, true) directly, since ctxKey is not
// nameable outside this package. Set is the only way to produce a context
// that satisfies Is.
type ctxKey struct{}

// Set returns a context derived from ctx carrying the workflow marker. The
// only call site anywhere in entflow's non-test, non-testdata source is
// worker/dbstep.go's claim-transaction context derivation — enforced at
// test time by marker_test.go's AST scan
// (TestWorkflowMarkerHasExactlyOneSetter), and at compile time for any code
// outside this module by this package's own internal/ location.
func Set(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, true)
}

// Is reports whether ctx (or any ancestor ctx was derived from) carries the
// workflow marker set by Set.
func Is(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKey{}).(bool)
	return v
}
