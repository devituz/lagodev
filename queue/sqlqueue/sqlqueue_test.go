package sqlqueue

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
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

// TestOrphanRecovery_IncrementsAttempts verifies that recovering an
// orphaned (reserved-but-abandoned) job bumps attempts so MaxRetry can
// eventually bound a poison job that keeps outliving the visibility
// window. Regression: recovery used to clear reserved_at without
// touching attempts, allowing an unbounded re-delivery loop.
func TestOrphanRecovery_IncrementsAttempts(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	_ = q.Push(ctx, queue.Job{ID: "orphan-1", Name: "x", Payload: []byte("p")})

	first, err := q.Pop(ctx, time.Second)
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if first.Attempts != 0 {
		t.Fatalf("first delivery attempts = %d, want 0", first.Attempts)
	}
	// Abandon (no Ack/Nack); wait past the visibility timeout.
	time.Sleep(300 * time.Millisecond)

	second, err := q.Pop(ctx, time.Second)
	if err != nil {
		t.Fatalf("orphan re-Pop: %v", err)
	}
	if second.Attempts != 1 {
		t.Fatalf("recovered job attempts = %d, want 1", second.Attempts)
	}
}

// TestConcurrentReservers_NoErrorsNoDoubleProcessing spins up N workers
// reserving from the same SQLite-backed queue. None must error with a
// lock and no job may be claimed twice. Regression for SQLITE_LOCKED on
// the SELECT-then-UPDATE lock upgrade.
func TestConcurrentReservers_NoErrorsNoDoubleProcessing(t *testing.T) {
	// A long visibility timeout so orphan recovery cannot re-deliver and
	// confuse the dedup check.
	conn := newConn(t)
	q, err := New(conn, WithVisibilityTimeout(time.Hour))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := q.Setup(context.Background()); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	ctx := context.Background()
	const nJobs = 60
	for i := 0; i < nJobs; i++ {
		if err := q.Push(ctx, queue.Job{
			ID:      "c-" + strconv.Itoa(i),
			Name:    "x",
			Payload: []byte("p"),
		}); err != nil {
			t.Fatalf("Push %d: %v", i, err)
		}
	}

	const workers = 8
	var (
		mu      sync.Mutex
		seen    = map[string]bool{}
		claimed int32
		wg      sync.WaitGroup
	)
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				j, err := q.Pop(ctx, 100*time.Millisecond)
				if errors.Is(err, queue.ErrEmpty) {
					return
				}
				if err != nil {
					t.Errorf("Pop errored under concurrency: %v", err)
					return
				}
				mu.Lock()
				if seen[j.ID] {
					t.Errorf("job %s reserved twice", j.ID)
				}
				seen[j.ID] = true
				mu.Unlock()
				atomic.AddInt32(&claimed, 1)
				_ = q.Ack(ctx, j.ID)
			}
		}()
	}
	wg.Wait()

	if int(claimed) != nJobs {
		t.Fatalf("claimed %d jobs, want %d", claimed, nJobs)
	}
}

// TestVisibilityTimeout_TwoWorkersNoDoubleProcessWithinWindow asserts
// that once one worker reserves a job, a second worker polling the same
// queue cannot also claim it until the visibility timeout elapses — and
// then it reappears exactly once. Regression for double-processing
// within the visibility window.
func TestVisibilityTimeout_TwoWorkersNoDoubleProcessWithinWindow(t *testing.T) {
	conn := newConn(t)
	const visTout = 250 * time.Millisecond
	q, err := New(conn, WithVisibilityTimeout(visTout))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := q.Setup(context.Background()); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	ctx := context.Background()
	if err := q.Push(ctx, queue.Job{ID: "vis-1", Name: "x", Payload: []byte("p")}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Worker A reserves the job and abandons it (simulating a crash mid-flight).
	a, err := q.Pop(ctx, time.Second)
	if err != nil {
		t.Fatalf("worker A Pop: %v", err)
	}
	if a.ID != "vis-1" {
		t.Fatalf("worker A got %q", a.ID)
	}

	// Worker B repeatedly polls during the visibility window: it must
	// never see the still-reserved job.
	bDeadline := time.Now().Add(visTout - 50*time.Millisecond)
	for time.Now().Before(bDeadline) {
		if _, err := q.Pop(ctx, 20*time.Millisecond); !errors.Is(err, queue.ErrEmpty) {
			t.Fatalf("worker B claimed a reserved job within the visibility window: err=%v", err)
		}
	}

	// After the timeout, the orphaned job becomes visible again — and
	// exactly one of the workers can reclaim it.
	time.Sleep(visTout)
	b, err := q.Pop(ctx, time.Second)
	if err != nil {
		t.Fatalf("orphan never re-delivered after visibility timeout: %v", err)
	}
	if b.ID != "vis-1" {
		t.Fatalf("worker B got %q, want vis-1", b.ID)
	}
	if b.Attempts != 1 {
		t.Fatalf("recovered job attempts = %d, want 1", b.Attempts)
	}

	// Ack it; the queue must now be empty (no duplicate row lingering).
	if err := q.Ack(ctx, b.ID); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if q.Len() != 0 {
		t.Fatalf("Len after Ack = %d, want 0", q.Len())
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
