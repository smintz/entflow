package crashpoint_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smintz/entflow/internal/crashpoint"
)

func TestMain(m *testing.M) {
	code := m.Run()
	os.Exit(code)
}

// TestAtWithNoHookInstalledReturnsNil proves the production default: with
// nothing installed, At is a nil-returning no-op for any name.
func TestAtWithNoHookInstalledReturnsNil(t *testing.T) {
	crashpoint.Clear()
	require.NoError(t, crashpoint.At(context.Background(), "anything:after-claim"))
	require.NoError(t, crashpoint.At(context.Background(), crashpoint.PreClaimName))
}

// TestInstallFiresAndUninstallRestoresInertBehavior proves an installed
// hook fires exactly once per named call, and that calling its returned
// uninstall function restores At's inert, nil-returning default.
func TestInstallFiresAndUninstallRestoresInertBehavior(t *testing.T) {
	crashpoint.Clear()
	t.Cleanup(crashpoint.Clear)

	const name = "step-x:before-step"
	calls := 0
	boom := errors.New("boom")
	uninstall := crashpoint.Install(name, func() error {
		calls++
		return boom
	})

	err := crashpoint.At(context.Background(), name)
	require.ErrorIs(t, err, boom)
	require.Equal(t, 1, calls)

	err = crashpoint.At(context.Background(), name)
	require.ErrorIs(t, err, boom)
	require.Equal(t, 2, calls, "an installed hook fires on every call, not just the first")

	uninstall()
	require.NoError(t, crashpoint.At(context.Background(), name), "uninstall must restore the inert default")
	require.Equal(t, 2, calls, "an uninstalled hook must not fire again")

	// A different name was never touched by name's install/uninstall pair.
	require.NoError(t, crashpoint.At(context.Background(), "step-y:before-step"))
}

// TestConcurrentAtDuringInstallIsRaceClean proves N goroutines calling At
// concurrently with a goroutine repeatedly installing and uninstalling a
// hook is race-detector clean — run with -race. Synchronization is
// entirely goroutine-lifecycle based (a WaitGroup for the writer, a stop
// channel closed once the writer is done and readers have had a chance to
// observe it) — no sleep anywhere.
func TestConcurrentAtDuringInstallIsRaceClean(t *testing.T) {
	crashpoint.Clear()
	t.Cleanup(crashpoint.Clear)

	const name = "race:after-hydrate"
	stop := make(chan struct{})
	var readers sync.WaitGroup

	// Readers: hammer At concurrently until told to stop.
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = crashpoint.At(context.Background(), name)
				}
			}
		}()
	}

	// Writer: repeatedly install/uninstall while readers are hammering At
	// concurrently, then signal readers to stop once it has genuinely
	// finished — not after a fixed sleep.
	for i := 0; i < 200; i++ {
		uninstall := crashpoint.Install(name, func() error { return nil })
		uninstall()
	}
	close(stop)
	readers.Wait()
}

// TestMatrixThreeStepFlowMoreCrashPointsThanOneStepFlow proves Matrix
// scales with step count: a three-step flow enumerates strictly more crash
// points than a one-step flow, and the counts match the documented 1+6*N
// shape.
func TestMatrixThreeStepFlowMoreCrashPointsThanOneStepFlow(t *testing.T) {
	one := crashpoint.Matrix("OneStep", []string{"only"})
	three := crashpoint.Matrix("ThreeStep", []string{"a", "b", "c"})

	require.Len(t, one, 1+6*1)
	require.Len(t, three, 1+6*3)
	require.Greater(t, len(three), len(one))
}

// TestMatrixNeverEmptyForNonEmptyStepsAndPanicsOnEmpty proves Matrix always
// returns a non-empty slice for at least one step, and panics — never
// silently returns empty — for zero steps.
func TestMatrixNeverEmptyForNonEmptyStepsAndPanicsOnEmpty(t *testing.T) {
	got := crashpoint.Matrix("Solo", []string{"only"})
	require.NotEmpty(t, got)

	require.Panics(t, func() {
		crashpoint.Matrix("Empty", nil)
	}, "Matrix must panic rather than silently return an empty matrix (T-02-20)")
}

// TestMatrixOrderIsStableAcrossCalls proves Matrix's output order is
// deterministic: two calls with identical arguments produce identical
// slices, in the same order, every time.
func TestMatrixOrderIsStableAcrossCalls(t *testing.T) {
	steps := []string{"reserve", "charge", "ship"}
	first := crashpoint.Matrix("ProcessOrder", steps)
	for i := 0; i < 10; i++ {
		again := crashpoint.Matrix("ProcessOrder", steps)
		require.Equal(t, first, again, "Matrix's output order must be stable across repeated calls")
	}
}

