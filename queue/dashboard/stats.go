// Package dashboard provides a Horizon / Bull-Board-style operational
// view over the parent queue package: live throughput, per-queue
// counters, and a failed-job list with retry / forget actions.
//
// It is deliberately decoupled from any concrete Queue driver. Two seams
// connect it to a running system:
//
//   - Stats — a concurrency-safe counter set the worker feeds via the
//     OnPushed / OnProcessed / OnFailed / OnRetried hooks. The parent
//     queue.Worker only exposes an OnFailed callback, so the remaining
//     events are wired by the caller at dispatch / handler boundaries
//     rather than discovered through an interface the Worker does not
//     implement.
//   - FailedJobs — a pluggable store of dead-lettered jobs the dashboard
//     can list, retry, and forget. An in-memory reference implementation
//     ships here (MemoryFailedJobs); a sqlqueue-backed store can be
//     supplied by satisfying the same interface.
//
// Wiring sketch:
//
//	st := dashboard.NewStats()
//	failed := dashboard.NewMemoryFailedJobs(func(ctx context.Context, j queue.Job) error {
//	    return q.Push(ctx, j) // re-enqueue on retry
//	})
//
//	worker.OnFailed(func(j queue.Job, err error) {
//	    st.OnFailed("default")
//	    failed.Record(j, "default", err)
//	})
//
//	mux.Handle("/queue/", http.StripPrefix("/queue",
//	    dashboard.New(st, failed).Handler()))
//
// Everything is stdlib only (net/http, html/template, sync, time,
// encoding/json).
package dashboard

import (
	"sort"
	"sync"
	"time"
)

// windowSize is the rolling throughput window. Throughput is reported as
// jobs/sec averaged over this many trailing one-second buckets.
const windowSize = 60

// nowFunc is indirected so tests can drive the throughput window
// deterministically instead of sleeping.
var nowFunc = time.Now

// QueueStat is an immutable snapshot of one logical queue's counters,
// produced by Stats.Snapshot. Throughput is jobs processed per second
// averaged over the rolling window.
type QueueStat struct {
	Queue      string  `json:"queue"`
	Pushed     uint64  `json:"pushed"`
	Processed  uint64  `json:"processed"`
	Failed     uint64  `json:"failed"`
	Retried    uint64  `json:"retried"`
	Pending    int64   `json:"pending"`    // Pushed-Processed-Failed, floored at 0
	Throughput float64 `json:"throughput"` // processed jobs/sec over the window
}

// Stats is a concurrency-safe collector of per-queue counters plus a
// rolling throughput window. The zero value is not usable; call NewStats.
//
// A "queue" here is any caller-chosen label (queue.Job carries no queue
// name of its own), letting a single Stats track several logical queues
// fed by distinct workers.
type Stats struct {
	mu     sync.Mutex
	queues map[string]*queueCounters
}

type queueCounters struct {
	pushed    uint64
	processed uint64
	failed    uint64
	retried   uint64

	// buckets is a ring of per-second processed counts indexed by
	// unix-second % windowSize. epoch records which absolute second each
	// slot currently holds so stale slots (older than the window) read as
	// zero instead of leaking last minute's count.
	buckets [windowSize]uint64
	epoch   [windowSize]int64
}

// NewStats returns an empty, ready-to-use collector.
func NewStats() *Stats {
	return &Stats{queues: map[string]*queueCounters{}}
}

func (s *Stats) counters(queue string) *queueCounters {
	c, ok := s.queues[queue]
	if !ok {
		c = &queueCounters{}
		s.queues[queue] = c
	}
	return c
}

// OnPushed records that one job was enqueued onto queue. Call it from the
// dispatch path (e.g. wrapping queue.Dispatch).
func (s *Stats) OnPushed(queue string) {
	s.mu.Lock()
	s.counters(queue).pushed++
	s.mu.Unlock()
}

// OnProcessed records one successfully handled job and advances the
// throughput window. Call it from the worker after a handler returns nil.
func (s *Stats) OnProcessed(queue string) {
	sec := nowFunc().Unix()
	s.mu.Lock()
	c := s.counters(queue)
	c.processed++
	slot := sec % windowSize
	if c.epoch[slot] != sec {
		c.epoch[slot] = sec
		c.buckets[slot] = 0
	}
	c.buckets[slot]++
	s.mu.Unlock()
}

// OnFailed records one job that exhausted its retries and was dead-
// lettered. Wire it from queue.Worker.OnFailed.
func (s *Stats) OnFailed(queue string) {
	s.mu.Lock()
	s.counters(queue).failed++
	s.mu.Unlock()
}

// OnRetried records one job that failed an attempt and was re-queued
// (i.e. a Nack with attempts remaining), or one re-enqueued from the
// failed list. It is informational and does not feed throughput.
func (s *Stats) OnRetried(queue string) {
	s.mu.Lock()
	s.counters(queue).retried++
	s.mu.Unlock()
}

// throughputLocked sums the window buckets that fall within the trailing
// windowSize seconds and averages them. Caller holds s.mu.
func (c *queueCounters) throughputLocked(now int64) float64 {
	cutoff := now - windowSize + 1
	var sum uint64
	for i := 0; i < windowSize; i++ {
		if c.epoch[i] >= cutoff && c.epoch[i] <= now {
			sum += c.buckets[i]
		}
	}
	return float64(sum) / float64(windowSize)
}

// Snapshot returns a stable, sorted-by-queue copy of every counter set.
// Safe to call concurrently with the On* hooks.
func (s *Stats) Snapshot() []QueueStat {
	now := nowFunc().Unix()
	s.mu.Lock()
	out := make([]QueueStat, 0, len(s.queues))
	for name, c := range s.queues {
		pending := int64(c.pushed) - int64(c.processed) - int64(c.failed)
		if pending < 0 {
			pending = 0
		}
		out = append(out, QueueStat{
			Queue:      name,
			Pushed:     c.pushed,
			Processed:  c.processed,
			Failed:     c.failed,
			Retried:    c.retried,
			Pending:    pending,
			Throughput: c.throughputLocked(now),
		})
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Queue < out[j].Queue })
	return out
}

// Totals collapses every queue into a single aggregate row (Queue == "").
// Throughput is the sum of per-queue throughputs.
func (s *Stats) Totals() QueueStat {
	var t QueueStat
	for _, q := range s.Snapshot() {
		t.Pushed += q.Pushed
		t.Processed += q.Processed
		t.Failed += q.Failed
		t.Retried += q.Retried
		t.Pending += q.Pending
		t.Throughput += q.Throughput
	}
	return t
}
