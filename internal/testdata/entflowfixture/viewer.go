package entflowfixture

import "context"

// Viewer is the application's own viewer type (OPS-02, D-45) — entflow core
// never sees it, standing in for whatever auth/session type a real adopter
// already has. This fixture ties ownership directly to the Order a run
// belongs to: Subject is the Order ID this viewer may act on, a
// simplification of what a real application would resolve through its own
// user/tenant model. Admin bypasses the owner-scoping check entirely.
type Viewer struct {
	// Subject is the Order ID this viewer is scoped to.
	Subject int
	// Admin, when true, is allowed to read and cancel every run regardless
	// of Subject.
	Admin bool
}

// viewerKey is the unexported context-key type a Viewer is stored under.
type viewerKey struct{}

// WithViewer returns a context derived from ctx carrying v, for
// CancelOrderFlowRun's Policy() to consult via ViewerFromContext.
func WithViewer(ctx context.Context, v Viewer) context.Context {
	return context.WithValue(ctx, viewerKey{}, v)
}

// ViewerFromContext retrieves the Viewer attached by WithViewer. ok is
// false when no viewer is present — CancelOrderFlowRun's Policy() denies a
// query outright in that case (OPS-02).
func ViewerFromContext(ctx context.Context) (Viewer, bool) {
	v, ok := ctx.Value(viewerKey{}).(Viewer)
	return v, ok
}

// AdminViewer returns a Viewer with the admin flag set — for the worker's
// own Options.Context hook (D-45: the worker obtains its own viewer this
// way, since entflow core cannot name the application's viewer type) and
// for tests that need unrestricted read/write access to every run
// regardless of owner.
func AdminViewer() Viewer {
	return Viewer{Admin: true}
}
