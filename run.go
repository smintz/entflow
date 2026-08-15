package entflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
)

// The four non-failure stored `state` values a run's status column can hold
// (D-27). RunStates derives the remaining, per-step `failed:<step>` values
// from a flow's own declared step graph — see RunStateFailed and RunStates.
const (
	// RunStatePending is a run's initial stored state: created, not yet
	// claimed by any worker.
	RunStatePending = "pending"
	// RunStateRunning is a run currently claimed and executing.
	RunStateRunning = "running"
	// RunStateDone is a run's terminal success state.
	RunStateDone = "done"
	// RunStateCancelled is a run's terminal cancelled state.
	RunStateCancelled = "cancelled"
)

// RunStateFailed returns the stored `state` value for a run that failed at
// step. The literal colon separator is intentional (D-27): ent validates the
// Go identifier attached to an enum value, never the stored string, so
// `failed:refund` is a legal stored value even though `FailedRefund` (not
// `Failed:Refund`) must be its Go identifier. An operator can filter
// terminal failures by step directly in SQL — `WHERE state = 'failed:refund'`
// — with no separate lookup table.
func RunStateFailed(step string) string {
	return "failed:" + step
}

// RunStates returns every stored `state` value a run of f can hold: the four
// base values, in declaration order, followed by one RunStateFailed value
// per step in f's declared step graph, in step-declaration order. This is
// the single source of truth RunMixin's enum, the claim predicate, and tests
// all read — the state vocabulary is derived from the step graph exactly
// once.
func RunStates(f Flow) []string {
	steps := f.Meta().Steps
	states := make([]string, 0, 4+len(steps))
	states = append(states, RunStatePending, RunStateRunning, RunStateDone, RunStateCancelled)
	for _, s := range steps {
		states = append(states, RunStateFailed(s.Name))
	}
	return states
}

// runMixin is RunMixin's unexported implementation. It embeds mixin.Schema
// so it satisfies ent.Mixin with only Fields() and Indexes() overridden —
// its edges, hooks, interceptors, policy, and annotations all stay at
// mixin.Schema's no-op defaults.
type runMixin struct {
	mixin.Schema
	flows []Flow
}

// RunMixin returns an ent.Mixin carrying every framework-owned column a
// flow's run row needs (D-28), plus the D-42 `self_was` entry-snapshot
// column D-28's own list omits. The `state` enum's stored values and Go
// identifiers are derived from flows' declared step graphs, in the order
// passed (D-27), so the concrete schema embedding this mixin and entflow's
// own runtime can never drift apart.
//
// RunMixin accepts more than one flow so several flows can legally share
// one physical run table — plan 02-08's crash-simulation fixture is the
// first case that needs this: CancelOrder and a second, multi-DB-step flow
// both persist through CancelOrderFlowRun, so that table's `state` enum
// must carry a failed:<step> value for every step either flow declares, not
// just the first flow's. Each flow's steps contribute their own
// failed:<step> values in the order the flows are passed; a step name
// shared by two flows collides at ent codegen time (ent's own
// duplicate-value validation) rather than silently losing one flow's
// failure state — callers sharing a table are expected to keep step names
// distinct across the flows they combine here.
//
// RunMixin never declares an edges method: the owner-aggregate lineage edge
// points at an application type a library-supplied mixin can never name.
// The concrete schema (e.g. CancelOrderFlowRun) declares that edge itself.
func RunMixin(flows ...Flow) ent.Mixin {
	return runMixin{flows: flows}
}

