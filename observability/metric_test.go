package observability

import (
	"math"
	"sync"
	"testing"
)

func TestCounter_IncAddValue(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("requests_total")

	if got := c.Value(); got != 0 {
		t.Fatalf("new counter Value = %v, want 0", got)
	}
	c.Inc()
	c.Add(4)
	if got := c.Value(); got != 5 {
		t.Fatalf("counter Value = %v, want 5", got)
	}
}

func TestCounter_Monotonic(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("c")
	c.Add(3)
	c.Add(-10) // must be ignored: counters are monotonic
	if got := c.Value(); got != 3 {
		t.Fatalf("counter Value after negative Add = %v, want 3", got)
	}
}

func TestCounter_Dedup(t *testing.T) {
	r := NewRegistry()
	c1 := r.Counter("hits", "code", "200")
	c2 := r.Counter("hits", "code", "200")
	if c1 != c2 {
		t.Fatal("same name+labels must return the same Counter instance")
	}
	c1.Inc()
	if c2.Value() != 1 {
		t.Fatalf("deduped counter Value = %v, want 1", c2.Value())
	}

	// Different label value -> distinct series.
	c3 := r.Counter("hits", "code", "500")
	if c1 == c3 {
		t.Fatal("different label value must yield a distinct Counter")
	}
	if c3.Value() != 0 {
		t.Fatalf("distinct series Value = %v, want 0", c3.Value())
	}
}

func TestCounter_LabelOrderIndependent(t *testing.T) {
	r := NewRegistry()
	a := r.Counter("x", "method", "GET", "code", "200")
	b := r.Counter("x", "code", "200", "method", "GET")
	if a != b {
		t.Fatal("label pairs are order-independent; must dedup to same series")
	}
}

func TestGauge_SetAddIncDec(t *testing.T) {
	r := NewRegistry()
	g := r.Gauge("inflight")

	g.Set(10)
	if got := g.Value(); got != 10 {
		t.Fatalf("after Set Value = %v, want 10", got)
	}
	g.Inc()
	g.Inc()
	g.Dec()
	if got := g.Value(); got != 11 {
		t.Fatalf("after Inc/Inc/Dec Value = %v, want 11", got)
	}
	g.Add(-5)
	if got := g.Value(); got != 6 {
		t.Fatalf("after Add(-5) Value = %v, want 6", got)
	}
}

func TestGauge_Dedup(t *testing.T) {
	r := NewRegistry()
	g1 := r.Gauge("temp", "zone", "a")
	g2 := r.Gauge("temp", "zone", "a")
	if g1 != g2 {
		t.Fatal("same name+labels must return the same Gauge instance")
	}
}

func TestHistogram_ObserveSnapshot(t *testing.T) {
	r := NewRegistry()
	buckets := []float64{0.1, 0.5, 1}
	h := r.Histogram("latency", buckets)

	// Observations: 0.05, 0.3, 0.7, 5
	h.Observe(0.05)
	h.Observe(0.3)
	h.Observe(0.7)
	h.Observe(5)

	snap := h.Snapshot()
	if snap.Count != 4 {
		t.Fatalf("Count = %d, want 4", snap.Count)
	}
	if math.Abs(snap.Sum-(0.05+0.3+0.7+5)) > 1e-9 {
		t.Fatalf("Sum = %v, want 6.05", snap.Sum)
	}
	// Cumulative counts: le 0.1 -> {0.05} = 1; le 0.5 -> {0.05,0.3} = 2; le 1 -> {0.05,0.3,0.7} = 3.
	want := []uint64{1, 2, 3}
	if len(snap.Counts) != len(want) {
		t.Fatalf("Counts len = %d, want %d", len(snap.Counts), len(want))
	}
	for i := range want {
		if snap.Counts[i] != want[i] {
			t.Fatalf("Counts[%d] (le %v) = %d, want %d", i, snap.Buckets[i], snap.Counts[i], want[i])
		}
	}
}

