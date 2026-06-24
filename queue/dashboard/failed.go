package dashboard

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/devituz/lagodev/queue"
)

// ErrNotFound is returned by FailedJobs.Retry / Forget when the given id
// does not match a stored failed job.
var ErrNotFound = errors.New("dashboard: failed job not found")

// FailedJob is a dead-lettered job plus the metadata the dashboard needs
// to display and act on it. The embedded queue.Job is the original
// payload that Retry re-enqueues verbatim.
type FailedJob struct {
	ID       string    `json:"id"`        // store-local identity (defaults to Job.ID)
	Queue    string    `json:"queue"`     // logical queue label
	Job      queue.Job `json:"job"`       // original job, replayed on retry
	Err      string    `json:"error"`     // last handler error message
	FailedAt time.Time `json:"failed_at"` // when it was dead-lettered
}

// FailedJobs is a pluggable store of dead-lettered jobs. The dashboard
// reads it for the failed-jobs view and mutates it through the retry /
// forget actions. Implementations must be safe for concurrent use.
//
// MemoryFailedJobs is the reference implementation; a sqlqueue-backed
// store can be supplied by satisfying this interface (e.g. selecting from
// a `failed_jobs` table and re-inserting into the jobs table on Retry).
type FailedJobs interface {
	// List returns every stored failed job. Ordering is implementation-
	// defined; the reference store returns newest-first.
	List(ctx context.Context) ([]FailedJob, error)
	// Retry re-enqueues the job with the given id and removes it from the
	// store. Returns ErrNotFound if no such job exists.
	Retry(ctx context.Context, id string) error
	// Forget permanently drops the job with the given id without
	// re-enqueueing. Returns ErrNotFound if no such job exists.
	Forget(ctx context.Context, id string) error
}

// RequeueFunc re-enqueues a job during Retry. It typically wraps a
// Queue.Push. A nil RequeueFunc makes Retry forget-only (it still removes
// the record but performs no enqueue), which is occasionally useful in
// read-only dashboards.
type RequeueFunc func(ctx context.Context, j queue.Job) error

// MemoryFailedJobs is an in-process FailedJobs store. It is the reference
// implementation used by tests and single-process apps. Safe for
// concurrent use.
type MemoryFailedJobs struct {
	mu      sync.Mutex
	items   map[string]FailedJob
	order   []string // insertion order, newest appended last
	requeue RequeueFunc
	stats   *Stats // optional: OnRetried is bumped on successful Retry
}

// NewMemoryFailedJobs returns an empty store. requeue is invoked by Retry
// to push the job back onto its queue; pass nil for a forget-only store.
func NewMemoryFailedJobs(requeue RequeueFunc) *MemoryFailedJobs {
	return &MemoryFailedJobs{
		items:   map[string]FailedJob{},
		requeue: requeue,
	}
}

// WithStats wires a Stats so successful Retry calls bump OnRetried for the
// job's queue. Returns the receiver for chaining.
func (m *MemoryFailedJobs) WithStats(s *Stats) *MemoryFailedJobs {
	m.stats = s
	return m
}

// Record stores a dead-lettered job. Call it from queue.Worker.OnFailed.
// queueName is the logical queue label; errMsg is the last handler error.
// The store id defaults to the job's ID.
func (m *MemoryFailedJobs) Record(j queue.Job, queueName string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	m.add(FailedJob{
		ID:       j.ID,
		Queue:    queueName,
		Job:      j,
		Err:      msg,
		FailedAt: nowFunc(),
	})
}

func (m *MemoryFailedJobs) add(f FailedJob) {
	m.mu.Lock()
	if _, exists := m.items[f.ID]; !exists {
		m.order = append(m.order, f.ID)
	}
	m.items[f.ID] = f
	m.mu.Unlock()
}

// List returns stored failed jobs newest-first.
func (m *MemoryFailedJobs) List(_ context.Context) ([]FailedJob, error) {
	m.mu.Lock()
	out := make([]FailedJob, 0, len(m.order))
	for _, id := range m.order {
		if f, ok := m.items[id]; ok {
			out = append(out, f)
		}
	}
	m.mu.Unlock()
	// Newest-first: reverse the insertion order. Stable on FailedAt for
	// records inserted within the same instant.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].FailedAt.After(out[j].FailedAt)
	})
	return out, nil
}

// Retry re-enqueues the job via the configured RequeueFunc and removes it
// from the store. If requeue fails the record is kept so the action can be
// retried, and the error is returned.
func (m *MemoryFailedJobs) Retry(ctx context.Context, id string) error {
	m.mu.Lock()
	f, ok := m.items[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if m.requeue != nil {
		if err := m.requeue(ctx, f.Job); err != nil {
			return err
		}
	}
	m.remove(id)
	if m.stats != nil {
		m.stats.OnRetried(f.Queue)
	}
	return nil
}

// Forget drops the record without re-enqueueing.
func (m *MemoryFailedJobs) Forget(_ context.Context, id string) error {
	m.mu.Lock()
	_, ok := m.items[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	m.remove(id)
	return nil
}

func (m *MemoryFailedJobs) remove(id string) {
	m.mu.Lock()
	delete(m.items, id)
	for i, oid := range m.order {
		if oid == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
}

// Len reports the number of stored failed jobs (primarily for tests).
func (m *MemoryFailedJobs) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

// Static assertion: *MemoryFailedJobs satisfies FailedJobs.
var _ FailedJobs = (*MemoryFailedJobs)(nil)
