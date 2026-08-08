package entflow

import (
	"fmt"
	"time"
)

// RetryPolicy is the fixed, small retry parameter set CORE-09 specifies —
// exactly these three fields and no others. An over-parameterised retry API
// (jitter knobs, per-error-class policies, circuit breakers) is on the
// project's explicit anti-features list.
type RetryPolicy struct {
	MaxAttempts int
	Initial     time.Duration
	Max         time.Duration
}

// Backoff constructs a RetryPolicy, validating that maxAttempts is at least
// 1 and that initial does not exceed max. Both are declaration-time
// programming mistakes, so both panic with an error value rather than
// silently clamping.
func Backoff(maxAttempts int, initial, max time.Duration) RetryPolicy {
	validateRetryPolicy("Backoff", RetryPolicy{MaxAttempts: maxAttempts, Initial: initial, Max: max})
	return RetryPolicy{MaxAttempts: maxAttempts, Initial: initial, Max: max}
}

// validateRetryPolicy re-checks the same two invariants Backoff enforces
// (maxAttempts >= 1, initial <= max), naming caller in the panic message. It
// exists because every field of RetryPolicy is exported (necessarily, since
// meta.RetryMeta and Describe() read them) — a caller can construct a
// RetryPolicy{} literal directly and hand it to Retry, bypassing Backoff's
// constructor entirely. Calling this from both Backoff and Retry closes that
// gap so the invariant holds for every RetryPolicy attached to a step,
// regardless of how it was built (WR-03).
func validateRetryPolicy(caller string, p RetryPolicy) {
	if p.MaxAttempts < 1 {
		panic(fmt.Errorf("entflow: %s: maxAttempts must be at least 1, got %d", caller, p.MaxAttempts))
	}
	if p.Initial > p.Max {
		panic(fmt.Errorf("entflow: %s: initial (%s) must not exceed max (%s)", caller, p.Initial, p.Max))
	}
}

// Retry attaches a retry policy to an Activity step. Retry policies apply to
// Activities only — a DB step's retry is the transaction's, not the
// framework's — so applying Retry to a DB step panics at declaration time.
// The check happens here, against the step's kind, rather than in steps.go,
// because by the time a StepOption runs the step's kind has already been set
// by its constructor (addDBStep / Activity), so the option itself can tell.
//
// p is re-validated here (not just trusted from Backoff) because every field
// of RetryPolicy is exported — a caller can construct one directly, skipping
// Backoff's validation, and hand it to Retry (WR-03).
func Retry(p RetryPolicy) StepOption {
	return func(s *step) {
		if s.kind != KindActivity {
			panic(fmt.Errorf("entflow: step %q: Retry is not valid on a %s step; retry policies apply to Activities only", s.name, s.kind))
		}
		validateRetryPolicy(fmt.Sprintf("step %q: Retry", s.name), p)
		rp := p
		s.retry = &rp
	}
}