func TestHistogram_DefaultBuckets(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("d", nil)
	snap := h.Snapshot()
	if len(snap.Buckets) != len(DefaultBuckets) {
		t.Fatalf("nil buckets must fall back to DefaultBuckets: got %d buckets, want %d",
			len(snap.Buckets), len(DefaultBuckets))
	}
	for i, b := range DefaultBuckets {
		if snap.Buckets[i] != b {
			t.Fatalf("Buckets[%d] = %v, want %v", i, snap.Buckets[i], b)
		}
	}
}

func TestHistogram_BucketsSortedAndCopied(t *testing.T) {
	r := NewRegistry()
	src := []float64{1, 0.1, 0.5}
	h := r.Histogram("sorted", src)

	// Mutating the source after construction must not affect the histogram.
	src[0] = 999

	snap := h.Snapshot()
	wantBuckets := []float64{0.1, 0.5, 1}
	for i, b := range wantBuckets {
		if snap.Buckets[i] != b {
			t.Fatalf("Buckets[%d] = %v, want %v (sorted+defensive copy)", i, snap.Buckets[i], b)
		}
	}

	// Snapshot must be a copy: mutating it must not change later snapshots.
	snap.Buckets[0] = -1
	snap.Counts[0] = 123
	snap2 := h.Snapshot()
	if snap2.Buckets[0] != 0.1 {
		t.Fatal("Snapshot must return a defensive copy of Buckets")
	}
	if snap2.Counts[0] != 0 {
		t.Fatal("Snapshot must return a defensive copy of Counts")
	}
}

func TestHistogram_Dedup(t *testing.T) {
	r := NewRegistry()
	h1 := r.Histogram("h", nil, "route", "/a")
	h2 := r.Histogram("h", nil, "route", "/a")
	if h1 != h2 {
		t.Fatal("same name+labels must return the same Histogram instance")
	}
}

func TestLabelSet_OddTrailingDropped(t *testing.T) {
	// An odd trailing element must be dropped: "k","v","dangling" == "k","v".
	r := NewRegistry()
	a := r.Counter("z", "k", "v", "dangling")
	b := r.Counter("z", "k", "v")
	if a != b {
		t.Fatal("odd trailing label element must be dropped")
	}
}

// TestConcurrentRecording is meaningful under -race: many goroutines hit the
// same instruments and registry concurrently.
func TestConcurrentRecording(t *testing.T) {
	r := NewRegistry()
	const (
		goroutines = 50
		perG       = 1000
	)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			// Re-resolve instruments inside each goroutine to also exercise
			// concurrent family/series creation and dedup.
			c := r.Counter("c_total", "shard", "x")
			g := r.Gauge("g_inflight", "shard", "x")
			h := r.Histogram("h_lat", []float64{0.5, 1, 2}, "shard", "x")
			for j := 0; j < perG; j++ {
				c.Inc()
				g.Inc()
				g.Dec()
				h.Observe(0.25)
			}
		}()
	}
	wg.Wait()

	if got := r.Counter("c_total", "shard", "x").Value(); got != goroutines*perG {
		t.Fatalf("concurrent counter Value = %v, want %v", got, goroutines*perG)
	}
	if got := r.Gauge("g_inflight", "shard", "x").Value(); got != 0 {
		t.Fatalf("concurrent gauge Value = %v, want 0 (balanced Inc/Dec)", got)
	}
	snap := r.Histogram("h_lat", []float64{0.5, 1, 2}, "shard", "x").Snapshot()
	if snap.Count != goroutines*perG {
		t.Fatalf("concurrent histogram Count = %d, want %d", snap.Count, goroutines*perG)
	}
}

// TestConcurrentDistinctFamilies stresses Registry.family creation across many
// distinct names from many goroutines (RWMutex + double-checked locking path).
func TestConcurrentDistinctFamilies(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	wg.Add(20)
	for i := 0; i < 20; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.Counter("shared_name").Inc()
			}
		}()
	}
	wg.Wait()
	if got := r.Counter("shared_name").Value(); got != 20*200 {
		t.Fatalf("shared family counter = %v, want %v", got, 20*200)
	}
}
