package queue

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingQueue wraps a Queue and records every Ack/Nack so tests can
// assert at-least-once delivery semantics precisely (e.g. a successful
// job is acked exactly once and never re-run).
type countingQueue struct {
	Queue
	acks  int32
	nacks int32
}

func newCountingQueue() *countingQueue { return &countingQueue{Queue: NewMemoryQueue()} }

func (c *countingQueue) Ack(ctx context.Context, id string) error {
	atomic.AddInt32(&c.acks, 1)
	return c.Queue.Ack(ctx, id)
}

func (c *countingQueue) Nack(ctx context.Context, id string, d time.Duration) error {
	atomic.AddInt32(&c.nacks, 1)
	return c.Queue.Nack(ctx, id, d)
}

type ReliabilityJob struct{ ID int }

// TestReliability_SuccessAckedExactlyOnce verifies a successful job runs
// once and is acked exactly once — never nacked, never re-delivered.
func TestReliability_SuccessAckedExactlyOnce(t *testing.T) {
	q := newCountingQueue()
	w := NewWorker(q).Poll(10 * time.Millisecond)

	var runs int32
	done := make(chan struct{})
	Handle[ReliabilityJob](w, func(_ context.Context, _ ReliabilityJob) error {
		atomic.AddInt32(&runs, 1)
		close(done)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx)
	defer w.Stop()

	if err := Dispatch(ctx, q, ReliabilityJob{ID: 1}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}
	// Give any (erroneous) re-delivery a chance to occur.
	time.Sleep(80 * time.Millisecond)

	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Fatalf("handler ran %d times, want exactly 1", n)
	}
	if a := atomic.LoadInt32(&q.acks); a != 1 {
		t.Fatalf("acks = %d, want exactly 1", a)
	}
	if n := atomic.LoadInt32(&q.nacks); n != 0 {
		t.Fatalf("nacks = %d, want 0 for a successful job", n)
	}
	if q.Len() != 0 {
		t.Fatalf("queue not drained, len = %d", q.Len())
	}
}

