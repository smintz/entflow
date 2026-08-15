package entflow

import (
	"context"

	"github.com/smintz/entflow/internal/wfmarker"
)

// IsWorkflow reports whether ctx was derived from a context marked by the
// worker's claim-transaction path (D-44). This is the ONLY exported surface
// entflow gives the workflow marker — nothing in the public module can set
// it. The marker itself lives in internal/wfmarker, unreachable from any
// module other than entflow's own by Go's import-visibility rule; see that
// package's doc comment for the full boundary.
//
// A privacy Policy() is the intended consumer: it lets a mutation rule
// distinguish "this write originates inside a claim the worker itself
// opened" from any application-supplied context, however privileged that
// context's own viewer may be (T-02-04).
func IsWorkflow(ctx context.Context) bool {
	return wfmarker.Is(ctx)
}
