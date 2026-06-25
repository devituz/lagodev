package resilience

import (
	"context"
	"errors"
	"time"
)

// ErrTimeout is returned by Timeout when fn does not complete within the
// configured duration. The derived context passed to fn is cancelled at the
// same moment, so a well-behaved fn observes ctx.Done() too.
var ErrTimeout = errors.New("resilience: operation timed out")

// TimeoutConfig configures a Timeout. The zero value bounds nothing; provide a
// positive Duration.
type TimeoutConfig struct {
	// Duration is the per-call bound. A non-positive Duration disables the
	// bound (fn runs without an added deadline).
	Duration time.Duration
	// Clock is reserved for time reads; defaults to SystemClock. The
	// derived deadline itself uses context.WithTimeout so cancellation
	// propagates to fn.
	Clock Clock
}

// Timeout bounds each call to fn. It is goroutine-safe and adds no per-call
// state, so a single Timeout can guard many concurrent calls.
type Timeout struct {
	duration time.Duration
	clock    Clock
}

// NewTimeout builds a Timeout from cfg.
func NewTimeout(cfg TimeoutConfig) *Timeout {
	t := &Timeout{
		duration: cfg.Duration,
		clock:    cfg.Clock,
	}
	if t.clock == nil {
		t.clock = SystemClock
	}
	return t
}

// Execute runs fn under a derived context bounded by the configured Duration.
// If fn finishes first, its result is returned. If the bound elapses first,
// ErrTimeout is returned and the derived context is cancelled. fn keeps running
// in its goroutine until it observes the cancellation; Execute does not block on
// it, but it never leaks the goroutine's channel (buffered, size 1).
func (t *Timeout) Execute(ctx context.Context, fn func(ctx context.Context) (any, error)) (any, error) {
	if t.duration <= 0 {
		return fn(ctx)
	}

	callCtx, cancel := context.WithTimeout(ctx, t.duration)
	defer cancel()

	type result struct {
		val any
		err error
	}
	// Buffered so the goroutine can always send and exit even if Execute has
	// already returned on timeout — no goroutine leak.
	done := make(chan result, 1)
	go func() {
		val, err := fn(callCtx)
		done <- result{val: val, err: err}
	}()

	select {
	case r := <-done:
		return r.val, r.err
	case <-callCtx.Done():
		// Distinguish our deadline from a parent cancellation.
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, ErrTimeout
		}
		return nil, callCtx.Err()
	}
}

// Middleware adapts the timeout for use with Wrap.
func (t *Timeout) Middleware() Middleware {
	return func(next Operation) Operation {
		return func(ctx context.Context) (any, error) {
			return t.Execute(ctx, next)
		}
	}
}
