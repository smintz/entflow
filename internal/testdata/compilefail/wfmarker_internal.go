// This file documents D-44's compiler-enforced boundary the same way
// activity_tx.go documents CORE-04: it is expected to fail to compile, and
// its failure IS the test (TestWorkflowMarkerKeyCompileFail, marker_test.go
// at the module root). Do NOT "fix" this file to compile.
//
// internal/wfmarker's Set and Is are exported *within the module* — any
// package under github.com/smintz/entflow, including this one, can legally
// import internal/wfmarker and call them (that is how worker/dbstep.go
// legitimately sets the marker; marker_test.go's AST scan, not the Go
// compiler, is what keeps that call site to exactly one). What the compiler
// alone enforces, with no test needed, is narrower and just as load-bearing:
// nobody — not even code inside this module — can forge a context that
// satisfies wfmarker.Is by directly constructing its context key, because
// that key type is unexported and therefore unreachable outside
// internal/wfmarker itself. The only way to produce a marked context is to
// call wfmarker.Set.
package compilefail

import (
	"context"

	"github.com/smintz/entflow/internal/wfmarker"
)

func forgeMarkerWithoutSet(ctx context.Context) context.Context {
	// wfmarker.ctxKey is unexported: this line does not compile.
	return context.WithValue(ctx, wfmarker.ctxKey{}, true)
}