// TestReliability_RetriedThenFailedRoutedToOnFailed verifies a job whose
// handler always errors is retried up to MaxRetry with backoff and then
// routed to the failed sink (OnFailed) exactly once before being dropped.
func TestReliability_RetriedThenFailedRoutedToOnFailed(t *testing.T) {
	q := newCountingQueue()
	const maxRetry = 3
	failErr := errors.New("permanent failure")

	var attempts int32
	failedCount := int32(0)
	failed := make(chan Job, 1)
	var lastErr error

	w := NewWorker(q).
		Poll(5 * time.Millisecond).
		Backoff(10 * time.Millisecond).
		MaxRetry(maxRetry).
		OnFailed(func(j Job, err error) {
			atomic.AddInt32(&failedCount, 1)
			lastErr = err
			failed <- j
		})
	Handle[ReliabilityJob](w, func(_ context.Context, _ ReliabilityJob) error {
		atomic.AddInt32(&attempts, 1)
		return failErr
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go w.Run(ctx)
	defer w.Stop()

	if err := Dispatch(ctx, q, ReliabilityJob{ID: 2}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	select {
	case j := <-failed:
		if j.Name != jobName(ReliabilityJob{}) {
			t.Fatalf("OnFailed routed wrong job: %+v", j)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("job never routed to failed; attempts=%d", atomic.LoadInt32(&attempts))
	}
	// Let the worker settle so no extra retries/failures slip in.
	time.Sleep(100 * time.Millisecond)

	if a := atomic.LoadInt32(&attempts); a != maxRetry {
		t.Fatalf("attempts = %d, want exactly MaxRetry=%d", a, maxRetry)
	}
	if !errors.Is(lastErr, failErr) {
		t.Fatalf("OnFailed err = %v, want %v", lastErr, failErr)
	}
	if fc := atomic.LoadInt32(&failedCount); fc != 1 {
		t.Fatalf("OnFailed fired %d times, want exactly 1", fc)
	}
	// nacks == maxRetry-1 (one per failed-but-not-exhausted attempt);
	// the final exhausting attempt drops via Ack, not Nack.
	if n := atomic.LoadInt32(&q.nacks); n != maxRetry-1 {
		t.Fatalf("nacks = %d, want %d", n, maxRetry-1)
	}
	if q.Len() != 0 {
		t.Fatalf("failed job not dropped, len = %d", q.Len())
	}
}

type CrashJob struct{ ID int }
type SurvivorJob struct{ ID int }

// TestReliability_PanicRecoveredWorkerSurvives asserts that a handler
// panic is recovered, counted as a failure (retried/failed per policy),
// and the worker goroutine keeps running so a later job still executes.
func TestReliability_PanicRecoveredWorkerSurvives(t *testing.T) {
	q := newCountingQueue()
	const maxRetry = 2

	var panicAttempts int32
	failed := make(chan struct{}, 1)
	survivorRan := make(chan struct{})

	w := NewWorker(q).
		Poll(5 * time.Millisecond).
		Backoff(10 * time.Millisecond).
		MaxRetry(maxRetry).
		OnFailed(func(_ Job, _ error) {
			select {
			case failed <- struct{}{}:
			default:
			}
		})
	Handle[CrashJob](w, func(_ context.Context, _ CrashJob) error {
		atomic.AddInt32(&panicAttempts, 1)
		panic("handler exploded")
	})
	Handle[SurvivorJob](w, func(_ context.Context, _ SurvivorJob) error {
		close(survivorRan)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go w.Run(ctx)
	defer w.Stop()

	if err := Dispatch(ctx, q, CrashJob{ID: 1}); err != nil {
		t.Fatalf("Dispatch crash: %v", err)
	}
	if err := Dispatch(ctx, q, SurvivorJob{ID: 2}); err != nil {
		t.Fatalf("Dispatch survivor: %v", err)
	}

	// The panicking job must be counted as a failure (routed to OnFailed
	// after exhausting retries) — proving the panic became a normal error.
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("panicking job never reached OnFailed (panic not treated as failure)")
	}
	// The worker goroutine must NOT have died: a later job still runs.
	select {
	case <-survivorRan:
	case <-time.After(5 * time.Second):
		t.Fatal("worker goroutine died on panic; survivor job never ran")
	}
	if a := atomic.LoadInt32(&panicAttempts); a != maxRetry {
		t.Fatalf("panicking handler ran %d times, want MaxRetry=%d (retried like any error)", a, maxRetry)
	}
}

// TestReliability_GracefulShutdownNoGoroutineLeak verifies Worker.Stop()
// drains cleanly and leaks no goroutines.
//
// In-flight semantics: Run dispatches handlers synchronously, so a job
// already in flight when Stop is called runs to completion (and is
// acked/nacked) before Run observes the stop signal and returns. Nothing
// is requeued for an in-flight job on a clean Stop; only an uncompleted
// (never-acked) job left after a crash is requeued — and only by a
// persistent driver via its visibility timeout, not the memory driver.
func TestReliability_GracefulShutdownNoGoroutineLeak(t *testing.T) {
	// Settle background goroutines from earlier tests / the runtime.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	before := runtime.NumGoroutine()

	q := NewMemoryQueue()
	w := NewWorker(q).Poll(10 * time.Millisecond)

	inHandler := make(chan struct{})
	releaseHandler := make(chan struct{})
	var ran int32
	Handle[ReliabilityJob](w, func(_ context.Context, _ ReliabilityJob) error {
		atomic.AddInt32(&ran, 1)
		close(inHandler)
		<-releaseHandler // hold the worker in-flight until the test allows it
		return nil
	})

	ctx := context.Background()
	go w.Run(ctx)

	if err := Dispatch(ctx, q, ReliabilityJob{ID: 1}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// Wait until the handler is mid-flight, then trigger shutdown.
	select {
	case <-inHandler:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never started")
	}

	stopReturned := make(chan struct{})
	go func() {
		w.Stop()
		close(stopReturned)
	}()

	// Stop must block while the in-flight job is still running.
	select {
	case <-stopReturned:
		t.Fatal("Stop returned before in-flight job finished")
	case <-time.After(100 * time.Millisecond):
	}

	// Let the in-flight job complete; Stop should then return promptly.
	close(releaseHandler)
	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after in-flight job finished")
	}

	if n := atomic.LoadInt32(&ran); n != 1 {
		t.Fatalf("in-flight job ran %d times, want 1", n)
	}
	// In-flight job finished and was acked — queue is empty, nothing requeued.
	if q.Len() != 0 {
		t.Fatalf("queue not empty after clean shutdown, len = %d", q.Len())
	}

	// Allow the Run goroutine to fully unwind, then assert no leak.
	deadline := time.Now().Add(2 * time.Second)
	var after int
	for time.Now().Before(deadline) {
		runtime.GC()
		after = runtime.NumGoroutine()
		if after <= before {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if after > before {
		t.Fatalf("goroutine leak on Stop: before=%d after=%d", before, after)
	}
}

// TestReliability_ConcurrentWorkersNoDoubleAck runs multiple workers on
// one memory queue and asserts every job is handled exactly once (no
// double-ack, no lost job) under -race.
func TestReliability_ConcurrentWorkersNoDoubleAck(t *testing.T) {
	q := NewMemoryQueue()
	const nJobs = 200
	const nWorkers = 6

	var (
		mu       sync.Mutex
		seen     = make(map[int]int) // job id -> run count
		handled  int32
		workers  []*Worker
		ctx, cxl = context.WithTimeout(context.Background(), 10*time.Second)
	)
	defer cxl()

	for i := 0; i < nWorkers; i++ {
		w := NewWorker(q).Poll(5 * time.Millisecond)
		Handle[ReliabilityJob](w, func(_ context.Context, j ReliabilityJob) error {
			mu.Lock()
			seen[j.ID]++
			mu.Unlock()
			atomic.AddInt32(&handled, 1)
			return nil
		})
		workers = append(workers, w)
		go w.Run(ctx)
	}
	defer func() {
		for _, w := range workers {
			w.Stop()
		}
	}()

	for i := 0; i < nJobs; i++ {
		if err := Dispatch(ctx, q, ReliabilityJob{ID: i}); err != nil {
			t.Fatalf("Dispatch %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&handled) < nJobs {
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != nJobs {
		t.Fatalf("handled %d distinct jobs, want %d", len(seen), nJobs)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("job %d handled %d times, want exactly 1 (double-processing)", id, c)
		}
	}
}