// Fields of the run mixin — every framework-owned column plus the state
// enum, whose name/value pairs are derived from the union of every flow in
// m.flows' step graphs, in flow-then-step order.
func (m runMixin) Fields() []ent.Field {
	namevalue := make([]string, 0, 8)
	namevalue = append(namevalue,
		"Pending", RunStatePending,
		"Running", RunStateRunning,
		"Done", RunStateDone,
		"Cancelled", RunStateCancelled,
	)
	for _, f := range m.flows {
		for _, s := range f.Meta().Steps {
			namevalue = append(namevalue, "Failed"+goIdent(f.Name(), s.Name), RunStateFailed(s.Name))
		}
	}

	return []ent.Field{
		field.Enum("state").
			NamedValues(namevalue...).
			Default(RunStatePending),
		field.Bytes("input"),
		field.String("current_step").
			Optional(),
		field.Int("attempt").
			Default(0),
		field.String("last_error").
			Optional(),
		field.JSON("results", map[string]json.RawMessage{}).
			Optional(),
		// retry_after gates claimability by time (D-29): a claim query only
		// considers a run claimable once retry_after is null or in the
		// past. Phase 2 ships no timer/durable-sleep semantics on this
		// column — it exists solely to keep a backing-off run from
		// spinning the claim loop after an Activity retry schedules a
		// later attempt. It is the eventual seat for a durable-sleep wake
		// time, but no such mechanism exists yet.
		field.Time("retry_after").
			Optional().
			Nillable(),
		field.String("trace_context").
			Optional(),
		field.String("self_was").
			Optional(),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
		field.Time("started_at").
			Optional().
			Nillable(),
		field.Time("finished_at").
			Optional().
			Nillable(),
	}
}

// Indexes of the run mixin: a single partial index over (state, retry_after)
// restricted to the two claimable states, so the worker's claim query never
// scans a terminal run.
//
// This partial index exists on PostgreSQL and SQLite only — MySQL has no
// equivalent construct. Phase 2 ships no MySQL-specific index variant: a
// variant would carry zero test coverage (the release gate never runs
// against MySQL), and D-35 already frames MySQL as compatible but
// uncertified. docs/dialects.md (plan 02-02) names this gap explicitly
// rather than leaving it implied.
func (m runMixin) Indexes() []ent.Index {
	where := fmt.Sprintf("state IN ('%s', '%s')", RunStatePending, RunStateRunning)
	return []ent.Index{
		index.Fields("state", "retry_after").
			Annotations(entsql.IndexWhere(where)),
	}
}

// goIdent derives a valid, exported Go identifier from a step name by
// splitting on every rune that cannot appear in a Go identifier, upper-
// casing the first rune of each resulting segment, and concatenating them —
// e.g. "order.cancelled" becomes "OrderCancelled". It panics with a
// descriptive message naming both the flow and the offending step if no
// segment survives (an all-punctuation step name), since ent's own
// validation would otherwise fail deep inside codegen with no reference
// back to the step that caused it.
func goIdent(flowName, stepName string) string {
	var b []rune
	upperNext := true
	for _, r := range stepName {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if upperNext {
				r = unicode.ToUpper(r)
				upperNext = false
			}
			b = append(b, r)
			continue
		}
		upperNext = true
	}
	ident := string(b)
	first, _ := utf8.DecodeRuneInString(ident)
	if ident == "" || unicode.IsDigit(first) {
		panic(fmt.Errorf("entflow: RunMixin(%q): step %q cannot produce a valid Go identifier for its failed:<step> enum value", flowName, stepName))
	}
	return ident
}

// Run is the transport-free run record the worker and a RunStore exchange —
// entflow's own view of a run row, independent of any generated ent entity
// type. ID is any because the owning application's generated ID type (int,
// uuid.UUID, etc.) is never known to entflow core.
type Run struct {
	ID           any
	Flow         string
	State        string
	Input        []byte
	CurrentStep  string
	Attempt      int
	LastError    string
	Results      map[string]json.RawMessage
	RetryAfter   *time.Time
	TraceContext string
	SelfWas      string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

// RunTable names the generated identity a dialect-specific claim query is
// built from, without entflow ever naming a generated ent type directly.
type RunTable struct {
	// Name is the run table's storage name, e.g. "cancel_order_flow_runs".
	Name string
	// IDColumn is the run table's primary key column name.
	IDColumn string
	// StateColumn is the run table's `state` column name.
	StateColumn string
	// RetryAfterColumn is the run table's `retry_after` column name.
	RetryAfterColumn string
}

// RawQuerier is the single method entflow's worker needs to run its claim
// query on whatever transaction the generated ent builders are also using.
// It matches the signature sql/execquery's generated ExecContext/
// QueryContext methods carry on both *ent.Client and *ent.Tx (verified in
// execquery_test.go): database/sql is standard library, so this interface
// adds no dependency of its own.
type RawQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
