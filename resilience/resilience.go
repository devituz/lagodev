// Package resilience provides composable fault-tolerance primitives for
// Go services — a circuit breaker, retry with backoff, timeout, bulkhead
// (concurrency limiter), and a token-bucket rate limiter, modelled on
// gobreaker / resilience4j.
//
// Every primitive shares the same shape so they compose cleanly:
//
//	type Operation func(ctx context.Context) (any, error)
//	type Middleware func(Operation) Operation
//
// A primitive exposes Execute(ctx, fn) for runtime use and a Middleware()
// method for static composition. Wrap chains middlewares outermost-first:
//
//	guard := resilience.Wrap(
//	    breaker.Middleware(),   // outermost: sees retry's final result
//	    retry.Middleware(),     // re-runs the timeout-wrapped call
//	    timeout.Middleware(),   // innermost: bounds each attempt
//	)
//	out, err := guard(fn)(ctx)
//
// The generic Do[T] helpers recover the concrete return type without the
// caller writing a type assertion.
//
// All primitives are goroutine-safe and honour context cancellation. Time
// is read through an injectable Clock so timing behaviour can be tested
// deterministically (see NewFakeClock).
package resilience

import "context"

// Operation is the unit of work guarded by the resilience primitives. It
// returns an arbitrary result plus an error; primitives decide success or
// failure from the error (and, for the breaker, a configurable predicate).
type Operation func(ctx context.Context) (any, error)

// Middleware decorates an Operation with one resilience concern. Compose
// several with Wrap.
type Middleware func(Operation) Operation

// Wrap composes middlewares into a single Middleware. The first argument is
// the outermost layer: Wrap(a, b, c)(op) == a(b(c(op))). Calling Wrap with
// no arguments yields an identity middleware.
func Wrap(mws ...Middleware) Middleware {
	return func(op Operation) Operation {
		// Apply in reverse so the first middleware ends up outermost.
		for i := len(mws) - 1; i >= 0; i-- {
			op = mws[i](op)
		}
		return op
	}
}

// Do adapts a typed function into an Operation, runs it through mw, and
// returns the result reasserted to T. A nil mw runs fn directly. If the
// underlying Operation returns a value that is not a T (only possible when
// an outer layer substitutes a fallback), the zero value of T is returned
// alongside any error.
func Do[T any](ctx context.Context, mw Middleware, fn func(ctx context.Context) (T, error)) (T, error) {
	op := func(ctx context.Context) (any, error) {
		return fn(ctx)
	}
	if mw != nil {
		op = mw(op)
	}
	v, err := op(ctx)
	if v == nil {
		var zero T
		return zero, err
	}
	t, ok := v.(T)
	if !ok {
		var zero T
		return zero, err
	}
	return t, err
}
