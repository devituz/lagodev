package observability

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

// Meter creates and stores instruments. Implementations must be safe for
// concurrent use. Instruments with the same name and label set are
// deduplicated so repeated lookups return the same object.
type Meter interface {
	Counter(name string, labels ...string) Counter
	Gauge(name string, labels ...string) Gauge
	// Histogram creates a histogram. buckets are upper bounds in seconds (or
	// any unit); pass nil for DefaultBuckets.
	Histogram(name string, buckets []float64, labels ...string) Histogram
}

// Counter is a monotonically increasing value.
type Counter interface {
	Inc()
	Add(v float64)
	Value() float64
}

// Gauge is a value that can go up and down.
type Gauge interface {
	Set(v float64)
	Add(v float64)
	Inc()
	Dec()
	Value() float64
}

// Histogram records a distribution of observations into fixed buckets.
type Histogram interface {
	Observe(v float64)
	// Snapshot returns cumulative bucket counts (le bound -> count), total
	// count and sum.
	Snapshot() HistogramSnapshot
}

// HistogramSnapshot is an immutable view of a histogram's state.
type HistogramSnapshot struct {
	Buckets []float64 // upper bounds
	Counts  []uint64  // cumulative counts aligned with Buckets
	Count   uint64    // total observations
	Sum     float64
}

// DefaultBuckets are latency-oriented bucket bounds in seconds.
var DefaultBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// metricKind tags a registered metric family.
type metricKind int

const (
	kindCounter metricKind = iota
	kindGauge
	kindHistogram
)

// Registry is the default in-memory Meter. MetricsHandler renders it in the
// Prometheus text exposition format (see metrics_handler.go).
type Registry struct {
	mu      sync.RWMutex
	metrics map[string]*metricFamily
	order   []string // insertion order of family ids for stable output
}

type metricFamily struct {
	kind    metricKind
	name    string
	buckets []float64 // histogram only
	mu      sync.Mutex
	series  map[string]*series // keyed by label-value signature
	keys    []string           // series insertion order
}

type series struct {
	labels labelSet
	c      *counter
	g      *gauge
	h      *histogram
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{metrics: make(map[string]*metricFamily)}
}

func (r *Registry) family(kind metricKind, name string, buckets []float64) *metricFamily {
	r.mu.RLock()
	f := r.metrics[name]
	r.mu.RUnlock()
	if f != nil {
		return f
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if f = r.metrics[name]; f != nil {
		return f
	}
	f = &metricFamily{
		kind:    kind,
		name:    name,
		buckets: buckets,
		series:  make(map[string]*series),
	}
	r.metrics[name] = f
	r.order = append(r.order, name)
	return f
}

func (r *Registry) Counter(name string, labels ...string) Counter {
	f := r.family(kindCounter, name, nil)
	ls := newLabelSet(labels)
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.series[ls.sig]; s != nil {
		return s.c
	}
	s := &series{labels: ls, c: &counter{}}
	f.series[ls.sig] = s
	f.keys = append(f.keys, ls.sig)
	return s.c
}

func (r *Registry) Gauge(name string, labels ...string) Gauge {
	f := r.family(kindGauge, name, nil)
	ls := newLabelSet(labels)
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.series[ls.sig]; s != nil {
		return s.g
	}
	s := &series{labels: ls, g: &gauge{}}
	f.series[ls.sig] = s
	f.keys = append(f.keys, ls.sig)
	return s.g
}

func (r *Registry) Histogram(name string, buckets []float64, labels ...string) Histogram {
	if buckets == nil {
		buckets = DefaultBuckets
	}
	f := r.family(kindHistogram, name, buckets)
	ls := newLabelSet(labels)
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.series[ls.sig]; s != nil {
		return s.h
	}
	s := &series{labels: ls, h: newHistogram(buckets)}
	f.series[ls.sig] = s
	f.keys = append(f.keys, ls.sig)
	return s.h
}

// --- counter ---

type counter struct {
	bits uint64 // float64 bits
}

func (c *counter) Inc() { c.Add(1) }

func (c *counter) Add(v float64) {
	if v < 0 {
		return // counters are monotonic
	}
	for {
		old := atomic.LoadUint64(&c.bits)
		nw := math.Float64bits(math.Float64frombits(old) + v)
		if atomic.CompareAndSwapUint64(&c.bits, old, nw) {
			return
		}
	}
}

func (c *counter) Value() float64 { return math.Float64frombits(atomic.LoadUint64(&c.bits)) }

// --- gauge ---

type gauge struct {
	bits uint64
}

func (g *gauge) Set(v float64) { atomic.StoreUint64(&g.bits, math.Float64bits(v)) }

func (g *gauge) Add(v float64) {
	for {
		old := atomic.LoadUint64(&g.bits)
		nw := math.Float64bits(math.Float64frombits(old) + v)
		if atomic.CompareAndSwapUint64(&g.bits, old, nw) {
			return
		}
	}
}

func (g *gauge) Inc() { g.Add(1) }
func (g *gauge) Dec() { g.Add(-1) }

func (g *gauge) Value() float64 { return math.Float64frombits(atomic.LoadUint64(&g.bits)) }

// --- histogram ---

type histogram struct {
	buckets []float64
	mu      sync.Mutex
	counts  []uint64
	count   uint64
	sum     float64
}

func newHistogram(buckets []float64) *histogram {
	// Defensive copy; ensure sorted ascending.
	b := make([]float64, len(buckets))
	copy(b, buckets)
	sort.Float64s(b)
	return &histogram{buckets: b, counts: make([]uint64, len(b))}
}

func (h *histogram) Observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	h.sum += v
	for i, ub := range h.buckets {
		if v <= ub {
			h.counts[i]++
		}
	}
}

func (h *histogram) Snapshot() HistogramSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := make([]float64, len(h.buckets))
	copy(b, h.buckets)
	c := make([]uint64, len(h.counts))
	copy(c, h.counts)
	return HistogramSnapshot{Buckets: b, Counts: c, Count: h.count, Sum: h.sum}
}

// --- label set ---

type labelSet struct {
	names  []string
	values []string
	sig    string // canonical signature for map keying
}

// newLabelSet accepts a flat name,value,name,value... slice. An odd trailing
// element is dropped. Pairs are sorted by name for a stable signature.
func newLabelSet(flat []string) labelSet {
	n := len(flat) / 2
	type kv struct{ k, v string }
	pairs := make([]kv, 0, n)
	for i := 0; i+1 < len(flat); i += 2 {
		pairs = append(pairs, kv{flat[i], flat[i+1]})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })

	ls := labelSet{
		names:  make([]string, len(pairs)),
		values: make([]string, len(pairs)),
	}
	var sb []byte
	for i, p := range pairs {
		ls.names[i] = p.k
		ls.values[i] = p.v
		sb = append(sb, p.k...)
		sb = append(sb, '=')
		sb = append(sb, p.v...)
		sb = append(sb, ',')
	}
	ls.sig = string(sb)
	return ls
}
