package entflow

import (
	"encoding/json"
	"fmt"
)

// JSON wraps an Activity's typed result so it round-trips through the core
// JSON codec transparently: MarshalJSON/UnmarshalJSON delegate to the
// wrapped value, so the stored/wire shape is the value itself, not a wrapper
// envelope. Phase 3 persists these onto the run row and depends on that
// shape.
type JSON[Out any] struct {
	Value Out
}

// NewJSON wraps v as a JSON[Out].
func NewJSON[Out any](v Out) JSON[Out] {
	return JSON[Out]{Value: v}
}

// MarshalJSON encodes the wrapped value directly, with no envelope.
func (j JSON[Out]) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(j.Value)
	if err != nil {
		return nil, fmt.Errorf("entflow: marshal %T: %w", j.Value, err)
	}
	return b, nil
}

// UnmarshalJSON decodes directly into the wrapped value, with no envelope.
func (j *JSON[Out]) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &j.Value); err != nil {
		return fmt.Errorf("entflow: unmarshal into %T: %w", j.Value, err)
	}
	return nil
}

// Attempt identifies a single execution attempt of an Activity step: the
// owning run, the step name, and the attempt number. Its fields are
// unexported because no Attempt is ever constructed by the executor in
// Phase 1 (D-13 means no Activity closure executes yet) — Phase 3 is the
// first and only Phase-1-onward constructor of a real Attempt, supplying
// values from the persisted run row.
type Attempt struct {
	runID   string
	step    string
	attempt int
}

// IdempotencyKey returns the run ID, step name, and attempt number joined by
// colons, matching ACT-02's runID:stepName:attempt derivation exactly. This
// is the exact line generated code must never be trusted to invent.
func (a Attempt) IdempotencyKey() string {
	return fmt.Sprintf("%s:%s:%d", a.runID, a.step, a.attempt)
}
