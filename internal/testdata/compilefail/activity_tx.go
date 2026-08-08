// Package compilefail contains fixtures that are expected to fail
// compilation by design. It proves CORE-04: entflow.Activity's type
// parameter list and closure signature structurally exclude a transaction
// handle, so a closure declaring one is a compile error, not a runtime
// check. It is exercised by TestActivityCompileFail in activity_test.go,
// which shells out to `go build` against this package by path — its
// testdata path segment keeps `go build ./...` green while still letting an
// explicit build reach it. Do NOT "fix" this file to compile; its failure
// IS the test.
package compilefail

import (
	"context"

	"github.com/smintz/entflow"
)

type fakeIn struct{}
type fakeEnt struct{}
type fakeOut struct{}
type fakeTx struct{}

func badActivity() {
	f := entflow.New[fakeIn]("BadActivity")
	entflow.Activity(f, "bad", func(ctx context.Context, tx fakeTx, self fakeEnt, att entflow.Attempt) (entflow.JSON[fakeOut], error) {
		return entflow.JSON[fakeOut]{}, nil
	})
}
