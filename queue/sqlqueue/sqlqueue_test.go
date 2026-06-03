package sqlqueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/queue"
)

func newConn(t *testing.T) *database.Connection {
	t.Helper()
	mgr := database.NewManager()
	conn, err := mgr.Open("default", database.Config{
		Driver: "sqlite",
		DSN:    "file::memory:?cache=shared",
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	return conn
}

func newQueue(t *testing.T) *Queue {
	t.Helper()
	conn := newConn(t)
	q, err := New(conn, WithVisibilityTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := q.Setup(context.Background()); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return q
}

func TestPushPopRoundtrip(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	j := queue.Job{ID: "j-1", Name: "Demo", Payload: []byte(`{"x":1}`)}
	if err := q.Push(ctx, j); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, err := q.Pop(ctx, time.Second)
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if got.ID != "j-1" || got.Name != "Demo" || string(got.Payload) != `{"x":1}` {
		t.Fatalf("got = %+v", got)
	}
}

func TestPop_EmptyReturnsErrEmpty(t *testing.T) {
	q := newQueue(t)
	_, err := q.Pop(context.Background(), 50*time.Millisecond)
	if !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("want ErrEmpty, got %v", err)
	}
}

func TestAck_RemovesRow(t *testing.T) {
	q := newQueue(t)
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

func TestNack_RequeuesIncrementsAttempts(t *testing.T) {
	q := newQueue(t)
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

func TestNack_RetryAfterDelaysVisibility(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	_ = q.Push(ctx, queue.Job{ID: "j-4", Name: "x", Payload: []byte("p")})
	j, _ := q.Pop(ctx, time.Second)
	_ = q.Nack(ctx, j.ID, 100*time.Millisecond)
	if _, err := q.Pop(ctx, 30*time.Millisecond); !errors.Is(err, queue.ErrEmpty) {
		t.Fatal("Pop must miss before retry delay elapses")
	}
	if _, err := q.Pop(ctx, 300*time.Millisecond); err != nil {
		t.Fatalf("Pop after retry delay: %v", err)
	}
}

func TestOrphanRecovery_AfterVisibilityTimeout(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	_ = q.Push(ctx, queue.Job{ID: "j-5", Name: "x", Payload: []byte("p")})
	// Reserve and abandon (no Ack / Nack)
	if _, err := q.Pop(ctx, time.Second); err != nil {
		t.Fatalf("Pop: %v", err)
	}
	// Within visibility window — must NOT pop again.
	if _, err := q.Pop(ctx, 50*time.Millisecond); !errors.Is(err, queue.ErrEmpty) {
		t.Fatal("reserved job must stay hidden during visibility window")
	}
	// Past visibility window — orphan recovery makes it eligible.
	time.Sleep(300 * time.Millisecond)
	if _, err := q.Pop(ctx, time.Second); err != nil {
		t.Fatalf("orphan must reappear after visibility timeout, got %v", err)
	}
}

func TestWorkerEndToEnd_AgainstSQL(t *testing.T) {
	q := newQueue(t)
	w := queue.NewWorker(q).Poll(10 * time.Millisecond)

	type Job struct{ N int }
	got := make(chan int, 1)
	queue.Handle[Job](w, func(_ context.Context, j Job) error {
		got <- j.N
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx)
	defer w.Stop()

	if err := queue.Dispatch(ctx, q, Job{N: 99}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	select {
	case n := <-got:
		if n != 99 {
			t.Fatalf("got %d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}
}
