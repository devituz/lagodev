package resilience

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrRateLimited is returned by the non-blocking Allow path when no token is
// available. Execute does not return it — Execute blocks until a token frees up
// or ctx is cancelled.
var ErrRateLimited = errors.New("resilience: rate limited")

// RateLimiterConfig configures a RateLimiter. The zero value is usable;
// NewRateLimiter applies defaults.
type RateLimiterConfig struct {
	// Rate is the steady-state token refill rate in tokens per second.
	// Defaults to 1. Values <= 0 are raised to 1.
	Rate float64
	// Burst is the bucket capacity — the largest momentary burst allowed.
	// Defaults to 1. Values < 1 are raised to 1.
	Burst int
	// Clock is the time source; defaults to SystemClock. Inject a FakeClock
	// in tests to advance time deterministically.
	Clock Clock
}

// RateLimiter is a token-bucket limiter. Tokens accrue continuously at Rate up
// to Burst; each call spends one. Execute blocks until a token is available,
// Allow takes one without blocking. It is goroutine-safe.
type RateLimiter struct {
	rate  float64 // tokens per second
	burst float64
	clock Clock

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// NewRateLimiter builds a RateLimiter from cfg.
func NewRateLimiter(cfg RateLimiterConfig) *RateLimiter {
	rate := cfg.Rate
	if rate <= 0 {
		rate = 1
	}
	burst := cfg.Burst
	if burst < 1 {
		burst = 1
	}
	clock := cfg.Clock
	if clock == nil {
		clock = SystemClock
	}
	return &RateLimiter{
		rate:   rate,
		burst:  float64(burst),
		clock:  clock,
		tokens: float64(burst), // start full
		last:   clock.Now(),
	}
}

// refill credits tokens for the time elapsed since last. Caller holds mu.
func (rl *RateLimiter) refill(now time.Time) {
	elapsed := now.Sub(rl.last)
	if elapsed <= 0 {
		return
	}
	rl.last = now
	rl.tokens += elapsed.Seconds() * rl.rate
	if rl.tokens > rl.burst {
		rl.tokens = rl.burst
	}
}

// Allow reports whether a token was available and, if so, consumes it. It never
// blocks. Use it for best-effort shedding; use Execute to wait for a token.
func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.refill(rl.clock.Now())
	if rl.tokens >= 1 {
		rl.tokens--
		return true
	}
	return false
}

// reserve consumes a token if one is available; otherwise it returns the wait
// until the next token accrues without consuming. Caller does not hold mu.
func (rl *RateLimiter) reserve() (ok bool, wait time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.refill(rl.clock.Now())
	if rl.tokens >= 1 {
		rl.tokens--
		return true, 0
	}
	// Time until tokens reaches 1.
	deficit := 1 - rl.tokens
	wait = time.Duration(deficit / rl.rate * float64(time.Second))
	if wait <= 0 {
		wait = time.Nanosecond
	}
	return false, wait
}

// Execute blocks until a token is available (or ctx is cancelled), spends it,
// then runs fn. It returns ctx.Err() if the context is cancelled while waiting.
func (rl *RateLimiter) Execute(ctx context.Context, fn func(ctx context.Context) (any, error)) (any, error) {
	for {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		ok, wait := rl.reserve()
		if ok {
			return fn(ctx)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-rl.clock.After(wait):
			// Loop and try to reserve again; another waiter may have taken
			// the token first, so we re-check rather than assume.
		}
	}
}

// Middleware adapts the rate limiter for use with Wrap.
func (rl *RateLimiter) Middleware() Middleware {
	return func(next Operation) Operation {
		return func(ctx context.Context) (any, error) {
			return rl.Execute(ctx, next)
		}
	}
}
