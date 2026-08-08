package entflow_test

import (
	"encoding/json"
	"testing"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow"
)

// TestTransitionsGraph implements D-14's readability requirement: the
// Transitions annotation declared on the fixture Order schema's status enum
// is readable back off a loaded ent schema graph, using ent's own sanctioned
// decode idiom — look up Annotations[Name()] (present as a loosely-typed
// value because annotations round-trip through the loader as JSON),
// json.Marshal it back to bytes, then json.Unmarshal into a zero-value
// TransitionsAnnotation. This is the exact pattern entc/gen/type.go's own
// sqlComment() uses for its built-in CommentAnnotation
// (entc/gen/type.go:1091-1104, verified during research).
//
// entc.LoadGraph and entc/gen are imported from this test file only —
// TestNoTransportDeps proves they never enter the non-test build graph,
// preserving D-02's "root package has zero codegen dependency" property.
// Per D-14, the gen.Field-reading accessor itself ships in Phase 6 alongside
// its first real caller; this test is the readability proof Phase 6's
// cross-validator builds on, and the decode idiom is captured here so
// Phase 6 does not reinvent it.
func TestTransitionsGraph(t *testing.T) {
	graph, err := entc.LoadGraph("./internal/testdata/ent/schema", &gen.Config{
		Package: "github.com/smintz/entflow/internal/testdata/ent",
	})
	require.NoError(t, err)

	var order *gen.Type
	for _, n := range graph.Nodes {
		if n.Name == "Order" {
			order = n
			break
		}
	}
	require.NotNil(t, order, "expected an Order node in the loaded graph")

	var status *gen.Field
	for _, f := range order.Fields {
		if f.Name == "status" {
			status = f
			break
		}
	}
	require.NotNil(t, status, "expected a status field on Order")

	raw, ok := status.Annotations[entflow.TransitionsAnnotation{}.Name()]
	require.True(t, ok, "expected the status field to carry an annotation named %q", entflow.TransitionsAnnotation{}.Name())

	b, err := json.Marshal(raw)
	require.NoError(t, err)

	var ant entflow.TransitionsAnnotation
	require.NoError(t, json.Unmarshal(b, &ant))

	require.Equal(t, map[string][]string{
		"draft":   {"pending", "cancelled"},
		"pending": {"paid", "cancelled"},
		"paid":    {"shipped", "cancelled"},
		"shipped": {"delivered"},
	}, ant.Transitions)
}
