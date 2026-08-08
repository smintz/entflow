package entflow

import (
	"fmt"
	"strings"

	"github.com/smintz/entflow/meta"
)

// Describe renders f's structure as data a human — or a diff tool — can
// read: one block per step, in declaration order, covering its name, kind,
// constructor, dependencies, transition claim, emit topic, conditions, and
// retry policy (CORE-12). It doubles as the dry-run surface (entflow.md §6).
//
// Describe renders Meta(), never f's internal steps directly, so the
// metadata path is the ONLY path — any future divergence between what
// Meta() reports and what Describe() prints is impossible by construction.
//
// Where a step declares no transition, no emit topic, no dependencies, no
// conditions, or no retry policy, Describe renders an explicit "(none)"
// marker rather than omitting the line, so a diff shows a removed claim as a
// changed line rather than a shifted block — a claimed transition must read
// as visually distinct from a step with no claim (PITFALLS.md's UX row).
//
// Describe never iterates a map: every value is read off Meta()'s
// declaration-ordered slices, so its output is byte-identical across
// repeated calls on the same flow.
func (f *FlowOf[In]) Describe() string {
	m := f.Meta()

	var b strings.Builder
	fmt.Fprintf(&b, "Flow: %s\n", m.Name)
	fmt.Fprintf(&b, "Owner: %s\n", orNone(m.Owner))
	fmt.Fprintf(&b, "InType: %s\n", orNone(m.InType))
	fmt.Fprintf(&b, "OutType: %s\n", orNone(m.OutType))
	fmt.Fprintf(&b, "Steps:\n")
	for _, s := range m.Steps {
		fmt.Fprintf(&b, "  - %s\n", s.Name)
		fmt.Fprintf(&b, "      kind: %s\n", s.Kind)
		fmt.Fprintf(&b, "      constructor: %s\n", s.Constructor)
		fmt.Fprintf(&b, "      depends_on: %s\n", joinOrNone(s.DependsOn))
		fmt.Fprintf(&b, "      transition: %s\n", orNone(s.Transition))
		fmt.Fprintf(&b, "      emit_topic: %s\n", orNone(s.EmitTopic))
		fmt.Fprintf(&b, "      conditions: %s\n", conditionsOrNone(s.Conditions))
		fmt.Fprintf(&b, "      retry: %s\n", retryOrNone(s.Retry))
	}
	return b.String()
}

// orNone renders s, or the explicit "(none)" marker for an empty string —
// used for every optional single-value fact Describe renders.
func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// joinOrNone renders a comma-joined list, or "(none)" for an empty one.
func joinOrNone(ss []string) string {
	if len(ss) == 0 {
		return "(none)"
	}
	return strings.Join(ss, ", ")
}

// conditionsOrNone renders every condition's kind=value pair, comma-joined,
// or "(none)" for a step with no conditions.
func conditionsOrNone(cs []meta.ConditionMeta) string {
	if len(cs) == 0 {
		return "(none)"
	}
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = fmt.Sprintf("%s=%s", c.Kind, c.Value)
	}
	return strings.Join(parts, ", ")
}

// retryOrNone renders a step's retry policy, or "(none)" if it declared
// none.
func retryOrNone(r *meta.RetryMeta) string {
	if r == nil {
		return "(none)"
	}
	return fmt.Sprintf("max_attempts=%d initial=%s max=%s", r.MaxAttempts, r.Initial, r.Max)
}
