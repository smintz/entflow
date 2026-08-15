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
)

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