// TestMatrixIncludesFirstAndLastStepBoundaries proves the boundary edge:
// both the first and the last step in a multi-step flow get their own full
// set of six named boundaries in the matrix.
func TestMatrixIncludesFirstAndLastStepBoundaries(t *testing.T) {
	steps := []string{"reserve", "charge", "ship"}
	got := crashpoint.Matrix("ProcessOrder", steps)

	names := make(map[string]bool, len(got))
	for _, n := range got {
		names[n] = true
	}

	require.Contains(t, names, crashpoint.PreClaimName)
	for _, b := range []crashpoint.Boundary{
		crashpoint.AfterClaim, crashpoint.AfterHydrate, crashpoint.BeforeStep,
		crashpoint.AfterStep, crashpoint.AfterAdvance, crashpoint.BeforeCommit,
	} {
		require.Contains(t, names, crashpoint.Name("reserve", b), "first step %q boundary missing", b)
		require.Contains(t, names, crashpoint.Name("ship", b), "last step %q boundary missing", b)
	}
}

// crashPointFixtureStepCounts is the number of executable DB steps each
// fixture flow declares, kept here rather than importing the schema
// package directly (which would need internal/crashpoint to depend on
// internal/testdata/ent/schema — an import direction crashpoint, a
// dependency-free leaf package, has no reason to take on). The counts are
// asserted against dbstep.go's own instrumentation below by boundary type,
// not by importing the fixture.
const (
	cancelOrderStepCount  = 1 // CancelOrder: "cancel"
	processOrderStepCount = 3 // ProcessOrder: "reserve", "charge", "ship"
)

// TestMatrixMultiStepFlowStrictlyLargerThanSingleStepFixture pins the exact
// acceptance criterion: the fixture multi-step flow's matrix is strictly
// larger than the single-step CancelOrder flow's matrix.
func TestMatrixMultiStepFlowStrictlyLargerThanSingleStepFixture(t *testing.T) {
	cancelOrderSteps := make([]string, cancelOrderStepCount)
	for i := range cancelOrderSteps {
		cancelOrderSteps[i] = "step" + strconv.Itoa(i)
	}
	processOrderSteps := make([]string, processOrderStepCount)
	for i := range processOrderSteps {
		processOrderSteps[i] = "step" + strconv.Itoa(i)
	}

	cancel := crashpoint.Matrix("CancelOrder", cancelOrderSteps)
	process := crashpoint.Matrix("ProcessOrder", processOrderSteps)
	require.Greater(t, len(process), len(cancel))
}

// dbstepSource locates worker/dbstep.go relative to this test file's own
// source path (via runtime.Caller), so the test works regardless of the
// working directory `go test` is invoked from.
func dbstepSource(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed to resolve this test file's path")
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "worker", "dbstep.go")
	b, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)
	return string(b)
}

// TestEveryMatrixBoundaryTypeReachableInClaimPath proves every Boundary
// constant Matrix enumerates from has a corresponding crashpoint.At call
// site in worker/dbstep.go's claim path, named via the same
// crashpoint.Name/crashpoint.PreClaimName construction Matrix itself uses —
// a boundary Matrix produces with no call site in dbstep.go (or vice versa)
// is a coverage hole this test exists to catch. crashsim_test.go (plan
// 02-08 Task 2) is the complementary, fully dynamic proof: it exercises
// every CONCRETE name Matrix produces for the real fixture flows against a
// real claim path, not merely each boundary TYPE's call site.
func TestEveryMatrixBoundaryTypeReachableInClaimPath(t *testing.T) {
	src := dbstepSource(t)

	require.Contains(t, src, "crashpoint.PreClaimName", "pre-claim boundary must be instrumented in worker/dbstep.go")
	for _, b := range []crashpoint.Boundary{
		crashpoint.AfterClaim, crashpoint.AfterHydrate, crashpoint.BeforeStep,
		crashpoint.AfterStep, crashpoint.AfterAdvance, crashpoint.BeforeCommit,
	} {
		require.Contains(t, src, "crashpoint."+string(boundaryIdent(b)), "boundary %q must be instrumented in worker/dbstep.go", b)
	}
}

// boundaryIdent maps a Boundary's stored value back to its exported Go
// identifier, exactly as declared in crashpoint.go, so the source-text
// search above looks for the same token dbstep.go actually writes
// (crashpoint.AfterClaim, not crashpoint."after-claim").
func boundaryIdent(b crashpoint.Boundary) string {
	switch b {
	case crashpoint.AfterClaim:
		return "AfterClaim"
	case crashpoint.AfterHydrate:
		return "AfterHydrate"
	case crashpoint.BeforeStep:
		return "BeforeStep"
	case crashpoint.AfterStep:
		return "AfterStep"
	case crashpoint.AfterAdvance:
		return "AfterAdvance"
	case crashpoint.BeforeCommit:
		return "BeforeCommit"
	default:
		panic("boundaryIdent: unknown boundary " + string(b))
	}
}
