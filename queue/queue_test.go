package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type SendEmail struct {
	UserID uint64
	To     string
}

func TestMemoryQueue_PushPopRoundtrip(t *testing.T) {
	q := NewMemoryQueue()
	ctx := context.Background()
	if err := Dispatch(ctx, q, SendEmail{UserID: 1, To: "a@x"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	j, err := q.Pop(ctx, time.Second)
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if j.Name != jobName(SendEmail{}) {
		t.Fatalf("job name = %q", j.Name)
	}
	if len(j.Payload) == 0 {
		t.Fatal("payload empty")
	}
}

func TestMemoryQueue_EmptyReturnsErrEmpty(t *testing.T) {
	q := NewMemoryQueue()
	_, err := q.Pop(context.Background(), 50*time.Millisecond)
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("want ErrEmpty, got %v", err)
	}
}

func TestMemoryQueue_DeliversInFIFOOrder(t *testing.T) {
	q := NewMemoryQueue()
	ctx := context.Background()
	_ = Dispatch(ctx, q, SendEmail{UserID: 1})
	_ = Dispatch(ctx, q, SendEmail{UserID: 2})
	_ = Dispatch(ctx, q, SendEmail{UserID: 3})
	for _, want := range []uint64{1, 2, 3} {
		j, _ := q.Pop(ctx, time.Second)
		var p SendEmail
		_ = jsonUnmarshal(j.Payload, &p)
		if p.UserID != want {
			t.Fatalf("FIFO broken: got %d want %d", p.UserID, want)
		}
		_ = q.Ack(ctx, j.ID)
	}
}

func TestWorker_HandlesAndAcks(t *testing.T) {
	q := NewMemoryQueue()
	w := NewWorker(q).Poll(10 * time.Millisecond)
	var got SendEmail
	done := make(chan struct{})
	Handle[SendEmail](w, func(_ context.Context, p SendEmail) error {
		got = p
		close(done)
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx)
	defer w.Stop()

	_ = Dispatch(ctx, q, SendEmail{UserID: 42, To: "z@x"})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}
	if got.UserID != 42 {
		t.Fatalf("got %+v", got)
	}
	// allow Ack to settle
	time.Sleep(50 * time.Millisecond)
	if q.Len() != 0 {
		t.Fatalf("queue must be empty after Ack, len=%d", q.Len())
	}
}

func TestWorker_RetriesOnFailure(t *testing.T) {
	q := NewMemoryQueue()
	w := NewWorker(q).Poll(10 * time.Millisecond).Backoff(20 * time.Millisecond).MaxRetry(3)
	var attempts int32
	done := make(chan struct{})
	Handle[SendEmail](w, func(_ context.Context, _ SendEmail) error {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			return errors.New("transient")
		}
		close(done)
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go w.Run(ctx)
	defer w.Stop()

	_ = Dispatch(ctx, q, SendEmail{UserID: 7})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("never succeeded; attempts=%d", attempts)
	}
	if attempts != 3 {
		t.Fatalf("want 3 attempts, got %d", attempts)
	}
}

func TestWorker_DropsAfterMaxRetry(t *testing.T) {
	q := NewMemoryQueue()
	w := NewWorker(q).Poll(10 * time.Millisecond).Backoff(10 * time.Millisecond).MaxRetry(2)
	var attempts int32
	Handle[SendEmail](w, func(_ context.Context, _ SendEmail) error {
		atomic.AddInt32(&attempts, 1)
		return errors.New("always fails")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx)
	defer w.Stop()
	_ = Dispatch(ctx, q, SendEmail{UserID: 1})
	// wait for retries to complete
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && q.Len() > 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if q.Len() != 0 {
		t.Fatalf("must drop after max retries, len=%d", q.Len())
	}
	if attempts < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", attempts)
	}
}

func TestDispatchAfter_DelaysDelivery(t *testing.T) {
	q := NewMemoryQueue()
	ctx := context.Background()
	if err := DispatchAfter(ctx, q, SendEmail{UserID: 1}, 80*time.Millisecond); err != nil {
		t.Fatalf("DispatchAfter: %v", err)
	}
	_, err := q.Pop(ctx, 20*time.Millisecond)
	if !errors.Is(err, ErrEmpty) {
		t.Fatal("must not be available before delay elapses")
	}
	_, err = q.Pop(ctx, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("must be available after delay, got %v", err)
	}
}

func TestNack_WithoutDelayRequeuesImmediately(t *testing.T) {
	q := NewMemoryQueue()
	ctx := context.Background()
	_ = Dispatch(ctx, q, SendEmail{UserID: 9})
	j, _ := q.Pop(ctx, time.Second)
	_ = q.Nack(ctx, j.ID, 0)
	j2, err := q.Pop(ctx, time.Second)
	if err != nil {
		t.Fatalf("re-pop failed: %v", err)
	}
	if j2.Attempts != 1 {
		t.Fatalf("attempt count = %d", j2.Attempts)
	}
}

func TestWorker_NoHandlerDropsJob(t *testing.T) {
	q := NewMemoryQueue()
	w := NewWorker(q).Poll(10 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx)
	defer w.Stop()
	_ = Dispatch(ctx, q, SendEmail{UserID: 1}) // no handler registered
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && q.Len() > 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if q.Len() != 0 {
		t.Fatalf("unhandled job must be dropped, len=%d", q.Len())
	}
}

func TestMemoryQueue_ConcurrentProducersConsumers(t *testing.T) {
	q := NewMemoryQueue()
	ctx := context.Background()
	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = Dispatch(ctx, q, SendEmail{UserID: uint64(i)})
		}(i)
	}
	wg.Wait()
	if q.Len() != n {
		t.Fatalf("Len after %d Dispatches = %d", n, q.Len())
	}
	for i := 0; i < n; i++ {
		j, err := q.Pop(ctx, time.Second)
		if err != nil {
			t.Fatalf("Pop %d: %v", i, err)
		}
		_ = q.Ack(ctx, j.ID)
	}
}

