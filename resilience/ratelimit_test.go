package resilience

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewRateLimiter_Defaults(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{})
	if rl.rate != 1 {
		t.Errorf("default rate = %v, want 1", rl.rate)
	}
	if rl.burst != 1 {
		t.Errorf("default burst = %v, want 1", rl.burst)
	}
	if rl.clock == nil {
		t.Error("default clock is nil")
	}
	if rl.tokens != 1 {
		t.Errorf("initial tokens = %v, want full bucket (1)", rl.tokens)
	}
}

func TestRateLimiter_AllowConsumesBurst(t *testing.T) {
	clk := NewFakeClock(epoch)
	rl := NewRateLimiter(RateLimiterConfig{Rate: 1, Burst: 3, Clock: clk})

	// Burst of 3 tokens available immediately.
	for i := 0; i < 3; i++ {
		if !rl.Allow() {
			t.Fatalf("Allow() #%d = false, want true (burst)", i+1)
		}
	}
	// Bucket empty now.
	if rl.Allow() {
		t.Fatal("Allow() after burst = true, want false")
	}

	// After 1s at rate 1/s, exactly one token refills.
	clk.Advance(time.Second)
	if !rl.Allow() {
		t.Fatal("Allow() after 1s refill = false, want true")
	}
	if rl.Allow() {
		t.Fatal("Allow() after consuming the single refilled token = true, want false")
	}
}

func TestRateLimiter_RefillCapsAtBurst(t *testing.T) {
	clk := NewFakeClock(epoch)
	rl := NewRateLimiter(RateLimiterConfig{Rate: 1, Burst: 2, Clock: clk})
	// Drain the bucket.
	rl.Allow()
	rl.Allow()
	// Idle 10s; refill must cap at burst (2), not accrue to 10.
	clk.Advance(10 * time.Second)
	// Two separate calls — each consumes one refilled token.
	first, second := rl.Allow(), rl.Allow()
	if !first || !second {
		t.Fatal("expected 2 tokens after long idle")
	}
	if rl.Allow() {
		t.Fatal("third token available — refill exceeded burst cap")
	}
}

func TestRateLimiter_ExecuteFastPathWhenTokenAvailable(t *testing.T) {
	clk := NewFakeClock(epoch)
	rl := NewRateLimiter(RateLimiterConfig{Rate: 1, Burst: 1, Clock: clk})
	out, err := rl.Execute(context.Background(), func(ctx context.Context) (any, error) {
		return "ok", nil
	})
	if err != nil || out != "ok" {
		t.Fatalf("Execute = (%v, %v)", out, err)
	}
}

func TestRateLimiter_ExecuteBlocksUntilToken(t *testing.T) {
	clk := NewFakeClock(epoch)
	rl := NewRateLimiter(RateLimiterConfig{Rate: 1, Burst: 1, Clock: clk})

	// Drain the single token.
	if !rl.Allow() {
		t.Fatal("precondition: bucket should start full")
	}

	var ran int32
	done := make(chan error, 1)
	go func() {
		_, err := rl.Execute(context.Background(), func(ctx context.Context) (any, error) {
			atomic.AddInt32(&ran, 1)
			return "ok", nil
		})
		done <- err
	}()

	// The goroutine parks on After waiting for a token.
	waitFor(t, func() bool { return clk.BlockedSleepers() == 1 })
	if atomic.LoadInt32(&ran) != 0 {
		t.Fatal("fn ran before a token was available")
	}

	// Advance 1s: one token accrues, the waiter wakes and runs.
	clk.Advance(time.Second)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute never unblocked after refill")
	}
	if atomic.LoadInt32(&ran) != 1 {
		t.Fatalf("ran = %d, want 1", ran)
	}
}

func TestRateLimiter_ExecuteContextCancelWhileWaiting(t *testing.T) {
	clk := NewFakeClock(epoch)
	rl := NewRateLimiter(RateLimiterConfig{Rate: 1, Burst: 1, Clock: clk})
	rl.Allow() // drain

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := rl.Execute(ctx, func(ctx context.Context) (any, error) {
			return "ok", nil
		})
		done <- err
	}()
	waitFor(t, func() bool { return clk.BlockedSleepers() == 1 })

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not unblock on cancel")
	}
}

func TestRateLimiter_ExecuteContextAlreadyCancelled(t *testing.T) {
	clk := NewFakeClock(epoch)
	rl := NewRateLimiter(RateLimiterConfig{Rate: 1, Burst: 1, Clock: clk})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran int32
	_, err := rl.Execute(ctx, func(ctx context.Context) (any, error) {
		atomic.AddInt32(&ran, 1)
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if ran != 0 {
		t.Fatalf("ran = %d, want 0 (cancelled before token check)", ran)
	}
}

func TestRateLimiter_ComposesWithWrap(t *testing.T) {
	clk := NewFakeClock(epoch)
	rl := NewRateLimiter(RateLimiterConfig{Rate: 1, Burst: 5, Clock: clk})
	guard := Wrap(rl.Middleware())
	out, err := guard(alwaysOK)(context.Background())
	if err != nil || out != "ok" {
		t.Fatalf("guard = (%v, %v)", out, err)
	}
}

func TestRateLimiter_ConcurrentRace(t *testing.T) {
	// Real clock here: many goroutines hammering Allow/Execute with a generous
	// burst so the race detector exercises the shared bucket without deadlock.
	rl := NewRateLimiter(RateLimiterConfig{Rate: 1000000, Burst: 1000})
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if i%2 == 0 {
					_ = rl.Allow()
				} else {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					_, _ = rl.Execute(ctx, func(ctx context.Context) (any, error) {
						return "ok", nil
					})
					cancel()
				}
			}
		}()
	}
	wg.Wait()
}
