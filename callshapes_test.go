package entflow_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/smintz/entflow/meta"
	"github.com/stretchr/testify/require"
)

// callShapesHeader documents, inside the committed golden file itself, that
// this is a cross-phase contract (D-20): Phase 5's AST parser is written
// against exactly this surface, so a change here is a change to that
// contract. It also records, per 01-RESEARCH.md's schemast section, the
// finding that the generics-forced call shape (a flat sequence of top-level
// calls) is the EASIER shape for go/ast to parse — walking a sequence of
// top-level *ast.CallExpr statements is more robust than descending an
// arbitrarily deep *ast.SelectorExpr chain a fluent dot-chain would have
// produced.
const callShapesHeader = `# testdata/callshapes.golden
#
# Phase 5's AST parser is written against this file (D-20): a change here is
# a change to the contract Phase 5 must AST-parse. Record one line per
# constructor invocation, in declaration order, naming the constructor, its
# literal string arguments, a fixed placeholder token where the closure
# argument sits, and the option constructors with their literal arguments.
#
# Documented finding (01-RESEARCH.md, "schemast's read surface"): the
# generics-forced API shape — package-level function calls rather than a
# fluent dot-chain — is, incidentally, the EASIER shape for go/ast to parse:
# walking a sequence of top-level *ast.CallExpr statements is more robust
# than descending an arbitrarily deep *ast.SelectorExpr chain.
`

// closurePlaceholder is the fixed token callShapeOf renders wherever a
// constructor's closure argument sits — the call-shape data never contains
// the closure itself (that would violate CORE-11), only its position.
const closurePlaceholder = "<CLOSURE>"

// renderCallShapes renders one line per constructor invocation the fixture
// flow's declaration makes, in declaration order: the flow constructor
// itself, then each step's constructor.
func renderCallShapes(m meta.FlowMeta) []string {
	lines := make([]string, 0, len(m.Steps)+1)
	lines = append(lines, fmt.Sprintf("New(%q, WithOwner(%q))", m.Name, m.Owner))
	for _, s := range m.Steps {
		lines = append(lines, callShapeOf(s))
	}
	return lines
}

// callShapeOf renders one step constructor's call shape: name, a closure
// placeholder (Emit takes none — activity.go documents it as pure
// declaration), then After/When/Transition/Retry in that fixed order.
func callShapeOf(s meta.StepMeta) string {
	parts := []string{fmt.Sprintf("%q", s.Name)}
	if s.Kind != meta.KindEmit {
		parts = append(parts, closurePlaceholder)
	}
	for _, dep := range s.DependsOn {
		parts = append(parts, fmt.Sprintf("After(%q)", dep))
	}
	for _, c := range s.Conditions {
		parts = append(parts, fmt.Sprintf("When(%s(%q))", conditionConstructorOf(c.Kind), c.Value))
	}
	if s.Transition != "" {
		parts = append(parts, fmt.Sprintf("Transition(%q)", s.Transition))
	}
	if s.Retry != nil {
		parts = append(parts, fmt.Sprintf("Retry(Backoff(%d, %s, %s))", s.Retry.MaxAttempts, s.Retry.Initial, s.Retry.Max))
	}
	return fmt.Sprintf("%s(%s)", s.Constructor, strings.Join(parts, ", "))
}

// conditionConstructorOf maps a condition's recorded Kind back to the
// constructor name that produces it — currently only SelfWas.
func conditionConstructorOf(kind string) string {
	if kind == "self_was" {
		return "SelfWas"
	}
	return kind
}

// renderGolden renders m's call shapes with the committed header, as the
// exact byte sequence written to and compared against testdata/callshapes.golden.
func renderGolden(m meta.FlowMeta) string {
	return callShapesHeader + strings.Join(renderCallShapes(m), "\n") + "\n"
}

// TestCallShapesMatchesGolden implements D-20: the fixture flow's
// builder-chain call shapes, snapshotted from its Meta(), match a committed
// golden file that Phase 5's AST parser is written against.
func TestCallShapesMatchesGolden(t *testing.T) {
	m := cancelOrderFlow(t).Meta()
	got := renderGolden(m)

	const goldenPath = "testdata/callshapes.golden"
	if *update {
		require.NoError(t, os.WriteFile(goldenPath, []byte(got), 0o644))
	}

	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	require.Equal(t, string(want), got)
}

// TestCallShapesDetectsDrift proves the golden comparison actually bites: a
// flow built with one recorded fact changed (the refund Activity's retry
// max-attempts) renders call shapes that do NOT match the committed golden.
func TestCallShapesDetectsDrift(t *testing.T) {
	m := cancelOrderFlow(t).Meta()

	altered := m
	altered.Steps = append([]meta.StepMeta(nil), m.Steps...)
	refund := altered.Steps[1]
	alteredRetry := *refund.Retry
	alteredRetry.MaxAttempts = 99
	refund.Retry = &alteredRetry
	altered.Steps[1] = refund

	got := renderGolden(altered)

	want, err := os.ReadFile("testdata/callshapes.golden")
	require.NoError(t, err)
	require.NotEqual(t, string(want), got, "an altered flow must render call shapes that differ from the committed golden — the snapshot must detect drift")
}
