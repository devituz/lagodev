# Resilience

`github.com/devituz/lagodev/resilience` provides composable fault-tolerance
primitives for services that call dependencies which can fail, hang, or
overload. The package is modelled on gobreaker / resilience4j.

Every primitive shares one shape so they compose cleanly:

```go
type Operation  func(ctx context.Context) (any, error)
type Middleware func(Operation) Operation
```

A primitive exposes `Execute(ctx, fn)` for runtime use and a `Middleware()`
method for static composition. All primitives are goroutine-safe, honour
`context` cancellation, and read time through an injectable [`Clock`](#deterministic-time)
so timing is testable.

> **Status.** All five primitives are shipped and exported: the **circuit
> breaker**, **retry** (with pluggable backoff), **timeout**, **bulkhead**
> (concurrency limiter), and a token-bucket **rate limiter** — plus the
> composition helpers (`Wrap`, `Do[T]`) and the `Clock` test harness. They share
> one `Execute(ctx, fn)` / `Middleware()` contract and drop straight into
> [`Wrap`](#composing-patterns).

## Quick start

```go
package main

import (
    "context"
    "errors"
    "fmt"
    "time"

    "github.com/devituz/lagodev/resilience"
)

func main() {
    br := resilience.NewBreaker(resilience.BreakerConfig{
        Name:                "payments",
        ConsecutiveFailures: 5,                // trip after 5 failures in a row
        OpenTimeout:         30 * time.Second, // cooldown before a retry probe
    })

    ctx := context.Background()
    out, err := br.Execute(ctx, func(ctx context.Context) (any, error) {
        return chargeCard(ctx) // your dependency call
    })

    switch {
    case errors.Is(err, resilience.ErrOpenCircuit):
        fmt.Println("breaker open — failing fast, not calling the dependency")
    case err != nil:
        fmt.Println("call failed:", err)
    default:
        fmt.Println("charged:", out)
    }
}
```

`Execute` runs `fn` and records its outcome. When the breaker is **open** it
returns `ErrOpenCircuit` *without* invoking `fn`, so a struggling dependency
gets breathing room instead of a thundering herd.

## Circuit breaker

A circuit breaker stops calling a failing dependency once failures cross a
threshold, then periodically lets a trial call through to see if it has
recovered.

### State machine

```
            failures cross threshold
   ┌──────────────────────────────────────┐
   │                                       ▼
CLOSED                                    OPEN
   ▲                                       │
   │ trial call succeeds                   │ OpenTimeout elapses
   │                                       ▼
   └──────────────── HALF-OPEN ◀───────────┘
                         │
                         └── trial call fails ──▶ OPEN
```

| State            | Behaviour                                                                 |
|------------------|---------------------------------------------------------------------------|
| `StateClosed`    | Calls pass through; outcomes are tallied. Trips to open when thresholds hit. |
| `StateOpen`      | Calls are rejected immediately with `ErrOpenCircuit`. After `OpenTimeout` the next observation promotes to half-open. |
| `StateHalfOpen`  | Up to `MaxRequests` trial calls are allowed. Enough consecutive successes close it; any failure re-opens it. |

`State` implements `Stringer` (`"closed"`, `"open"`, `"half-open"`). Inspect the
live state — an expired open circuit is recomputed to half-open on read:

```go
if br.State() == resilience.StateOpen {
    // serve a cached/fallback response
}
```

### Generations and counters

Each state transition starts a fresh **generation** and clears the tally, so
thresholds always measure the *current* window — there is no stale carry-over
across transitions. An outcome whose generation rolled over while `fn` was
running is discarded. Read the snapshot with `Counts()`:

```go
c := br.Counts()
fmt.Printf("reqs=%d ok=%d fail=%d consecFail=%d ratio=%.2f\n",
    c.Requests, c.TotalSuccesses, c.TotalFailures,
    c.ConsecutiveFailures, c.FailureRatio())
```

```go
type Counts struct {
    Requests            uint32
    TotalSuccesses      uint32
    TotalFailures       uint32
    ConsecutiveSuccess  uint32
    ConsecutiveFailures uint32
}
```

`FailureRatio()` returns `TotalFailures / Requests` for the generation (0 when
there have been no requests).

### Configuration

```go
type BreakerConfig struct {
    Name                string
    MaxRequests         uint32
    OpenTimeout         time.Duration
    MinRequests         uint32
    FailureRatio        float64
    ConsecutiveFailures uint32
    IsSuccessful        func(err error) bool
    OnStateChange       func(name string, from, to State)
    Clock               Clock
}
```

The zero value is usable; `NewBreaker` fills defaults for any unset field:

| Field                 | Default | Meaning                                                              |
|-----------------------|---------|----------------------------------------------------------------------|
| `Name`                | `""`    | Included in `OnStateChange` callbacks for observability.             |
| `MaxRequests`         | `1`     | Trial calls allowed in half-open before the breaker decides.         |
| `OpenTimeout`         | `60s`   | How long the circuit stays open before moving to half-open.          |
| `MinRequests`         | `0`     | Minimum requests in a generation before the ratio rule is considered. |
| `FailureRatio`        | `0`     | Trip when failure ratio ≥ this value. `0` disables the ratio rule.   |
| `ConsecutiveFailures` | `0`*    | Trip when this many calls fail in a row. `0` disables the rule.      |
| `IsSuccessful`        | nil err | Classify an error as success/failure.                                |
| `OnStateChange`       | none    | Called on every transition.                                          |
| `Clock`               | `SystemClock` | Time source (inject `FakeClock` in tests).                     |

\* If **both** `FailureRatio` and `ConsecutiveFailures` are left at `0`, a
default `ConsecutiveFailures` of `5` is applied — the breaker is never trip-less.

### Tripping thresholds

The breaker trips to open (from closed) when **either** rule is satisfied:

- **Consecutive:** `ConsecutiveFailures` calls fail in a row.
- **Ratio:** `Requests >= MinRequests` *and* `FailureRatio >= FailureRatio`.

```go
// Ratio-based: trip at 50% failures, but only after 20 requests in the window.
br := resilience.NewBreaker(resilience.BreakerConfig{
    Name:         "search",
    MinRequests:  20,
    FailureRatio: 0.5,
    OpenTimeout:  15 * time.Second,
})
```

### Classifying outcomes

By default `nil error == success`. Use `IsSuccessful` to keep "expected" errors
from tripping the breaker — a cancelled context or a 4xx client error usually
should not count against an otherwise-healthy dependency:

```go
br := resilience.NewBreaker(resilience.BreakerConfig{
    Name: "users-api",
    IsSuccessful: func(err error) bool {
        if err == nil {
            return true
        }
        // Caller-side cancellation isn't the dependency's fault.
        if errors.Is(err, context.Canceled) {
            return true
        }
        var httpErr *HTTPError
        if errors.As(err, &httpErr) && httpErr.Status < 500 {
            return true // 4xx is a client problem, not a dependency failure
        }
        return false
    },
})
```

> A panic in `fn` is recorded as a **failure** and then re-panicked, so a
> panicking dependency still trips the breaker instead of leaking a wedged
> generation.

### Observing transitions

`OnStateChange` fires on every transition and is the integration point for
metrics, logs, and alerts. It is invoked without the breaker lock held, so it
is safe to query the breaker from inside the callback:

```go
br := resilience.NewBreaker(resilience.BreakerConfig{
    Name:                "billing",
    ConsecutiveFailures: 5,
    OnStateChange: func(name string, from, to resilience.State) {
        log.Printf("breaker %s: %s -> %s", name, from, to)
        metrics.BreakerState.WithLabelValues(name).Set(float64(to))
    },
})
```

## Retry

`Retry` re-runs an `Operation` up to `MaxAttempts` times, waiting per a pluggable
`Backoff` between attempts and waking immediately on context cancellation. The
wait is slept through the injected `Clock`, so it is deterministic in tests.

```go
type RetryConfig struct {
    MaxAttempts int                  // total calls (initial try + retries); default 3
    Backoff     Backoff              // wait between attempts; default constant 100ms
    RetryIf     func(err error) bool // retry predicate; default: any non-nil error
    Clock       Clock                // time source; default SystemClock
}

func NewRetry(cfg RetryConfig) *Retry
func (r *Retry) Execute(ctx context.Context, fn func(ctx context.Context) (any, error)) (any, error)
func (r *Retry) Middleware() Middleware
```

`Execute` returns the result and error of the last attempt. It stops early when
`fn` succeeds (nil error) or when `RetryIf` returns `false`, and returns
`ctx.Err()` if the context is cancelled before or between attempts.

### Backoff strategies

```go
type Backoff func(attempt int) time.Duration // attempt is 1-based

func ConstantBackoff(delay time.Duration) Backoff
func ExponentialBackoff(base time.Duration, factor float64, max time.Duration) Backoff
func ExponentialJitterBackoff(base time.Duration, factor float64, max time.Duration, src JitterSource) Backoff
```

`ExponentialBackoff` yields `base * factor^(attempt-1)`, capped at `max` (a
non-positive `max` is uncapped; overflow is guarded). `ExponentialJitterBackoff`
applies *full jitter* — a uniform random value in `[0, exp(attempt)]` — using an
injectable `JitterSource` so tests are deterministic and the `math/rand` global
is never touched:

```go
type JitterSource interface{ Float64() float64 } // value in [0,1)

func NewLCGJitter(seed uint64) JitterSource // seedable 64-bit LCG
```

```go
retry := resilience.NewRetry(resilience.RetryConfig{
    MaxAttempts: 4,
    Backoff: resilience.ExponentialJitterBackoff(
        100*time.Millisecond, 2, 5*time.Second,
        resilience.NewLCGJitter(uint64(time.Now().UnixNano())),
    ),
    RetryIf: func(err error) bool {
        // Don't retry a context cancellation or a 4xx; do retry transient errors.
        return err != nil && !errors.Is(err, context.Canceled)
    },
})

out, err := retry.Execute(ctx, func(ctx context.Context) (any, error) {
    return callDependency(ctx)
})
```

## Timeout

`Timeout` bounds each call. It runs `fn` under a context derived with
`context.WithTimeout`; if `fn` finishes first its result is returned, otherwise
`Execute` returns the sentinel `ErrTimeout` and the derived context is cancelled
so a well-behaved `fn` observes `ctx.Done()`. A parent cancellation surfaces as
the parent's error (e.g. `context.Canceled`), *not* `ErrTimeout`.

```go
var ErrTimeout = errors.New("resilience: operation timed out")

type TimeoutConfig struct {
    Duration time.Duration // per-call bound; non-positive disables the bound
    Clock    Clock         // default SystemClock
}

func NewTimeout(cfg TimeoutConfig) *Timeout
func (t *Timeout) Execute(ctx context.Context, fn func(ctx context.Context) (any, error)) (any, error)
func (t *Timeout) Middleware() Middleware
```

The worker goroutine sends on a buffered channel, so it never leaks even when
`fn` ignores cancellation and runs past the deadline.

```go
timeout := resilience.NewTimeout(resilience.TimeoutConfig{Duration: 2 * time.Second})

out, err := timeout.Execute(ctx, func(ctx context.Context) (any, error) {
    return slowCall(ctx) // should respect ctx.Done()
})
if errors.Is(err, resilience.ErrTimeout) {
    // the call exceeded 2s
}
```

## Bulkhead

`Bulkhead` is a semaphore-based concurrency limiter that isolates a dependency
so a slow one cannot exhaust the whole process. At most `MaxConcurrent` calls run
together; up to `MaxQueue` more may wait for a slot; anything beyond is rejected
immediately with `ErrBulkheadFull`. A queued caller unblocks when a slot frees or
when its context is cancelled.

```go
var ErrBulkheadFull = errors.New("resilience: bulkhead is full")

type BulkheadConfig struct {
    MaxConcurrent int // calls running at once; default 1
    MaxQueue      int // extra callers allowed to wait; 0 = reject when busy
}

func NewBulkhead(cfg BulkheadConfig) *Bulkhead
func (b *Bulkhead) Execute(ctx context.Context, fn func(ctx context.Context) (any, error)) (any, error)
func (b *Bulkhead) Middleware() Middleware
```

```go
bh := resilience.NewBulkhead(resilience.BulkheadConfig{
    MaxConcurrent: 8,  // at most 8 concurrent calls to this dependency
    MaxQueue:      16, // up to 16 more may wait
})

out, err := bh.Execute(ctx, func(ctx context.Context) (any, error) {
    return callDependency(ctx)
})
if errors.Is(err, resilience.ErrBulkheadFull) {
    // shed load — serve a fallback instead of piling on
}
```

## Rate limiter

`RateLimiter` is a token bucket: tokens accrue continuously at `Rate` (tokens per
second) up to `Burst`, and each call spends one. `Execute` **blocks** until a
token is available or the context is cancelled; `Allow` takes one **without
blocking**, returning `false` (the non-blocking path that the `ErrRateLimited`
sentinel describes) when the bucket is empty.

```go
var ErrRateLimited = errors.New("resilience: rate limited")

type RateLimiterConfig struct {
    Rate  float64 // steady-state refill, tokens/sec; default 1
    Burst int     // bucket capacity / max momentary burst; default 1
    Clock Clock   // default SystemClock
}

func NewRateLimiter(cfg RateLimiterConfig) *RateLimiter
func (rl *RateLimiter) Allow() bool
func (rl *RateLimiter) Execute(ctx context.Context, fn func(ctx context.Context) (any, error)) (any, error)
func (rl *RateLimiter) Middleware() Middleware
```

```go
rl := resilience.NewRateLimiter(resilience.RateLimiterConfig{
    Rate:  100, // 100 requests/sec sustained
    Burst: 20,  // allow short bursts of 20
})

// Blocking: wait for a token (or ctx cancellation), then call.
out, err := rl.Execute(ctx, func(ctx context.Context) (any, error) {
    return callDependency(ctx)
})

// Non-blocking: shed the request if no token is free.
if !rl.Allow() {
    return resilience.ErrRateLimited
}
```

## Composing patterns

`Wrap` composes middlewares into one. The **first argument is the outermost
layer** — `Wrap(a, b, c)(op) == a(b(c(op)))`. With no arguments it is the
identity middleware.

```go
guard := resilience.Wrap(
    breaker.Middleware(),     // outermost: sees the final result of the chain
    rateLimiter.Middleware(), // shed/queue before spending an attempt
    retry.Middleware(),       // re-runs the timeout-wrapped call
    bulkhead.Middleware(),    // cap concurrency per attempt
    timeout.Middleware(),     // innermost: bounds each individual attempt
)

op := guard(func(ctx context.Context) (any, error) {
    return callDependency(ctx)
})
out, err := op(ctx)
```

Ordering matters: put the breaker **outermost** so it observes the final
outcome of the whole chain (e.g. retries exhausted), and put per-attempt bounds
(timeout, bulkhead) **innermost** so they apply to each individual call. With
this order, `timeout` bounds every attempt, `retry` re-runs the bounded call,
and the breaker only trips on the chain's final verdict.

### Typed results without a type assertion

`Operation` returns `any`. `Do[T]` adapts a typed function, runs it through a
middleware, and re-asserts the result to `T` so the caller skips the cast. A
`nil` middleware runs `fn` directly.

```go
type Invoice struct{ ID string; Total int }

mw := br.Middleware()
inv, err := resilience.Do(ctx, mw, func(ctx context.Context) (*Invoice, error) {
    return fetchInvoice(ctx, id)
})
// inv is *Invoice, err carries ErrOpenCircuit when short-circuited.
```

If an outer layer substitutes a value that is not a `T`, `Do` returns the zero
value of `T` alongside the error.

## Deterministic time

The breaker reads time through a `Clock`, so cooldown and half-open behaviour
can be tested without sleeping. `SystemClock` is the production default;
`FakeClock` advances only when you call `Advance`.

```go
type Clock interface {
    Now() time.Time
    After(d time.Duration) <-chan time.Time
    Sleep(d time.Duration)
}
```

```go
func TestBreakerCooldown(t *testing.T) {
    clock := resilience.NewFakeClock(time.Unix(0, 0))
    br := resilience.NewBreaker(resilience.BreakerConfig{
        Name:                "dep",
        ConsecutiveFailures: 2,
        OpenTimeout:         5 * time.Second,
        Clock:               clock,
    })

    ctx := context.Background()
    boom := func(context.Context) (any, error) { return nil, errors.New("down") }
    ok := func(context.Context) (any, error) { return "ok", nil }

    // Two failures trip it open.
    _, _ = br.Execute(ctx, boom)
    _, _ = br.Execute(ctx, boom)
    if br.State() != resilience.StateOpen {
        t.Fatalf("want open, got %s", br.State())
    }

    // While open, calls fail fast without touching the dependency.
    if _, err := br.Execute(ctx, ok); !errors.Is(err, resilience.ErrOpenCircuit) {
        t.Fatalf("want ErrOpenCircuit, got %v", err)
    }

    // Advance past the cooldown → half-open; a trial success closes it.
    clock.Advance(5 * time.Second)
    if br.State() != resilience.StateHalfOpen {
        t.Fatalf("want half-open, got %s", br.State())
    }
    if _, err := br.Execute(ctx, ok); err != nil {
        t.Fatalf("trial call: %v", err)
    }
    if br.State() != resilience.StateClosed {
        t.Fatalf("want closed, got %s", br.State())
    }
}
```

`FakeClock.BlockedSleepers()` reports how many goroutines are parked in
`Sleep`/`After`, which is useful when coordinating timers in a test.

## Production notes

- **One breaker per dependency, not per request.** Construct the breaker once
  (at startup) and share the `*Breaker` — its counters and state are the whole
  point. A breaker created per call never trips.
- **Always pair the breaker with a deadline.** The breaker shields you from a
  *failing* dependency; a `context` timeout shields you from a *hanging* one.
  Use both.
- **Tune for your traffic.** Low-volume endpoints want the consecutive rule
  (a ratio is noisy on small samples). High-volume endpoints want
  `FailureRatio` with a `MinRequests` floor so a few stray errors don't trip it.
- **Treat client errors as success.** Wire `IsSuccessful` so 4xx and
  `context.Canceled` don't count against the dependency — otherwise client
  misbehaviour can open a circuit on a healthy backend.
- **Export `OnStateChange`.** Emit a metric and a log line on every transition;
  an open circuit is an incident signal, not a silent fallback.
- **Have a fallback path.** When `Execute` returns `ErrOpenCircuit`, serve a
  cached value, a degraded response, or a queued retry — don't surface the raw
  error to the user.
- **Keep `OpenTimeout` realistic.** Too short and you hammer a recovering
  dependency every cooldown; too long and you stay degraded after it recovers.
  Seconds-to-tens-of-seconds is typical.

## See also

- [GETTING_STARTED.md](GETTING_STARTED.md) — project scaffolding and the `web` server.
- [ORM.md](ORM.md) — querying and transactions for the dependencies you guard.
