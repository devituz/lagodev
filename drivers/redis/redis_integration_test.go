package redis

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/devituz/lagodev/broadcasting"
	"github.com/devituz/lagodev/queue"
	redislib "github.com/redis/go-redis/v9"
)

// clock abstracts the only server-specific capability the time-based
// queue tests need: advancing the server clock. A real Redis cannot be
// fast-forwarded, so against REDIS_ADDR the test sleeps instead.
type clock interface {
	advance(d time.Duration)
}

type miniClock struct{ mr *miniredis.Miniredis }

func (c miniClock) advance(d time.Duration) { c.mr.FastForward(d) }

type wallClock struct{}

func (wallClock) advance(d time.Duration) { time.Sleep(d) }

// dialQueueRedis returns a client suitable for the LIST/ZSET-based queue
// tests plus a clock for time control.
//
// Gating rules:
//   - under -short  → skip (these touch a server / spin a fake one).
//   - REDIS_ADDR set → dial that real server (clock = wall clock).
//   - otherwise      → spin an in-process miniredis (clock = fast-forward).
//
// The queue driver only uses LIST/ZSET commands, which miniredis emulates
// reliably, so the default (no REDIS_ADDR) path stays green without a
// live server.
func dialQueueRedis(t *testing.T) (*redislib.Client, clock) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping server-backed test in -short mode")
	}
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		rdb := redislib.NewClient(&redislib.Options{Addr: addr})
		if err := rdb.Ping(context.Background()).Err(); err != nil {
			t.Fatalf("REDIS_ADDR=%s unreachable: %v", addr, err)
		}
		t.Cleanup(func() { _ = rdb.Close() })
		return rdb, wallClock{}
	}
	mr := miniredis.RunT(t)
	rdb := redislib.NewClient(&redislib.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, miniClock{mr}
}

// dialPubSubRedis is for Pub/Sub-based broadcaster tests. miniredis'
// Pub/Sub emulation is unreliable with this go-redis/runtime combination
// (it can deadlock), so these tests REQUIRE a real server via REDIS_ADDR.
func dialPubSubRedis(t *testing.T) *redislib.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping Pub/Sub test in -short mode")
	}
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("set REDIS_ADDR=host:port to run Pub/Sub integration tests")
	}
	rdb := redislib.NewClient(&redislib.Options{Addr: addr})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("REDIS_ADDR=%s unreachable: %v", addr, err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// --- Broadcaster (Pub/Sub, needs a real server) -------------------------

func TestBroadcaster_PublishToSubscriber(t *testing.T) {
	rdb := dialPubSubRedis(t)
	b := NewBroadcaster(rdb, WithPrefix(uniquePrefix()))
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
	rdb := dialPubSubRedis(t)
	base := uniquePrefix()
	a := NewBroadcaster(rdb, WithPrefix(base+"-a"))
	b := NewBroadcaster(rdb, WithPrefix(base+"-b"))
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
	rdb := dialPubSubRedis(t)
	b := NewBroadcaster(rdb, WithPrefix(uniquePrefix()))
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
	rdb := dialPubSubRedis(t)
	b := NewBroadcaster(rdb)
	_ = b.Close()
	if err := b.Publish(context.Background(), broadcasting.Event{Channel: "x"}); !errors.Is(err, broadcasting.ErrClosed) {
		t.Fatalf("Publish after Close: %v", err)
	}
	if _, err := b.Subscribe(context.Background(), "x", nil); !errors.Is(err, broadcasting.ErrClosed) {
		t.Fatalf("Subscribe after Close: %v", err)
	}
}

// --- Queue (LIST/ZSET, miniredis-backed by default) ---------------------

func TestQueue_PushPopRoundtrip(t *testing.T) {
	rdb, _ := dialQueueRedis(t)
	q := NewQueue(rdb, "default", QueueWithPrefix(uniquePrefix()))
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
	rdb, _ := dialQueueRedis(t)
	q := NewQueue(rdb, "default", QueueWithPrefix(uniquePrefix()))
	_, err := q.Pop(context.Background(), 50*time.Millisecond)
	if !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("want ErrEmpty, got %v", err)
	}
}

func TestQueue_AckDeletes(t *testing.T) {
	rdb, _ := dialQueueRedis(t)
	q := NewQueue(rdb, "default", QueueWithPrefix(uniquePrefix()))
	defer q.Purge(context.Background())
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
	rdb, _ := dialQueueRedis(t)
	q := NewQueue(rdb, "default", QueueWithPrefix(uniquePrefix()))
	defer q.Purge(context.Background())
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
	rdb, clk := dialQueueRedis(t)
	q := NewQueue(rdb, "default", QueueWithPrefix(uniquePrefix()))
	defer q.Purge(context.Background())
	ctx := context.Background()
	_ = q.Push(ctx, queue.Job{ID: "j-4", Name: "x", Payload: []byte("p")})
	j, _ := q.Pop(ctx, time.Second)
	_ = q.Nack(ctx, j.ID, 100*time.Millisecond)
	if _, err := q.Pop(ctx, 30*time.Millisecond); !errors.Is(err, queue.ErrEmpty) {
		t.Fatal("must be invisible before delay elapses")
	}
	// Advance the clock to expose the delayed job.
	clk.advance(150 * time.Millisecond)
	if _, err := q.Pop(ctx, 500*time.Millisecond); err != nil {
		t.Fatalf("Pop after delay: %v", err)
	}
}

func TestQueue_DelayedPushBecomesAvailable(t *testing.T) {
	rdb, clk := dialQueueRedis(t)
	q := NewQueue(rdb, "default", QueueWithPrefix(uniquePrefix()))
	defer q.Purge(context.Background())
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
	clk.advance(150 * time.Millisecond)
	if _, err := q.Pop(ctx, 500*time.Millisecond); err != nil {
		t.Fatalf("delayed Pop: %v", err)
	}
}

func TestQueue_OrphanRecovery_AfterVisibilityTimeout(t *testing.T) {
	rdb, clk := dialQueueRedis(t)
	q := NewQueue(rdb, "default", QueueWithPrefix(uniquePrefix()), QueueWithVisibilityTimeout(200*time.Millisecond))
	defer q.Purge(context.Background())
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
	clk.advance(300 * time.Millisecond)
	if _, err := q.Pop(ctx, time.Second); err != nil {
		t.Fatalf("orphan recovery failed: %v", err)
	}
}

func TestQueue_WorkerEndToEnd(t *testing.T) {
	rdb, _ := dialQueueRedis(t)
	q := NewQueue(rdb, "default", QueueWithPrefix(uniquePrefix()))
	defer q.Purge(context.Background())
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

// uniquePrefix isolates keys/channels per test so runs against a shared
// real Redis don't collide.
func uniquePrefix() string {
	return "lagodevtest-" + time.Now().Format("150405.000000000")
}
