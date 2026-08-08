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
	if maxAttempts < 1 {
		panic(fmt.Errorf("entflow: Backoff: maxAttempts must be at least 1, got %d", maxAttempts))
	}
	if initial > max {
		panic(fmt.Errorf("entflow: Backoff: initial (%s) must not exceed max (%s)", initial, max))
	}
	return RetryPolicy{MaxAttempts: maxAttempts, Initial: initial, Max: max}
}

// Retry attaches a retry policy to an Activity step. Retry policies apply to
// Activities only — a DB step's retry is the transaction's, not the
// framework's — so applying Retry to a DB step panics at declaration time.
// The check happens here, against the step's kind, rather than in steps.go,
// because by the time a StepOption runs the step's kind has already been set
// by its constructor (addDBStep / Activity), so the option itself can tell.
func Retry(p RetryPolicy) StepOption {
	return func(s *step) {
		if s.kind != KindActivity {
			panic(fmt.Errorf("entflow: step %q: Retry is not valid on a %s step; retry policies apply to Activities only", s.name, s.kind))
		}
		rp := p
		s.retry = &rp
	}
}
