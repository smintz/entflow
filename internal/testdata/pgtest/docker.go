// Package pgtest provides real-Postgres test infrastructure for entflow's
// worker suites: a testcontainers-backed ent client (pgtest.go) and the
// Docker-availability gate the D-54 release-gate protocol depends on
// (this file). Every symbol here is confined to internal/testdata (the "no
// testdata segment in go list -deps ./..." invariant TestDependenciesExclude
// TestdataFixture asserts) and to _test.go files (T-02-11).
package pgtest

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// dockerAvailableEnvOverride, when set to any non-empty value, short-circuits
// DockerAvailable's real probe — used only by pgtest's own tests to exercise
// the unavailable branch deterministically without actually tearing down the
// host's Docker daemon.
const dockerAvailableEnvOverride = "ENTFLOW_TEST_FORCE_DOCKER_UNAVAILABLE"

// RequireCrashSimEnv is the environment variable name that, when set to "1",
// turns a would-be Docker-unavailable skip into a failure (D-54). Exported
// as a named constant so a skip message can reference it without risking a
// typo'd duplicate literal.
const RequireCrashSimEnv = "ENTFLOW_REQUIRE_CRASHSIM"

// DockerAvailable reports whether a Docker daemon is reachable on the
// execution host. It probes the daemon directly via a real ping — never via
// testcontainers' own testcontainers.SkipIfProviderIsNotHealthy, which has
// an open upstream bug (testcontainers-go#2859) that panics instead of
// skipping on some CI runners. A panicking test process in exactly the
// environment where Docker is expected to be absent is precisely the silent
// gate bypass D-54 exists to prevent, so this probe fails loudly and
// returns an ordinary error instead.
func DockerAvailable() error {
	if os.Getenv(dockerAvailableEnvOverride) != "" {
		return fmt.Errorf("pgtest: docker unavailable (forced via %s)", dockerAvailableEnvOverride)
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("pgtest: constructing docker client: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		return fmt.Errorf("pgtest: pinging docker daemon: %w", err)
	}
	return nil
}

// RequireCrashSim reports whether ENTFLOW_REQUIRE_CRASHSIM is set to "1" —
// the release-gate escalation D-54 specifies: with this set, a Docker-
// unavailable environment must fail the Postgres-backed suites rather than
// skip them.
func RequireCrashSim() bool {
	return os.Getenv(RequireCrashSimEnv) == "1"
}

// SkipUnlessDockerAvailable combines DockerAvailable and RequireCrashSim into
// the one release-gate policy every Postgres-backed test in this module
// applies (D-54): when Docker is unreachable and ENTFLOW_REQUIRE_CRASHSIM is
// unset, the test skips with a message naming Docker as the missing
// dependency and naming the environment variable that would have turned the
// skip into a failure. When ENTFLOW_REQUIRE_CRASHSIM=1, the same condition
// fails the test instead — a skipped release-gate suite must never be
// mistaken for a passing one.
func SkipUnlessDockerAvailable(t *testing.T) {
	t.Helper()

	err := DockerAvailable()
	if err == nil {
		return
	}

	if RequireCrashSim() {
		t.Fatalf("pgtest: %s=1 but docker is unavailable: %v", RequireCrashSimEnv, err)
	}
	t.Skipf("pgtest: skipping — docker is unavailable (%v); set %s=1 to fail instead of skip", err, RequireCrashSimEnv)
}
