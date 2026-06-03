// Package queue provides a job queue abstraction modelled on Laravel's
// Queue facade.
//
// Three pieces:
//
//   - Job          — a payload that describes the work to do, carried as
//                    a name + bytes (typed via Register/Marshal).
//   - Queue        — the transport (push/pop/ack/nack). Memory driver
//                    ships in-box; DB/Redis live in sub-packages.
//   - Worker       — long-running consumer that pulls Jobs off a Queue
//                    and dispatches them to registered Handler funcs.
//
// Usage:
//
//	type SendWelcomeEmail struct{ UserID uint64 }
//
//	q := queue.NewMemoryQueue()
//	w := queue.NewWorker(q)
//	queue.Handle[SendWelcomeEmail](w, func(ctx context.Context, j SendWelcomeEmail) error {
//	    return mailer.Send(ctx, buildEmail(j.UserID))
//	})
//	go w.Run(ctx)
//
//	_ = queue.Dispatch(ctx, q, SendWelcomeEmail{UserID: 42})
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

// ErrEmpty is returned by Queue.Pop when no job is available within
// the configured wait window.
var ErrEmpty = errors.New("queue: empty")

// Job is the wire-format payload exchanged between producers and the
// queue transport. Name identifies the handler; Payload carries the
// JSON-encoded user-defined struct.
type Job struct {
	ID         string
	Name       string
	Payload    []byte
	Attempts   int
	AvailableAt time.Time
}

// Queue is the transport interface implemented by drivers.
type Queue interface {
	// Push enqueues a job. Implementations should respect job.AvailableAt
	// (deferred delivery) if non-zero.
	Push(ctx context.Context, j Job) error
	// Pop blocks until a job becomes available or wait elapses, in
	// which case ErrEmpty is returned. The implementation must
	// guarantee at-least-once semantics — Ack/Nack closes the loop.
	Pop(ctx context.Context, wait time.Duration) (Job, error)
	// Ack signals successful handling so the job is removed.
	Ack(ctx context.Context, jobID string) error
	// Nack returns the job to the queue (or to a delayed retry slot
	// when retryAfter > 0).
	Nack(ctx context.Context, jobID string, retryAfter time.Duration) error
	// Len returns the approximate number of pending jobs (driver-
	// specific; primarily for tests/metrics).
	Len() int
}

// Dispatch JSON-encodes payload and pushes it onto q. The job name is
// the Go type name of payload (e.g. "SendWelcomeEmail"). The Worker
// looks up the handler by the same key.
func Dispatch[T any](ctx context.Context, q Queue, payload T) error {
	name := jobName(payload)
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("queue: marshal %s: %w", name, err)
	}
	return q.Push(ctx, Job{
		ID:      newID(),
		Name:    name,
		Payload: buf,
	})
}

// DispatchAfter is Dispatch with a delivery delay.
func DispatchAfter[T any](ctx context.Context, q Queue, payload T, delay time.Duration) error {
	name := jobName(payload)
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("queue: marshal %s: %w", name, err)
	}
	return q.Push(ctx, Job{
		ID:          newID(),
		Name:        name,
		Payload:     buf,
		AvailableAt: time.Now().Add(delay),
	})
}

func jobName(v any) string {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.PkgPath() + "." + t.Name()
}

func newID() string {
	// Monotonic enough for in-process — drivers backed by a real
	// store will rewrite IDs to their native scheme on push.
	return fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&idCounter, 1))
}

var idCounter uint64

// Handler runs a typed job. The Worker decodes Job.Payload into T
// before invoking the handler.
type Handler func(ctx context.Context, raw []byte) error

// Worker pulls jobs off a Queue and dispatches them to registered
// handlers.
type Worker struct {
	q         Queue
	mu        sync.RWMutex
	handlers  map[string]Handler
	maxRetry  int
	backoff   time.Duration
	logger    func(string, ...any)
	poll      time.Duration
	stop      chan struct{}
	stopped   chan struct{}
}

// NewWorker returns a Worker bound to q. Defaults: 3 max attempts,
// 5s backoff, 1s poll interval. Tune via setters.
func NewWorker(q Queue) *Worker {
	return &Worker{
		q:        q,
		handlers: map[string]Handler{},
		maxRetry: 3,
		backoff:  5 * time.Second,
		logger:   func(string, ...any) {}, // silent by default
		poll:     time.Second,
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// MaxRetry sets how many times a failing job is retried before being
// dropped. The original attempt counts as 1.
func (w *Worker) MaxRetry(n int) *Worker { w.maxRetry = n; return w }

// Backoff sets the initial delay before retrying a failed job. Each
// retry doubles the delay.
func (w *Worker) Backoff(d time.Duration) *Worker { w.backoff = d; return w }

// Poll sets how long Pop blocks waiting for the next job before
// looping. Lower values cut latency at the cost of CPU.
func (w *Worker) Poll(d time.Duration) *Worker { w.poll = d; return w }

// Logger installs a structured logger callback.
func (w *Worker) Logger(fn func(string, ...any)) *Worker { w.logger = fn; return w }

// Handle registers a typed handler for jobs of T. T must JSON-decode
// from the payload bytes.
func Handle[T any](w *Worker, fn func(ctx context.Context, payload T) error) {
	var zero T
	name := jobName(zero)
	w.mu.Lock()
	w.handlers[name] = func(ctx context.Context, raw []byte) error {
		var p T
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("queue: unmarshal %s: %w", name, err)
		}
		return fn(ctx, p)
	}
	w.mu.Unlock()
}

// Run blocks the calling goroutine, pulling jobs until ctx is cancelled
// or Stop is called.
func (w *Worker) Run(ctx context.Context) error {
	defer close(w.stopped)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.stop:
			return nil
		default:
		}
		j, err := w.q.Pop(ctx, w.poll)
		if errors.Is(err, ErrEmpty) {
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if err != nil {
			w.logger("queue: pop error: %v", err)
			continue
		}
		w.handle(ctx, j)
	}
}

