package resilience_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devituz/lagodev/resilience"
)

// ExampleBreaker drives a circuit breaker through its full lifecycle using an
// injected FakeClock so the cooldown is deterministic: consecutive failures
// trip it OPEN, further calls are rejected fast with ErrOpenCircuit, advancing
// the clock past the cooldown promotes it to HALF-OPEN, and a trial success
// CLOSES it again.
func ExampleBreaker() {
	clock := resilience.NewFakeClock(time.Unix(0, 0))

	br := resilience.NewBreaker(resilience.BreakerConfig{
		Name:                "payments",
		ConsecutiveFailures: 2,               // trip after 2 failures in a row
		OpenTimeout:         5 * time.Second, // cooldown before half-open
		Clock:               clock,
		OnStateChange: func(name string, from, to resilience.State) {
			fmt.Printf("%s -> %s\n", from, to)
		},
	})

	ctx := context.Background()
	boom := errors.New("dependency down")
	fail := func(context.Context) (any, error) { return nil, boom }
	ok := func(context.Context) (any, error) { return "ok", nil }

	// Two consecutive failures trip the breaker open.
	_, _ = br.Execute(ctx, fail)
	_, _ = br.Execute(ctx, fail)
	fmt.Println("state:", br.State())

	// While open, calls are short-circuited without invoking fn.
	_, err := br.Execute(ctx, ok)
	fmt.Println("rejected fast:", errors.Is(err, resilience.ErrOpenCircuit))

	// Advance past the cooldown: the next observation promotes to half-open.
	clock.Advance(5 * time.Second)
	fmt.Println("state:", br.State())

	// A successful trial call closes the breaker again.
	if _, err := br.Execute(ctx, ok); err != nil {
		fmt.Println("trial:", err)
	}
	fmt.Println("state:", br.State())

	// Output:
	// closed -> open
	// state: open
	// rejected fast: true
	// open -> half-open
	// state: half-open
	// half-open -> closed
	// state: closed
}
