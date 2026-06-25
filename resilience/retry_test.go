package resilience

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewRetry_Defaults(t *testing.T) {
	r := NewRetry(RetryConfig{})
	if r.maxAttempts != 3 {
		t.Errorf("default maxAttempts = %d, want 3", r.maxAttempts)
	}
	if r.backoff == nil {
		t.Error("default backoff is nil")
	}
	if r.retryIf == nil {
		t.Error("default retryIf is nil")
	}
	if r.clock == nil {
		t.Error("default clock is nil")
	}
	// Default predicate retries any non-nil error.
	if r.retryIf(nil) || !r.retryIf(errBoom) {
		t.Error("default retryIf wrong")
	}
}

func TestRetry_SucceedsFirstTry(t *testing.T) {
	clk := NewFakeClock(epoch)
	r := NewRetry(RetryConfig{MaxAttempts: 3, Clock: clk})
	var calls int32
	out, err := r.Execute(context.Background(), func(ctx context.Context) (any, error) {
		atomic.AddInt32(&calls, 1)
		return "ok", nil
	})
	if err != nil || out != "ok" {
		t.Fatalf("Execute = (%v, %v)", out, err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on success)", calls)
	}
}

func TestRetry_RetriesThenSucceeds(t *testing.T) {
	clk := NewFakeClock(epoch)
	r := NewRetry(RetryConfig{
		MaxAttempts: 3,
		Backoff:     ConstantBackoff(time.Second),
		Clock:       clk,
	})
	var calls int32
	done := make(chan struct {
		v   any
		err error
	}, 1)
	go func() {
		v, err := r.Execute(context.Background(), func(ctx context.Context) (any, error) {
			if atomic.AddInt32(&calls, 1) < 3 {
				return nil, errBoom
			}
			return "ok", nil
		})
		done <- struct {
			v   any
			err error
		}{v, err}
	}()

	// Two backoff sleeps between the three attempts.
	waitFor(t, func() bool { return clk.BlockedSleepers() == 1 })
	clk.Advance(time.Second)
	waitFor(t, func() bool { return clk.BlockedSleepers() == 1 })
	clk.Advance(time.Second)

	select {
	case res := <-done:
		if res.err != nil || res.v != "ok" {
			t.Fatalf("Execute = (%v, %v)", res.v, res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not finish")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestRetry_ExhaustionReturnsLastError(t *testing.T) {
	clk := NewFakeClock(epoch)
	r := NewRetry(RetryConfig{
		MaxAttempts: 3,
		Backoff:     ConstantBackoff(time.Second),
		Clock:       clk,
	})
	var calls int32
	done := make(chan error, 1)
	go func() {
		_, err := r.Execute(context.Background(), func(ctx context.Context) (any, error) {
			atomic.AddInt32(&calls, 1)
			return nil, errBoom
		})
		done <- err
	}()

	for i := 0; i < 2; i++ {
		waitFor(t, func() bool { return clk.BlockedSleepers() == 1 })
		clk.Advance(time.Second)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want boom", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not finish")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (all attempts used)", calls)
	}
}

func TestRetry_RetryIfStopsEarly(t *testing.T) {
	clk := NewFakeClock(epoch)
	fatal := errors.New("do not retry")
	r := NewRetry(RetryConfig{
		MaxAttempts: 5,
		Backoff:     ConstantBackoff(time.Second),
		RetryIf:     func(err error) bool { return !errors.Is(err, fatal) },
		Clock:       clk,
	})
	var calls int32
	_, err := r.Execute(context.Background(), func(ctx context.Context) (any, error) {
		atomic.AddInt32(&calls, 1)
		return nil, fatal
	})
	if !errors.Is(err, fatal) {
		t.Fatalf("err = %v, want fatal", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (RetryIf returned false)", calls)
	}
}

func TestRetry_ContextCancelDuringBackoff(t *testing.T) {
	clk := NewFakeClock(epoch)
	r := NewRetry(RetryConfig{
		MaxAttempts: 5,
		Backoff:     ConstantBackoff(time.Hour),
		Clock:       clk,
	})
	ctx, cancel := context.WithCancel(context.Background())
	var calls int32
	done := make(chan error, 1)
	go func() {
		_, err := r.Execute(ctx, func(ctx context.Context) (any, error) {
			atomic.AddInt32(&calls, 1)
			return nil, errBoom
		})
		done <- err
	}()

	// First attempt fails, goroutine parks on backoff.
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
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (cancelled during first backoff)", calls)
	}
}

func TestRetry_ContextAlreadyCancelled(t *testing.T) {
	clk := NewFakeClock(epoch)
	r := NewRetry(RetryConfig{MaxAttempts: 3, Clock: clk})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int32
	_, err := r.Execute(ctx, func(ctx context.Context) (any, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errBoom
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("calls = %d, want 0 (cancelled before first attempt)", calls)
	}
}

func TestConstantBackoff(t *testing.T) {
	b := ConstantBackoff(250 * time.Millisecond)
	for _, attempt := range []int{1, 2, 5, 100} {
		if got := b(attempt); got != 250*time.Millisecond {
			t.Fatalf("attempt %d = %v, want 250ms", attempt, got)
		}
	}
}

func TestExponentialBackoff(t *testing.T) {
	b := ExponentialBackoff(100*time.Millisecond, 2, time.Second)
	want := []time.Duration{
		100 * time.Millisecond, // attempt 1: base
		200 * time.Millisecond, // attempt 2
		400 * time.Millisecond, // attempt 3
		800 * time.Millisecond, // attempt 4
		time.Second,            // attempt 5: capped (would be 1600ms)
		time.Second,            // attempt 6: capped
	}
	for i, w := range want {
		if got := b(i + 1); got != w {
			t.Fatalf("attempt %d = %v, want %v", i+1, got, w)
		}
	}
}

func TestExponentialBackoff_UncappedDoesNotOverflow(t *testing.T) {
	b := ExponentialBackoff(time.Second, 2, 0)
	// A large attempt must not wrap to a negative duration.
	if got := b(200); got < 0 {
		t.Fatalf("attempt 200 = %v, want non-negative", got)
	}
}

func TestExponentialJitterBackoff_DeterministicWithSeed(t *testing.T) {
	mk := func() Backoff {
		return ExponentialJitterBackoff(100*time.Millisecond, 2, time.Second, NewLCGJitter(42))
	}
	a, b := mk(), mk()
	for attempt := 1; attempt <= 6; attempt++ {
		da, db := a(attempt), b(attempt)
		if da != db {
			t.Fatalf("attempt %d: same seed gave %v and %v", attempt, da, db)
		}
		// Full jitter: never exceeds the exponential ceiling.
		ceil := ExponentialBackoff(100*time.Millisecond, 2, time.Second)(attempt)
		if da < 0 || da > ceil {
			t.Fatalf("attempt %d: jitter %v outside [0, %v]", attempt, da, ceil)
		}
	}
}

func TestLCGJitter_InRange(t *testing.T) {
	j := NewLCGJitter(0) // zero seed is remapped
	for i := 0; i < 1000; i++ {
		v := j.Float64()
		if v < 0 || v >= 1 {
			t.Fatalf("Float64 = %v, want [0,1)", v)
		}
	}
}

func TestRetry_ComposesWithWrap(t *testing.T) {
	clk := NewFakeClock(epoch)
	r := NewRetry(RetryConfig{MaxAttempts: 3, Backoff: ConstantBackoff(0), Clock: clk})
	guard := Wrap(r.Middleware())
	var calls int32
	out, err := guard(func(ctx context.Context) (any, error) {
		if atomic.AddInt32(&calls, 1) < 2 {
			return nil, errBoom
		}
		return "ok", nil
	})(context.Background())
	if err != nil || out != "ok" {
		t.Fatalf("guard = (%v, %v)", out, err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestRetry_ConcurrentRace(t *testing.T) {
	clk := NewFakeClock(epoch)
	r := NewRetry(RetryConfig{MaxAttempts: 2, Backoff: ConstantBackoff(0), Clock: clk})
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_, _ = r.Execute(context.Background(), func(ctx context.Context) (any, error) {
					if (g+i)%2 == 0 {
						return nil, errBoom
					}
					return "ok", nil
				})
			}
		}(g)
	}
	wg.Wait()
}
