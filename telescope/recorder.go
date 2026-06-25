package telescope

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync"
	"time"
)

// DefaultCapacity is the number of entries retained when Options.Capacity is
// not set.
const DefaultCapacity = 500

// maxNPlusRequests caps how many in-flight request ids the N+1 accumulator
// tracks at once. The per-request bookkeeping is normally evicted by
// RecordRequest when the request completes (see RecordRequest), but a caller
// that records queries under a request id that never terminates with a
// matching RecordRequest (e.g. a background context that reuses
// ContextWithRequestID, or an aborted request) would otherwise grow the map
// without bound. When the ceiling is reached the oldest tracked id is dropped,
// keeping the map bounded regardless of caller behaviour.
const maxNPlusRequests = 4096

// Options configures a Recorder.
type Options struct {
	// Capacity is the maximum number of entries kept in the ring buffer.
	// Older entries are evicted once it is full. Defaults to DefaultCapacity.
	Capacity int
	// Now overrides the clock; used by tests. Defaults to time.Now.
	Now func() time.Time
}

// Recorder collects entries into a bounded, concurrency-safe ring buffer and
// serves them to the dashboard. The zero value is not usable; build one with
// NewRecorder.
type Recorder struct {
	now func() time.Time

	mu   sync.Mutex
	buf  []Entry
	next int    // index of the next write
	full bool   // whether the buffer has wrapped at least once
	seq  uint64 // monotonic counter feeding generated ids

	// nplus tracks, per request id, how many times each normalised SQL
	// statement has been seen so the N+1 heuristic can flag repeats.
	nplus map[string]map[string]int
	// nplusOrder records the request ids in nplus in insertion order so the
	// oldest can be evicted once maxNPlusRequests is exceeded. It stays in
	// lock-step with nplus: an id is appended when first inserted and removed
	// when the id is dropped (by RecordRequest or by eviction).
	nplusOrder []string
}

// NewRecorder returns a ready Recorder.
func NewRecorder(opts Options) *Recorder {
	capacity := opts.Capacity
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Recorder{
		now:   now,
		buf:   make([]Entry, capacity),
		nplus: make(map[string]map[string]int),
	}
}

// Capacity reports the ring-buffer size.
func (r *Recorder) Capacity() int { return len(r.buf) }

// Record stores e, filling in a generated ID and the current time when they
// are missing. It is safe for concurrent use. The stored entry's defaulted
// values are returned.
func (r *Recorder) Record(e Entry) Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recordLocked(e)
}

// recordLocked assumes r.mu is held.
func (r *Recorder) recordLocked(e Entry) Entry {
	if e.Time.IsZero() {
		e.Time = r.now()
	}
	if e.ID == "" {
		e.ID = r.newID()
	}
	r.buf[r.next] = e
	r.next++
	if r.next == len(r.buf) {
		r.next = 0
		r.full = true
	}
	return e
}

// nplusFor returns the per-statement counter map for reqID, creating it (and
// recording its insertion order) on first use. When inserting a new id would
// exceed maxNPlusRequests, the oldest tracked id is evicted first so the map
// stays bounded even if callers never terminate their request ids with a
// RecordRequest. Assumes r.mu is held.
func (r *Recorder) nplusFor(reqID string) map[string]int {
	if seen := r.nplus[reqID]; seen != nil {
		return seen
	}
	if len(r.nplus) >= maxNPlusRequests && len(r.nplusOrder) > 0 {
		oldest := r.nplusOrder[0]
		r.nplusOrder = r.nplusOrder[1:]
		delete(r.nplus, oldest)
	}
	seen := make(map[string]int)
	r.nplus[reqID] = seen
	r.nplusOrder = append(r.nplusOrder, reqID)
	return seen
}

// dropNPlus removes reqID's accumulator and its order entry. Assumes r.mu held.
func (r *Recorder) dropNPlus(reqID string) {
	if _, ok := r.nplus[reqID]; !ok {
		return
	}
	delete(r.nplus, reqID)
	for i, id := range r.nplusOrder {
		if id == reqID {
			r.nplusOrder = append(r.nplusOrder[:i], r.nplusOrder[i+1:]...)
			break
		}
	}
}

// newID returns a process-unique id. It combines a random prefix with a
// monotonic counter so ids are both unguessable and ordered. Assumes r.mu held.
func (r *Recorder) newID() string {
	r.seq++
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:]) + "-" + strconv.FormatUint(r.seq, 36)
}

// Entries returns a snapshot of the stored entries, newest first.
func (r *Recorder) Entries() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

// snapshotLocked returns entries newest-first. Assumes r.mu is held.
func (r *Recorder) snapshotLocked() []Entry {
	var ordered []Entry
	if !r.full {
		ordered = make([]Entry, r.next)
		copy(ordered, r.buf[:r.next])
	} else {
		ordered = make([]Entry, 0, len(r.buf))
		ordered = append(ordered, r.buf[r.next:]...)
		ordered = append(ordered, r.buf[:r.next]...)
	}
	// Reverse in place to get newest-first.
	for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
		ordered[i], ordered[j] = ordered[j], ordered[i]
	}
	return ordered
}

// Filter selects stored entries newest-first. An empty typ matches all types;
// limit <= 0 means no limit.
func (r *Recorder) Filter(typ Type, limit int) []Entry {
	all := r.Entries()
	if typ == "" && limit <= 0 {
		return all
	}
	out := make([]Entry, 0, len(all))
	for _, e := range all {
		if typ != "" && e.Type != typ {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// Find returns the entry with the given id, if present.
func (r *Recorder) Find(id string) (Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.buf {
		if r.buf[i].ID == id {
			return r.buf[i], true
		}
	}
	return Entry{}, false
}

// Reset clears all stored entries and N+1 bookkeeping.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.buf {
		r.buf[i] = Entry{}
	}
	r.next = 0
	r.full = false
	r.nplus = make(map[string]map[string]int)
	r.nplusOrder = nil
}

// --- request id context plumbing ---

type requestIDKey struct{}

// ContextWithRequestID returns a copy of ctx carrying id so that child entries
// recorded during a request can be correlated with the Request entry.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext extracts the active telescope request id, if any.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey{}).(string)
	return id, ok
}