// jsonUnmarshal is a tiny shim so we don't need to import encoding/json
// just for one test that needed it.
func jsonUnmarshal(b []byte, dst any) error {
	return jsonImpl(b, dst)
}

type PanicJob struct{ ID int }
type NormalJob struct{ ID int }

// TestWorker_PanicInHandlerDoesNotKillWorker ensures a panicking handler
// is recovered and a subsequent normal job still runs. Regression for
// the worker goroutine dying on handler panic.
func TestWorker_PanicInHandlerDoesNotKillWorker(t *testing.T) {
	q := NewMemoryQueue()
	w := NewWorker(q).Poll(10 * time.Millisecond).Backoff(10 * time.Millisecond).MaxRetry(1)
	normalRan := make(chan struct{})
	Handle[PanicJob](w, func(_ context.Context, _ PanicJob) error {
		panic("boom")
	})
	Handle[NormalJob](w, func(_ context.Context, _ NormalJob) error {
		close(normalRan)
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx)
	defer w.Stop()

	_ = Dispatch(ctx, q, PanicJob{ID: 1})
	_ = Dispatch(ctx, q, NormalJob{ID: 2})

	select {
	case <-normalRan:
	case <-time.After(3 * time.Second):
		t.Fatal("worker died on panic; normal job never ran")
	}
}

// TestWorker_OnFailedCalledOnExhaustion verifies the OnFailed hook fires
// with the job and last error before a job is dropped.
func TestWorker_OnFailedCalledOnExhaustion(t *testing.T) {
	q := NewMemoryQueue()
	failErr := errors.New("permanent")
	failed := make(chan Job, 1)
	var gotErr error
	w := NewWorker(q).
		Poll(10 * time.Millisecond).
		Backoff(10 * time.Millisecond).
		MaxRetry(2).
		OnFailed(func(j Job, err error) {
			gotErr = err
			failed <- j
		})
	Handle[SendEmail](w, func(_ context.Context, _ SendEmail) error {
		return failErr
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx)
	defer w.Stop()

	_ = Dispatch(ctx, q, SendEmail{UserID: 5})
	select {
	case j := <-failed:
		if j.Name != jobName(SendEmail{}) {
			t.Fatalf("OnFailed got wrong job: %+v", j)
		}
		if !errors.Is(gotErr, failErr) {
			t.Fatalf("OnFailed got err %v, want %v", gotErr, failErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnFailed never fired on retry exhaustion")
	}
}

// TestWorker_StopWithoutRunReturnsQuickly ensures Stop does not deadlock
// when Run was never started.
func TestWorker_StopWithoutRunReturnsQuickly(t *testing.T) {
	w := NewWorker(NewMemoryQueue())
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop deadlocked when Run was never started")
	}
}

// TestWorker_Restartable ensures a Worker can be run again after Stop.
func TestWorker_Restartable(t *testing.T) {
	q := NewMemoryQueue()
	w := NewWorker(q).Poll(10 * time.Millisecond)
	runs := make(chan int, 2)
	Handle[SendEmail](w, func(_ context.Context, p SendEmail) error {
		runs <- int(p.UserID)
		return nil
	})

	ctx := context.Background()
	go w.Run(ctx)
	_ = Dispatch(ctx, q, SendEmail{UserID: 1})
	select {
	case <-runs:
	case <-time.After(2 * time.Second):
		t.Fatal("first run: handler never ran")
	}
	w.Stop()

	// Restart.
	go w.Run(ctx)
	defer w.Stop()
	_ = Dispatch(ctx, q, SendEmail{UserID: 2})
	select {
	case <-runs:
	case <-time.After(2 * time.Second):
		t.Fatal("after restart: handler never ran")
	}
}

// TestBackoffMultiplier_NoOverflow guards the exponential backoff shift
// against overflowing the Duration multiplication on high attempt counts.
func TestBackoffMultiplier_NoOverflow(t *testing.T) {
	if got := backoffMultiplier(0); got != 1 {
		t.Fatalf("attempts=0 -> %d, want 1", got)
	}
	if got := backoffMultiplier(3); got != 8 {
		t.Fatalf("attempts=3 -> %d, want 8", got)
	}
	// Way past the clamp — must not produce a negative/zero multiplier.
	if got := backoffMultiplier(200); got <= 0 {
		t.Fatalf("attempts=200 overflowed: %d", got)
	}
}