// Stop signals Run to exit. Blocks until Run returns.
func (w *Worker) Stop() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	<-w.stopped
}

func (w *Worker) handle(ctx context.Context, j Job) {
	w.mu.RLock()
	h, ok := w.handlers[j.Name]
	w.mu.RUnlock()
	if !ok {
		w.logger("queue: no handler for %s (job %s) — dropping", j.Name, j.ID)
		_ = w.q.Ack(ctx, j.ID)
		return
	}
	err := h(ctx, j.Payload)
	if err == nil {
		_ = w.q.Ack(ctx, j.ID)
		return
	}
	if j.Attempts+1 >= w.maxRetry {
		w.logger("queue: %s exhausted retries: %v", j.ID, err)
		_ = w.q.Ack(ctx, j.ID) // drop
		return
	}
	delay := w.backoff * time.Duration(1<<j.Attempts) // exponential
	w.logger("queue: %s attempt %d failed: %v (retry in %s)", j.ID, j.Attempts+1, err, delay)
	_ = w.q.Nack(ctx, j.ID, delay)
}

// --- MemoryQueue --------------------------------------------------------

// MemoryQueue is an in-process FIFO queue with delayed delivery and
// at-least-once semantics. Suitable for single-process apps and tests.
type MemoryQueue struct {
	mu      sync.Mutex
	ready   []Job
	delayed []Job
	in      map[string]Job // jobs currently being processed (between Pop and Ack/Nack)
	signal  chan struct{}
}

// NewMemoryQueue returns an empty in-memory Queue.
func NewMemoryQueue() *MemoryQueue {
	return &MemoryQueue{
		in:     map[string]Job{},
		signal: make(chan struct{}, 1),
	}
}

func (q *MemoryQueue) Push(_ context.Context, j Job) error {
	q.mu.Lock()
	if !j.AvailableAt.IsZero() && j.AvailableAt.After(time.Now()) {
		q.delayed = append(q.delayed, j)
	} else {
		q.ready = append(q.ready, j)
	}
	q.mu.Unlock()
	q.wake()
	return nil
}

func (q *MemoryQueue) Pop(ctx context.Context, wait time.Duration) (Job, error) {
	deadline := time.Now().Add(wait)
	for {
		q.promoteDelayed()
		q.mu.Lock()
		if len(q.ready) > 0 {
			j := q.ready[0]
			q.ready = q.ready[1:]
			q.in[j.ID] = j
			q.mu.Unlock()
			return j, nil
		}
		q.mu.Unlock()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return Job{}, ErrEmpty
		}
		// Sleep for the smaller of the wait window or 50ms so delayed
		// jobs surface promptly.
		w := remaining
		if w > 50*time.Millisecond {
			w = 50 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return Job{}, ctx.Err()
		case <-q.signal:
		case <-time.After(w):
		}
	}
}

func (q *MemoryQueue) Ack(_ context.Context, id string) error {
	q.mu.Lock()
	delete(q.in, id)
	q.mu.Unlock()
	return nil
}

func (q *MemoryQueue) Nack(_ context.Context, id string, retryAfter time.Duration) error {
	q.mu.Lock()
	j, ok := q.in[id]
	delete(q.in, id)
	q.mu.Unlock()
	if !ok {
		return nil
	}
	j.Attempts++
	if retryAfter > 0 {
		j.AvailableAt = time.Now().Add(retryAfter)
		q.mu.Lock()
		q.delayed = append(q.delayed, j)
		q.mu.Unlock()
	} else {
		q.mu.Lock()
		q.ready = append(q.ready, j)
		q.mu.Unlock()
	}
	q.wake()
	return nil
}

func (q *MemoryQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.ready) + len(q.delayed) + len(q.in)
}

func (q *MemoryQueue) promoteDelayed() {
	now := time.Now()
	q.mu.Lock()
	kept := q.delayed[:0]
	for _, j := range q.delayed {
		if !j.AvailableAt.After(now) {
			q.ready = append(q.ready, j)
		} else {
			kept = append(kept, j)
		}
	}
	q.delayed = kept
	q.mu.Unlock()
}

func (q *MemoryQueue) wake() {
	select {
	case q.signal <- struct{}{}:
	default:
	}
}
