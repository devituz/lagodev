package resilience

import (
	"context"
	"errors"
)

// ErrBulkheadFull is returned when a Bulkhead has no free execution slot and no
// free queue slot, so the call is rejected immediately rather than piling on.
var ErrBulkheadFull = errors.New("resilience: bulkhead is full")

// BulkheadConfig configures a Bulkhead. The zero value is usable; NewBulkhead
// applies defaults.
type BulkheadConfig struct {
	// MaxConcurrent caps the number of calls running at once. Defaults to 1.
	// Values < 1 are raised to 1.
	MaxConcurrent int
	// MaxQueue caps how many extra callers may wait for a slot. 0 means no
	// queue: a call is rejected the instant all slots are busy. Values < 0
	// are treated as 0.
	MaxQueue int
}

// Bulkhead is a semaphore-based concurrency limiter. It isolates a dependency
// so a slow one cannot exhaust the whole process: at most MaxConcurrent calls
// run together, at most MaxQueue more wait, and anything beyond is rejected
// with ErrBulkheadFull. It is goroutine-safe.
type Bulkhead struct {
	slots chan struct{} // capacity == MaxConcurrent; a token == a running slot
	queue chan struct{} // capacity == MaxQueue; a token == a waiting slot
}

// NewBulkhead builds a Bulkhead from cfg.
func NewBulkhead(cfg BulkheadConfig) *Bulkhead {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	maxQueue := cfg.MaxQueue
	if maxQueue < 0 {
		maxQueue = 0
	}
	b := &Bulkhead{
		slots: make(chan struct{}, maxConcurrent),
	}
	if maxQueue > 0 {
		b.queue = make(chan struct{}, maxQueue)
	}
	return b
}

// Execute runs fn if a concurrency slot is free, or waits for one while a queue
// slot is held. It returns ErrBulkheadFull when both the running slots and the
// queue are saturated, and ctx.Err() if the context is cancelled while queued.
func (b *Bulkhead) Execute(ctx context.Context, fn func(ctx context.Context) (any, error)) (any, error) {
	// Fast path: grab a running slot without queueing.
	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
		return fn(ctx)
	default:
	}

	// No free slot. Try to take a queue ticket; if the queue is full, reject.
	if b.queue == nil {
		return nil, ErrBulkheadFull
	}
	select {
	case b.queue <- struct{}{}:
		defer func() { <-b.queue }()
	default:
		return nil, ErrBulkheadFull
	}

	// Queued: wait for a running slot or context cancellation.
	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
		return fn(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Middleware adapts the bulkhead for use with Wrap.
func (b *Bulkhead) Middleware() Middleware {
	return func(next Operation) Operation {
		return func(ctx context.Context) (any, error) {
			return b.Execute(ctx, next)
		}
	}
}
