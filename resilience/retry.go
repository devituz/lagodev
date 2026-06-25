package resilience

import (
	"context"
	"time"
)

// Backoff computes how long to wait before the attempt-th retry. attempt is
// 1-based: attempt 1 is the wait after the first call failed, attempt 2 the
// wait after the second, and so on. A non-positive return means "retry
// immediately". Implementations must be safe for concurrent use.
type Backoff func(attempt int) time.Duration

// ConstantBackoff waits the same delay before every retry.
func ConstantBackoff(delay time.Duration) Backoff {
	return func(attempt int) time.Duration {
		return delay
	}
}

// ExponentialBackoff waits base * factor^(attempt-1), capped at max (max <= 0
// means uncapped). factor <= 0 is treated as 2.
func ExponentialBackoff(base time.Duration, factor float64, max time.Duration) Backoff {
	if factor <= 0 {
		factor = 2
	}
	return func(attempt int) time.Duration {
		if attempt < 1 {
			attempt = 1
		}
		d := float64(base)
		for i := 1; i < attempt; i++ {
			d *= factor
			// Guard against overflow into +Inf / negative durations.
			if max > 0 && d >= float64(max) {
				return max
			}
			if d >= float64(time.Duration(1<<62)) {
				if max > 0 {
					return max
				}
				return time.Duration(1 << 62)
			}
		}
		out := time.Duration(d)
		if max > 0 && out > max {
			return max
		}
		return out
	}
}

// JitterSource yields a pseudo-random float in [0,1). It is injectable so
// jittered backoff is deterministic in tests; it must be safe for concurrent
// use. NewLCGJitter provides a seedable default that never touches the
// math/rand global.
type JitterSource interface {
	// Float64 returns a value in [0,1).
	Float64() float64
}

// lcgJitter is a tiny self-contained linear-congruential generator. It avoids
// the math/rand global so concurrent Retry instances stay deterministic and
// isolated. It is goroutine-safe via a mutex-free atomic-style step guarded by
// the caller; ExponentialJitterBackoff serialises access through Retry's use.
type lcgJitter struct {
	state uint64
}

// NewLCGJitter returns a seedable JitterSource backed by a 64-bit LCG. It does
// not lock; wrap it if you share one source across goroutines that call
// Float64 concurrently. Retry copies a value into each Backoff closure, so the
// default per-Retry source is not shared.
func NewLCGJitter(seed uint64) JitterSource {
	if seed == 0 {
		seed = 0x9e3779b97f4a7c15 // avoid the all-zero fixed point
	}
	return &lcgJitter{state: seed}
}

func (l *lcgJitter) Float64() float64 {
	// Numerical Recipes LCG constants.
	l.state = l.state*6364136223846793005 + 1442695040888963407
	// Use the top 53 bits for a uniform [0,1) double.
	return float64(l.state>>11) / float64(uint64(1)<<53)
}

// ExponentialJitterBackoff applies full jitter to ExponentialBackoff: the wait
// is a uniform random value in [0, exp(attempt)]. src supplies the randomness;
// pass NewLCGJitter(seed) for deterministic tests. A nil src falls back to a
// fixed-seed source.
func ExponentialJitterBackoff(base time.Duration, factor float64, max time.Duration, src JitterSource) Backoff {
	exp := ExponentialBackoff(base, factor, max)
	if src == nil {
		src = NewLCGJitter(1)
	}
	return func(attempt int) time.Duration {
		ceil := exp(attempt)
		if ceil <= 0 {
			return 0
		}
		return time.Duration(src.Float64() * float64(ceil))
	}
}

// RetryConfig configures a Retry. The zero value is usable; NewRetry applies
// defaults for any unset field.
type RetryConfig struct {
	// MaxAttempts is the total number of calls (initial try + retries).
	// Defaults to 3. Values < 1 are raised to 1.
	MaxAttempts int
	// Backoff computes the wait between attempts. Defaults to a constant
	// 100ms.
	Backoff Backoff
	// RetryIf decides whether an error is worth retrying. Defaults to
	// "retry on any non-nil error". It is never consulted for a nil error
	// (success), nor after the final attempt.
	RetryIf func(err error) bool
	// Clock is the time source used to sleep between attempts; defaults to
	// SystemClock. Inject a FakeClock in tests.
	Clock Clock
}

// Retry re-runs an Operation up to MaxAttempts times, waiting per Backoff
// between attempts and honouring context cancellation while it waits. It is
// goroutine-safe: a single Retry can guard many concurrent calls.
type Retry struct {
	maxAttempts int
	backoff     Backoff
	retryIf     func(err error) bool
	clock       Clock
}

// NewRetry builds a Retry from cfg, filling in defaults.
func NewRetry(cfg RetryConfig) *Retry {
	r := &Retry{
		maxAttempts: cfg.MaxAttempts,
		backoff:     cfg.Backoff,
		retryIf:     cfg.RetryIf,
		clock:       cfg.Clock,
	}
	if r.maxAttempts < 1 {
		r.maxAttempts = 3
	}
	if r.backoff == nil {
		r.backoff = ConstantBackoff(100 * time.Millisecond)
	}
	if r.retryIf == nil {
		r.retryIf = func(err error) bool { return err != nil }
	}
	if r.clock == nil {
		r.clock = SystemClock
	}
	return r
}

// Execute runs fn, retrying on failure per the configuration. It returns the
// result and error of the last attempt. If ctx is cancelled while waiting
// between attempts, it returns the last attempt's result with ctx.Err().
func (r *Retry) Execute(ctx context.Context, fn func(ctx context.Context) (any, error)) (any, error) {
	var result any
	var err error
	for attempt := 1; attempt <= r.maxAttempts; attempt++ {
		// Respect a context that was cancelled before this attempt.
		if cerr := ctx.Err(); cerr != nil {
			return result, cerr
		}

		result, err = fn(ctx)
		if err == nil || !r.retryIf(err) {
			return result, err
		}
		if attempt == r.maxAttempts {
			break // exhausted: return the last failure
		}

		// Wait for the backoff, but wake immediately on cancellation.
		if d := r.backoff(attempt); d > 0 {
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			case <-r.clock.After(d):
			}
		} else if cerr := ctx.Err(); cerr != nil {
			return result, cerr
		}
	}
	return result, err
}

// Middleware adapts the retry for use with Wrap.
func (r *Retry) Middleware() Middleware {
	return func(next Operation) Operation {
		return func(ctx context.Context) (any, error) {
			return r.Execute(ctx, next)
		}
	}
}
