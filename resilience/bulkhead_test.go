package resilience

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBulkhead_AllowsUpToMaxConcurrent(t *testing.T) {
	b := NewBulkhead(BulkheadConfig{MaxConcurrent: 2, MaxQueue: 0})

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var inFlight int32
	var maxSeen int32

	run := func() {
		_, _ = b.Execute(context.Background(), func(ctx context.Context) (any, error) {
			n := atomic.AddInt32(&inFlight, 1)
			for {
				old := atomic.LoadInt32(&maxSeen)
				if n <= old || atomic.CompareAndSwapInt32(&maxSeen, old, n) {
					break
				}
			}
			started <- struct{}{}
			<-release
			atomic.AddInt32(&inFlight, -1)
			return nil, nil
		})
	}
	go run()
	go run()
	<-started
	<-started

	// Both slots busy, no queue: a third call is rejected immediately.
	_, err := b.Execute(context.Background(), func(ctx context.Context) (any, error) {
		return "ok", nil
	})
	if !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("third call err = %v, want ErrBulkheadFull", err)
	}

	close(release)
	if got := atomic.LoadInt32(&maxSeen); got != 2 {
		t.Fatalf("max concurrent = %d, want 2", got)
	}
}

func TestBulkhead_QueuesThenRejects(t *testing.T) {
	b := NewBulkhead(BulkheadConfig{MaxConcurrent: 1, MaxQueue: 1})

	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = b.Execute(context.Background(), func(ctx context.Context) (any, error) {
			close(started)
			<-release
			return nil, nil
		})
	}()
	<-started // slot occupied

	// One queued waiter is allowed; it blocks waiting for the slot.
	queued := make(chan error, 1)
	go func() {
		_, err := b.Execute(context.Background(), func(ctx context.Context) (any, error) {
			return "ok", nil
		})
		queued <- err
	}()

	// Give the queued goroutine time to take its queue ticket.
	waitFor(t, func() bool { return len(b.queue) == 1 })

	// Slot busy + queue full: the next call is rejected.
	_, err := b.Execute(context.Background(), func(ctx context.Context) (any, error) {
		return nil, nil
	})
	if !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("over-queue call err = %v, want ErrBulkheadFull", err)
	}

	close(release)
	select {
	case err := <-queued:
		if err != nil {
			t.Fatalf("queued call err = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued call never ran")
	}
}

func TestBulkhead_ContextCancelWhileQueued(t *testing.T) {
	b := NewBulkhead(BulkheadConfig{MaxConcurrent: 1, MaxQueue: 2})

	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		_, _ = b.Execute(context.Background(), func(ctx context.Context) (any, error) {
			close(started)
			<-release
			return nil, nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := b.Execute(ctx, func(ctx context.Context) (any, error) {
			return "ok", nil
		})
		done <- err
	}()
	waitFor(t, func() bool { return len(b.queue) == 1 })

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued call did not unblock on cancel")
	}
}

func TestBulkhead_NoQueueRejectsImmediately(t *testing.T) {
	b := NewBulkhead(BulkheadConfig{MaxConcurrent: 1, MaxQueue: 0})
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		_, _ = b.Execute(context.Background(), func(ctx context.Context) (any, error) {
			close(started)
			<-release
			return nil, nil
		})
	}()
	<-started

	_, err := b.Execute(context.Background(), func(ctx context.Context) (any, error) {
		return nil, nil
	})
	if !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("err = %v, want ErrBulkheadFull", err)
	}
}

func TestBulkhead_ComposesWithWrap(t *testing.T) {
	b := NewBulkhead(BulkheadConfig{MaxConcurrent: 1})
	guard := Wrap(b.Middleware())
	out, err := guard(alwaysOK)(context.Background())
	if err != nil || out != "ok" {
		t.Fatalf("guard = (%v, %v)", out, err)
	}
}

func TestBulkhead_ConcurrentRace(t *testing.T) {
	b := NewBulkhead(BulkheadConfig{MaxConcurrent: 4, MaxQueue: 4})
	var wg sync.WaitGroup
	var rejected int32
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_, err := b.Execute(context.Background(), func(ctx context.Context) (any, error) {
					return "ok", nil
				})
				if errors.Is(err, ErrBulkheadFull) {
					atomic.AddInt32(&rejected, 1)
				}
			}
		}()
	}
	wg.Wait()
	_ = atomic.LoadInt32(&rejected)
}
