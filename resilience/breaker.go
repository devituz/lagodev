package resilience

import (
	"context"
	"errors"
	"sync"
	"time"
)

// State is the circuit breaker's operating state.
type State int

const (
	// StateClosed lets calls through and records outcomes.
	StateClosed State = iota
	// StateOpen rejects calls immediately with ErrOpenCircuit until the
	// open timeout elapses.
	StateOpen
	// StateHalfOpen permits a limited number of trial calls to probe
	// whether the dependency has recovered.
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// ErrOpenCircuit is returned by the breaker when the circuit is open (or the
// half-open trial quota is exhausted) and the call is short-circuited.
var ErrOpenCircuit = errors.New("resilience: circuit breaker is open")

// Counts holds the breaker's rolling tally for the current generation. A
// generation resets on every state transition.
type Counts struct {
	Requests            uint32
	TotalSuccesses      uint32
	TotalFailures       uint32
	ConsecutiveSuccess  uint32
	ConsecutiveFailures uint32
}

func (c *Counts) onRequest() { c.Requests++ }
func (c *Counts) onSuccess() { c.TotalSuccesses++; c.ConsecutiveSuccess++; c.ConsecutiveFailures = 0 }
func (c *Counts) onFailure() { c.TotalFailures++; c.ConsecutiveFailures++; c.ConsecutiveSuccess = 0 }
func (c *Counts) clear()     { *c = Counts{} }

// FailureRatio returns failures/requests for the current generation, or 0
// when there have been no requests.
func (c Counts) FailureRatio() float64 {
	if c.Requests == 0 {
		return 0
	}
	return float64(c.TotalFailures) / float64(c.Requests)
}

// BreakerConfig configures a Breaker. The zero value is usable; New applies
// sensible defaults for any unset field.
type BreakerConfig struct {
	// Name is included in callbacks for observability.
	Name string
	// MaxRequests is the number of trial calls allowed in half-open before
	// the breaker decides. Defaults to 1.
	MaxRequests uint32
	// OpenTimeout is how long the breaker stays open before moving to
	// half-open. Defaults to 60s.
	OpenTimeout time.Duration
	// MinRequests is the minimum number of requests in a generation before
	// the failure-ratio threshold is considered. Defaults to 0 (always
	// considered).
	MinRequests uint32
	// FailureRatio trips the breaker when the failure ratio meets or
	// exceeds this value (and MinRequests is satisfied). 0 disables the
	// ratio rule.
	FailureRatio float64
	// ConsecutiveFailures trips the breaker when this many calls fail in a
	// row. 0 disables the consecutive rule. If both rules are disabled the
	// default ConsecutiveFailures of 5 is applied.
	ConsecutiveFailures uint32
	// IsSuccessful classifies an error as a success (true) or failure
	// (false). Defaults to "nil error == success". Use it to treat, e.g.,
	// context.Canceled or 4xx as non-failures.
	IsSuccessful func(err error) bool
	// OnStateChange is called on every state transition.
	OnStateChange func(name string, from, to State)
	// Clock is the time source; defaults to SystemClock.
	Clock Clock
}

// Breaker is a goroutine-safe circuit breaker.
type Breaker struct {
	name                string
	maxRequests         uint32
	openTimeout         time.Duration
	minRequests         uint32
	failureRatio        float64
	consecutiveFailures uint32
	isSuccessful        func(err error) bool
	onStateChange       func(name string, from, to State)
	clock               Clock

	mu         sync.Mutex
	state      State
	generation uint64
	counts     Counts
	expiry     time.Time // when an open circuit becomes half-open
}

// NewBreaker builds a Breaker from cfg, filling in defaults.
func NewBreaker(cfg BreakerConfig) *Breaker {
	b := &Breaker{
		name:                cfg.Name,
		maxRequests:         cfg.MaxRequests,
		openTimeout:         cfg.OpenTimeout,
		minRequests:         cfg.MinRequests,
		failureRatio:        cfg.FailureRatio,
		consecutiveFailures: cfg.ConsecutiveFailures,
		isSuccessful:        cfg.IsSuccessful,
		onStateChange:       cfg.OnStateChange,
		clock:               cfg.Clock,
	}
	if b.maxRequests == 0 {
		b.maxRequests = 1
	}
	if b.openTimeout <= 0 {
		b.openTimeout = 60 * time.Second
	}
	if b.failureRatio == 0 && b.consecutiveFailures == 0 {
		b.consecutiveFailures = 5
	}
	if b.isSuccessful == nil {
		b.isSuccessful = func(err error) bool { return err == nil }
	}
	if b.clock == nil {
		b.clock = SystemClock
	}
	b.state = StateClosed
	b.toNewGeneration(b.clock.Now())
	return b
}

// State returns the breaker's current state (recomputing an expired open
// circuit into half-open if its timeout has passed).
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()
	state, _ := b.currentState(now)
	return state
}

