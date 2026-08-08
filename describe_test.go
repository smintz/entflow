package entflow_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// update regenerates every golden file in this package when set:
//
//	go test ./... -run TestDescribe -update
//	go test ./... -run TestCallShapes -update
//
// Declared once here and reused by callshapes_test.go — flag.Bool panics on
// a duplicate registration, so this is the package's single "-update" flag.
// Hand-rolled (~15 lines total across both golden tests) rather than a
// golden-file dependency, per 01-RESEARCH.md's golden-file section: adding a
// test-only dependency for something this small does not sit well with
// META-02's dependency posture.
var update = flag.Bool("update", false, "update golden files")

// TestDescribeMatchesGolden proves CORE-12: Describe() output covers every
// declared fact and matches a committed golden file.
func TestDescribeMatchesGolden(t *testing.T) {
	got := cancelOrderFlow(t).Describe()

	const goldenPath = "testdata/describe.golden"
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(goldenPath), 0o755))
		require.NoError(t, os.WriteFile(goldenPath, []byte(got), 0o644))
	}

	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	require.Equal(t, string(want), got)
}

// TestDescribeIsStableAcrossCalls proves Describe() never leaks
// map-iteration order: two successive calls on the same flow produce
// byte-identical output.
func TestDescribeIsStableAcrossCalls(t *testing.T) {
	flow := cancelOrderFlow(t)
	first := flow.Describe()
	second := flow.Describe()
	require.Equal(t, first, second)
}

// TestDescribeCoversEveryDeclaredFact proves every fact CORE-12 requires —
// step, kind, dependencies, transition claims, emit topics, and retry
// policy — appears somewhere in Describe()'s rendered output.
func TestDescribeCoversEveryDeclaredFact(t *testing.T) {
	got := cancelOrderFlow(t).Describe()
	for _, want := range []string{
		"cancel", "kind: db", "constructor: UpdateSelf", "transition: cancelled",
		"refund", "kind: activity", "self_was=paid", "max_attempts=5 initial=1s max=1m0s",
		"order.cancelled", "kind: emit", "emit_topic: order.cancelled", "depends_on: cancel",
	} {
		require.Contains(t, got, want)
	}
}
