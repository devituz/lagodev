package redis

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/devituz/lagodev/broadcasting"
	"github.com/devituz/lagodev/queue"
	redislib "github.com/redis/go-redis/v9"
)

func newRedis(t *testing.T) (*redislib.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redislib.NewClient(&redislib.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, mr
}

// --- Broadcaster --------------------------------------------------------

func TestBroadcaster_PublishToSubscriber(t *testing.T) {
	rdb, _ := newRedis(t)
	b := NewBroadcaster(rdb)
	defer b.Close()

	got := make(chan broadcasting.Event, 1)
	_, err := b.Subscribe(context.Background(), "chat.42", func(_ context.Context, e broadcasting.Event) error {
		got <- e
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	want := broadcasting.Event{Channel: "chat.42", Name: "MsgPosted", Payload: []byte(`{"x":1}`)}
	if err := b.Publish(context.Background(), want); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case e := <-got:
		if e.Name != "MsgPosted" || string(e.Payload) != `{"x":1}` {
			t.Fatalf("decoded = %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber never received")
	}
}

func TestBroadcaster_PrefixIsolation(t *testing.T) {
	rdb, _ := newRedis(t)
	a := NewBroadcaster(rdb, WithPrefix("app-a"))
	b := NewBroadcaster(rdb, WithPrefix("app-b"))
	defer a.Close()
	defer b.Close()

	hitsA := make(chan struct{}, 1)
	hitsB := make(chan struct{}, 1)
	_, _ = a.Subscribe(context.Background(), "ch", func(_ context.Context, _ broadcasting.Event) error {
		hitsA <- struct{}{}
		return nil
	})
	_, _ = b.Subscribe(context.Background(), "ch", func(_ context.Context, _ broadcasting.Event) error {
		hitsB <- struct{}{}
		return nil
	})
	_ = a.Publish(context.Background(), broadcasting.Event{Channel: "ch", Name: "x"})
	select {
	case <-hitsA:
	case <-time.After(time.Second):
		t.Fatal("app-a subscriber missed event")
	}
	select {
	case <-hitsB:
		t.Fatal("app-b must not receive app-a's event due to prefix isolation")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestBroadcaster_Cancel(t *testing.T) {
	rdb, _ := newRedis(t)
	b := NewBroadcaster(rdb)
	defer b.Close()
	var hits int32
	sub, _ := b.Subscribe(context.Background(), "ch", func(_ context.Context, _ broadcasting.Event) error {
		atomic.AddInt32(&hits, 1)
		return nil
	})
	_ = b.Publish(context.Background(), broadcasting.Event{Channel: "ch", Name: "x"})
	time.Sleep(100 * time.Millisecond)
	sub.Cancel()
	_ = b.Publish(context.Background(), broadcasting.Event{Channel: "ch", Name: "x"})
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("after Cancel hits=%d (want 1)", hits)
	}
}

func TestBroadcaster_Close_RejectsFurtherUse(t *testing.T) {
	rdb, _ := newRedis(t)
	b := NewBroadcaster(rdb)
	_ = b.Close()
	if err := b.Publish(context.Background(), broadcasting.Event{Channel: "x"}); !errors.Is(err, broadcasting.ErrClosed) {
		t.Fatalf("Publish after Close: %v", err)
	}
	if _, err := b.Subscribe(context.Background(), "x", nil); !errors.Is(err, broadcasting.ErrClosed) {
		t.Fatalf("Subscribe after Close: %v", err)
	}
}

// --- Queue --------------------------------------------------------------

func TestQueue_PushPopRoundtrip(t *testing.T) {
	rdb, _ := newRedis(t)
	q := NewQueue(rdb, "default")
	defer q.Purge(context.Background())
	ctx := context.Background()

	if err := q.Push(ctx, queue.Job{ID: "j-1", Name: "Demo", Payload: []byte(`{"x":1}`)}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	j, err := q.Pop(ctx, time.Second)
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if j.ID != "j-1" || j.Name != "Demo" || string(j.Payload) != `{"x":1}` {
		t.Fatalf("got = %+v", j)
	}
}

func TestQueue_EmptyReturnsErrEmpty(t *testing.T) {
	rdb, _ := newRedis(t)
	q := NewQueue(rdb, "default")
	_, err := q.Pop(context.Background(), 50*time.Millisecond)
	if !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("want ErrEmpty, got %v", err)
	}
}

func TestQueue_AckDeletes(t *testing.T) {
	rdb, _ := newRedis(t)
	q := NewQueue(rdb, "default")
	ctx := context.Background()
	_ = q.Push(ctx, queue.Job{ID: "j-2", Name: "x", Payload: []byte("p")})
	j, _ := q.Pop(ctx, time.Second)
	if err := q.Ack(ctx, j.ID); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if q.Len() != 0 {
		t.Fatalf("Len after Ack = %d", q.Len())
	}
}

func TestQueue_NackRequeuesIncrementsAttempts(t *testing.T) {
	rdb, _ := newRedis(t)
	q := NewQueue(rdb, "default")
	ctx := context.Background()
	_ = q.Push(ctx, queue.Job{ID: "j-3", Name: "x", Payload: []byte("p")})
	j, _ := q.Pop(ctx, time.Second)
	if err := q.Nack(ctx, j.ID, 0); err != nil {
		t.Fatalf("Nack: %v", err)
	}
	j2, err := q.Pop(ctx, time.Second)
	if err != nil {
		t.Fatalf("re-Pop: %v", err)
	}
	if j2.Attempts != 1 {
		t.Fatalf("attempts = %d", j2.Attempts)
	}
}

func TestQueue_NackWithRetryAfter(t *testing.T) {
	rdb, mr := newRedis(t)
	q := NewQueue(rdb, "default")
	ctx := context.Background()
	_ = q.Push(ctx, queue.Job{ID: "j-4", Name: "x", Payload: []byte("p")})
	j, _ := q.Pop(ctx, time.Second)
	_ = q.Nack(ctx, j.ID, 100*time.Millisecond)
	if _, err := q.Pop(ctx, 30*time.Millisecond); !errors.Is(err, queue.ErrEmpty) {
		t.Fatal("must be invisible before delay elapses")
	}
	// Advance miniredis clock to expose the delayed job.
	mr.FastForward(150 * time.Millisecond)
	if _, err := q.Pop(ctx, 500*time.Millisecond); err != nil {
		t.Fatalf("Pop after delay: %v", err)
	}
}

func TestQueue_DelayedPushBecomesAvailable(t *testing.T) {
	rdb, mr := newRedis(t)
	q := NewQueue(rdb, "default")
	ctx := context.Background()
	_ = q.Push(ctx, queue.Job{
		ID:          "j-5",
		Name:        "x",
		Payload:     []byte("p"),
		AvailableAt: time.Now().Add(100 * time.Millisecond),
	})
	if _, err := q.Pop(ctx, 30*time.Millisecond); !errors.Is(err, queue.ErrEmpty) {
		t.Fatal("delayed job must be invisible initially")
	}
	mr.FastForward(150 * time.Millisecond)
	if _, err := q.Pop(ctx, 500*time.Millisecond); err != nil {
		t.Fatalf("delayed Pop: %v", err)
	}
}

func TestQueue_OrphanRecovery_AfterVisibilityTimeout(t *testing.T) {
	rdb, mr := newRedis(t)
	q := NewQueue(rdb, "default", QueueWithVisibilityTimeout(200*time.Millisecond))
	ctx := context.Background()
	_ = q.Push(ctx, queue.Job{ID: "j-6", Name: "x", Payload: []byte("p")})
	if _, err := q.Pop(ctx, time.Second); err != nil {
		t.Fatalf("Pop: %v", err)
	}
	// Worker dies without Ack/Nack — orphan should reappear after the
	// visibility timeout.
	if _, err := q.Pop(ctx, 50*time.Millisecond); !errors.Is(err, queue.ErrEmpty) {
		t.Fatal("must stay hidden during visibility window")
	}
	mr.FastForward(300 * time.Millisecond)
	if _, err := q.Pop(ctx, time.Second); err != nil {
		t.Fatalf("orphan recovery failed: %v", err)
	}
}

func TestQueue_WorkerEndToEnd(t *testing.T) {
	rdb, _ := newRedis(t)
	q := NewQueue(rdb, "default")
	w := queue.NewWorker(q).Poll(20 * time.Millisecond)

	type Job struct{ N int }
	got := make(chan int, 1)
	queue.Handle[Job](w, func(_ context.Context, j Job) error {
		got <- j.N
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx)
	defer w.Stop()

	if err := queue.Dispatch(ctx, q, Job{N: 99}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	select {
	case n := <-got:
		if n != 99 {
			t.Fatalf("handler got %d", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler never ran")
	}
}
