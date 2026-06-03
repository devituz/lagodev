package broadcasting

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublishToSubscriber(t *testing.T) {
	b := NewMemory()
	defer b.Close()
	got := make(chan Event, 1)
	_, err := b.Subscribe(context.Background(), "ch.1", func(_ context.Context, e Event) error {
		got <- e
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	want := Event{Channel: "ch.1", Name: "X", Payload: []byte("hi")}
	if err := b.Publish(context.Background(), want); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case e := <-got:
		if e.Name != "X" || string(e.Payload) != "hi" {
			t.Fatalf("event = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("handler never called")
	}
}

func TestMultipleSubscribersAllReceive(t *testing.T) {
	b := NewMemory()
	defer b.Close()
	var hits int32
	for i := 0; i < 3; i++ {
		_, _ = b.Subscribe(context.Background(), "ch", func(_ context.Context, _ Event) error {
			atomic.AddInt32(&hits, 1)
			return nil
		})
	}
	_ = b.Publish(context.Background(), Event{Channel: "ch", Name: "ping"})
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&hits) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&hits) != 3 {
		t.Fatalf("expected 3 hits, got %d", hits)
	}
}

func TestChannelIsolation(t *testing.T) {
	b := NewMemory()
	defer b.Close()
	a, c := make(chan struct{}, 1), make(chan struct{}, 1)
	_, _ = b.Subscribe(context.Background(), "a", func(_ context.Context, _ Event) error { a <- struct{}{}; return nil })
	_, _ = b.Subscribe(context.Background(), "c", func(_ context.Context, _ Event) error { c <- struct{}{}; return nil })

	_ = b.Publish(context.Background(), Event{Channel: "a", Name: "x"})
	select {
	case <-a:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("a never received")
	}
	select {
	case <-c:
		t.Fatal("c must NOT receive a's event")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCancelStopsDelivery(t *testing.T) {
	b := NewMemory()
	defer b.Close()
	var hits int32
	sub, _ := b.Subscribe(context.Background(), "ch", func(_ context.Context, _ Event) error {
		atomic.AddInt32(&hits, 1)
		return nil
	})
	_ = b.Publish(context.Background(), Event{Channel: "ch"})
	// give handler a chance to run
	time.Sleep(50 * time.Millisecond)
	sub.Cancel()
	_ = b.Publish(context.Background(), Event{Channel: "ch"})
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("after Cancel, must stop receiving (hits=%d)", hits)
	}
}

func TestCtxCancelCancelsSubscription(t *testing.T) {
	b := NewMemory()
	defer b.Close()
	var hits int32
	ctx, cancel := context.WithCancel(context.Background())
	_, _ = b.Subscribe(ctx, "ch", func(_ context.Context, _ Event) error {
		atomic.AddInt32(&hits, 1)
		return nil
	})
	cancel()
	time.Sleep(50 * time.Millisecond)
	_ = b.Publish(context.Background(), Event{Channel: "ch"})
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("ctx-cancelled sub still ran; hits=%d", hits)
	}
}

func TestClose_RejectsFurtherUse(t *testing.T) {
	b := NewMemory()
	_ = b.Close()
	err := b.Publish(context.Background(), Event{Channel: "x"})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Publish after Close: %v", err)
	}
	_, err = b.Subscribe(context.Background(), "x", nil)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Subscribe after Close: %v", err)
	}
}

func TestDropped_OnFullBuffer(t *testing.T) {
	b := NewMemory(WithBuffer(1))
	defer b.Close()
	// Slow handler so the buffer fills.
	block := make(chan struct{})
	defer close(block)
	_, _ = b.Subscribe(context.Background(), "ch", func(_ context.Context, _ Event) error {
		<-block
		return nil
	})
	// 1 fills the buffer, 1 is being handled, the rest get dropped.
	for i := 0; i < 20; i++ {
		_ = b.Publish(context.Background(), Event{Channel: "ch", Name: "burst"})
	}
	if b.Dropped() == 0 {
		t.Fatalf("expected drops, Dropped() = 0")
	}
}

func TestHandlerError_LoggedNotPropagated(t *testing.T) {
	var logged int32
	b := NewMemory(WithLogger(func(string, ...any) { atomic.AddInt32(&logged, 1) }))
	defer b.Close()
	_, _ = b.Subscribe(context.Background(), "ch", func(_ context.Context, _ Event) error {
		return errors.New("boom")
	})
	if err := b.Publish(context.Background(), Event{Channel: "ch"}); err != nil {
		t.Fatalf("Publish must not surface handler err, got %v", err)
	}
	// Wait for handler goroutine
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&logged) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&logged) == 0 {
		t.Fatal("logger never called for handler error")
	}
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	b := NewMemory()
	defer b.Close()
	var subs []Subscription
	var hits int32
	for i := 0; i < 5; i++ {
		s, _ := b.Subscribe(context.Background(), "ch", func(_ context.Context, _ Event) error {
			atomic.AddInt32(&hits, 1)
			return nil
		})
		subs = append(subs, s)
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Publish(context.Background(), Event{Channel: "ch", Name: "x"})
		}()
	}
	wg.Wait()
	// Each publish reaches 5 handlers; allow 100*5 = 500 expected,
	// minus any drops on slower runners. Just sanity-check non-zero
	// and below upper bound.
	time.Sleep(200 * time.Millisecond)
	got := atomic.LoadInt32(&hits)
	if got == 0 || got > 500 {
		t.Fatalf("hits = %d (expected 0 < n <= 500)", got)
	}
}