// Counts returns a snapshot of the current generation's tally.
func (b *Breaker) Counts() Counts {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counts
}

// Execute runs fn under the breaker. It returns ErrOpenCircuit without
// invoking fn when the circuit is open or the half-open quota is exhausted.
func (b *Breaker) Execute(ctx context.Context, fn func(ctx context.Context) (any, error)) (any, error) {
	generation, err := b.beforeRequest()
	if err != nil {
		return nil, err
	}
	// A panic still counts as a failure so a panicking dependency trips the
	// breaker instead of leaking a wedged generation.
	defer func() {
		if r := recover(); r != nil {
			b.afterRequest(generation, false)
			panic(r)
		}
	}()

	result, err := fn(ctx)
	b.afterRequest(generation, b.isSuccessful(err))
	return result, err
}

// Middleware adapts the breaker for use with Wrap.
func (b *Breaker) Middleware() Middleware {
	return func(next Operation) Operation {
		return func(ctx context.Context) (any, error) {
			return b.Execute(ctx, next)
		}
	}
}

func (b *Breaker) beforeRequest() (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()
	state, generation := b.currentState(now)

	if state == StateOpen {
		return generation, ErrOpenCircuit
	}
	if state == StateHalfOpen && b.counts.Requests >= b.maxRequests {
		return generation, ErrOpenCircuit
	}
	b.counts.onRequest()
	return generation, nil
}

func (b *Breaker) afterRequest(before uint64, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()
	state, generation := b.currentState(now)
	if generation != before {
		// The generation rolled over while fn ran; this outcome is stale.
		return
	}
	if success {
		b.onSuccess(state, now)
	} else {
		b.onFailure(state, now)
	}
}

func (b *Breaker) onSuccess(state State, now time.Time) {
	b.counts.onSuccess()
	if state == StateHalfOpen && b.counts.ConsecutiveSuccess >= b.maxRequests {
		b.setState(StateClosed, now)
	}
}

func (b *Breaker) onFailure(state State, now time.Time) {
	b.counts.onFailure()
	switch state {
	case StateClosed:
		if b.tripped() {
			b.setState(StateOpen, now)
		}
	case StateHalfOpen:
		// Any failure during a trial re-opens the circuit.
		b.setState(StateOpen, now)
	}
}

// tripped reports whether the configured thresholds are met. Caller holds mu.
func (b *Breaker) tripped() bool {
	if b.consecutiveFailures > 0 && b.counts.ConsecutiveFailures >= b.consecutiveFailures {
		return true
	}
	if b.failureRatio > 0 && b.counts.Requests >= b.minRequests && b.counts.FailureRatio() >= b.failureRatio {
		return true
	}
	return false
}

// currentState resolves the live state, promoting an expired open circuit to
// half-open. Caller holds mu.
func (b *Breaker) currentState(now time.Time) (State, uint64) {
	if b.state == StateOpen && !b.expiry.IsZero() && !now.Before(b.expiry) {
		b.setState(StateHalfOpen, now)
	}
	return b.state, b.generation
}

// setState transitions and starts a fresh generation. Caller holds mu.
func (b *Breaker) setState(to State, now time.Time) {
	if b.state == to {
		return
	}
	from := b.state
	b.state = to
	b.toNewGeneration(now)
	if b.onStateChange != nil {
		// Invoke without the lock to avoid re-entrancy deadlocks if the
		// callback queries the breaker.
		name := b.name
		cb := b.onStateChange
		b.mu.Unlock()
		cb(name, from, to)
		b.mu.Lock()
	}
}

// toNewGeneration resets counts and recomputes the open expiry. Caller holds mu.
func (b *Breaker) toNewGeneration(now time.Time) {
	b.generation++
	b.counts.clear()
	if b.state == StateOpen {
		b.expiry = now.Add(b.openTimeout)
	} else {
		b.expiry = time.Time{}
	}
}
