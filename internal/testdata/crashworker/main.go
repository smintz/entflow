// Package main is internal/testdata/crashworker's binary: the DUR-09 worked
// example for the dedicated-worker topology, and simultaneously plan
// 02-08's tier-2 kill target. It lives under internal/testdata so its
// Postgres driver never enters entflow's non-test dependency graph — see
// README.md for the full reasoning, environment variables, and dual role.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver name

	"github.com/smintz/entflow"
	"github.com/smintz/entflow/internal/crashpoint"
	"github.com/smintz/entflow/internal/testdata/ent"
	_ "github.com/smintz/entflow/internal/testdata/ent/runtime"
	"github.com/smintz/entflow/internal/testdata/ent/schema"
	"github.com/smintz/entflow/internal/testdata/entflowfixture"
	"github.com/smintz/entflow/worker"
)

// Env vars this binary reads — full documentation in README.md.
const (
	envDSN          = "ENTFLOW_CRASHWORKER_DSN"
	envFlows        = "ENTFLOW_CRASHWORKER_FLOWS"
	envConcurrency  = "ENTFLOW_CRASHWORKER_CONCURRENCY"
	envPollInterval = "ENTFLOW_CRASHWORKER_POLL_INTERVAL"
	envDrainTimeout = "ENTFLOW_CRASHWORKER_DRAIN_TIMEOUT"
	// envCrashPoint and envSentinelPath arm this binary as plan 02-08's
	// tier-2 kill target (D-52/D-53): when both are set, the binary
	// installs a crashpoint.Install hook at the named boundary that writes
	// the sentinel file and then blocks forever, so the process is sitting
	// at a precisely known point in a still-open (uncommitted) transaction
	// when the test process SIGKILLs it. Neither variable does anything on
	// its own — arming requires both.
	envCrashPoint   = "ENTFLOW_CRASHWORKER_CRASH_POINT"
	envSentinelPath = "ENTFLOW_CRASHWORKER_SENTINEL_PATH"
)

// sentinelContents is the fixed, plain byte sequence the sentinel file
// always contains — content carries no information the test needs (the
// FILE'S EXISTENCE is the signal), so there is nothing dynamic here that
// could make the write's encoding, newline, or locale dependent.
var sentinelContents = []byte("entflow-crashpoint-reached\n")

// armCrashPoint installs a crashpoint hook at name that writes sentinelPath
// atomically (write to a temp path in the same directory, fsync, then
// rename — so the test process can never observe a partially written file)
// and then blocks forever, holding whatever transaction called
// crashpoint.At open and uncommitted until this whole process is killed.
// The write happens strictly after the process has reached this exact
// point in worker/dbstep.go's claim path and strictly before it blocks.
func armCrashPoint(name, sentinelPath string) {
	crashpoint.Install(name, func() error {
		if err := writeSentinelAtomically(sentinelPath); err != nil {
			// Nothing this process can do to report failure usefully once
			// armed — the test process's own deadline-bounded wait for the
			// sentinel file will time out and name what was still missing.
			return err
		}
		select {} // block forever: the process now sits here until SIGKILLed.
	})
}

// writeSentinelAtomically writes sentinelContents to path via a temp file
// in the same directory followed by a rename, which POSIX guarantees is
// atomic on the same filesystem — the test process reading path either
// sees the file fully absent or fully present with its complete contents,
// never a partial write.
func writeSentinelAtomically(path string) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("creating sentinel temp file: %w", err)
	}
	if _, err := f.Write(sentinelContents); err != nil {
		f.Close()
		return fmt.Errorf("writing sentinel temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("syncing sentinel temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing sentinel temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming sentinel temp file into place: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("crashworker: %v", err) // never embeds envDSN's value (T-02-19)
	}
}

func run() error {
	dsn := os.Getenv(envDSN)
	if dsn == "" {
		return fmt.Errorf("%s is required and must not be empty", envDSN)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("opening database via %s: %w", envDSN, err)
	}
	defer db.Close()

	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	defer client.Close()

	eng := entflow.NewEngine()
	store := entflowfixture.New(client)
	for _, f := range entflow.FlowsOf(schema.Order{}) { // flow.go's own registration call, verbatim
		if err := eng.Register(f, store); err != nil {
			return fmt.Errorf("registering flow %q: %w", f.Name(), err)
		}
	}

	opts := worker.Options{
		Dialect:      "postgres",
		Concurrency:  envInt(envConcurrency, worker.DefaultConcurrency),
		PollInterval: envDuration(envPollInterval, worker.DefaultPollInterval),
		DrainTimeout: envDuration(envDrainTimeout, worker.DefaultDrainTimeout),
		Context: func(ctx context.Context) context.Context { // D-45's own-viewer seam
			return entflowfixture.WithViewer(ctx, entflowfixture.AdminViewer())
		},
	}
	if names := os.Getenv(envFlows); names != "" {
		opts.Flows = strings.Split(names, ",")
	}

	crashPointName := os.Getenv(envCrashPoint)
	sentinelPath := os.Getenv(envSentinelPath)
	switch {
	case crashPointName != "" && sentinelPath != "":
		armCrashPoint(crashPointName, sentinelPath)
	case crashPointName != "" || sentinelPath != "":
		return fmt.Errorf("%s and %s must be set together (both or neither)", envCrashPoint, envSentinelPath)
	}

	w, err := worker.New(eng, opts)
	if err != nil {
		return fmt.Errorf("constructing worker: %w", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- w.Run(context.Background()) }()

	// A termination signal drives Shutdown, never an abrupt process exit.
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	<-sigCtx.Done()
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), opts.DrainTimeout+5*time.Second)
	defer cancel()
	shutdownErr := w.Shutdown(shutdownCtx)
	if runWaitErr := <-runErr; runWaitErr != nil {
		return runWaitErr
	}
	return shutdownErr
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return def
}
