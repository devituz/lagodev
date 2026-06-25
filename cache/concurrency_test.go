package cache

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- TTL correctness -------------------------------------------------------

func TestTTL_ExpiresAndForeverPersists(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()

	// A key with a short TTL must read as a miss once expired.
	_ = c.Put(ctx, "ttl", []byte("v"), 15*time.Millisecond)
	if v, ok, _ := c.Get(ctx, "ttl"); !ok || string(v) != "v" {
		t.Fatalf("ttl key must be live immediately after Put; got (%q, %v)", v, ok)
	}
	time.Sleep(30 * time.Millisecond)
	if v, ok, err := c.Get(ctx, "ttl"); ok || v != nil || err != nil {
		t.Fatalf("ttl key must miss after expiry; got (%q, %v, %v)", v, ok, err)
	}

	// A Forever key must never expire (well past any reasonable TTL).
	_ = c.Forever(ctx, "forever", []byte("v"))
	c.purgeExpired(time.Now().Add(1000 * time.Hour))
	if v, ok, _ := c.Get(ctx, "forever"); !ok || string(v) != "v" {
		t.Fatalf("Forever key must never expire; got (%q, %v)", v, ok)
	}
}

// TestTTL_ExpiredKeysAreGarbageCollected proves the map does not grow
// unbounded: many short-TTL write-only keys are evicted by the sweeper's
// purge, leaving only the live (Forever) keys behind.
func TestTTL_ExpiredKeysAreGarbageCollected(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()

	const ephemeral = 5000
	for i := 0; i < ephemeral; i++ {
		// Write-only short-TTL keys: never read back, so lazy expiry never
		// touches them — only the sweeper can reclaim them.
		_ = c.Put(ctx, "tmp:"+strconv.Itoa(i), []byte("x"), 10*time.Millisecond)
	}
	const liveKeys = 10
	for i := 0; i < liveKeys; i++ {
		_ = c.Forever(ctx, "live:"+strconv.Itoa(i), []byte("x"))
	}

	if got := c.len(); got != ephemeral+liveKeys {
		t.Fatalf("pre-purge map len = %d; want %d", got, ephemeral+liveKeys)
	}

	// Advance past the TTL and force the sweep deterministically (instead of
	// waiting on the background ticker).
	c.purgeExpired(time.Now().Add(time.Second))

	if got := c.len(); got != liveKeys {
		t.Fatalf("post-purge map len = %d; want %d (expired keys must be GC'd)", got, liveKeys)
	}
}

// TestTTL_SweeperBoundsMemory exercises the real background sweeper with a
// short interval to confirm it actually shrinks the map without any access
// to the keys. It builds a *Memory with a fast sweep interval directly so
// no fields are mutated after the sweeper goroutine starts.
func TestTTL_SweeperBoundsMemory(t *testing.T) {
	ctx := context.Background()
	c := &Memory{
		items:    make(map[string]memoryItem),
		stop:     make(chan struct{}),
		sweepInt: 5 * time.Millisecond,
		flights:  make(map[string]*flight),
	}
	go c.sweep()
	defer c.Close()

	for i := 0; i < 1000; i++ {
		_ = c.Put(ctx, "e:"+strconv.Itoa(i), []byte("x"), 5*time.Millisecond)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.len() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sweeper did not bound memory; map len still %d", c.len())
}

// --- Atomic Increment / Decrement -----------------------------------------

func TestAtomic_IncrementNoLostUpdates(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()

	const (
		n = 50
		k = 2000
	)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < k; j++ {
				if _, err := c.Increment(ctx, "ctr", 1); err != nil {
					t.Errorf("Increment: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	v, ok, _ := c.Get(ctx, "ctr")
	if !ok {
		t.Fatal("counter missing")
	}
	got, _ := strconv.ParseInt(string(v), 10, 64)
	if want := int64(n * k); got != want {
		t.Fatalf("final counter = %d; want %d (lost updates)", got, want)
	}
}

func TestAtomic_IncrementDecrementMixedNetsZero(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()

	const (
		n = 40
		k = 1000
	)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < k; j++ {
				_, _ = c.Increment(ctx, "net", 1)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < k; j++ {
				_, _ = c.Decrement(ctx, "net", 1)
			}
		}()
	}
	wg.Wait()

	v, _, _ := c.Get(ctx, "net")
	got, _ := strconv.ParseInt(string(v), 10, 64)
	if got != 0 {
		t.Fatalf("inc/dec must net to 0; got %d", got)
	}
}

// --- Add: exactly one winner ----------------------------------------------

func TestAdd_ExactlyOneWinnerUnderRace(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()

	const n = 256
	var (
		wg    sync.WaitGroup
		wins  atomic.Int64
		start = make(chan struct{})
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			if ok, _ := c.Add(ctx, "lock", []byte(strconv.Itoa(id)), time.Minute); ok {
				wins.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if got := wins.Load(); got != 1 {
		t.Fatalf("Add winners = %d; want exactly 1", got)
	}
	// The stored value must belong to some single winner and be readable.
	if v, ok, _ := c.Get(ctx, "lock"); !ok || v == nil {
		t.Fatalf("winner's value must be stored; got (%q, %v)", v, ok)
	}
}

// --- Remember: single-flight producer dedup -------------------------------

func TestRemember_ConcurrentMissRunsProducerOnceAndCaches(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()

	const n = 200
	var (
		calls   atomic.Int64
		wg      sync.WaitGroup
		start   = make(chan struct{})
		release = make(chan struct{})
	)
	fn := func() ([]byte, error) {
		calls.Add(1)
		<-release // hold the producer so all goroutines pile up on the miss
		return []byte("loaded"), nil
	}

	results := make([][]byte, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			v, err := Remember(ctx, c, "key", time.Minute, fn)
			if err != nil {
				t.Errorf("Remember: %v", err)
				return
			}
			results[idx] = v
		}(i)
	}
	close(start)
	// Give goroutines a moment to register as waiters, then release the
	// single producer.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("producer ran %d times; single-flight must collapse to 1", got)
	}
	for i, v := range results {
		if string(v) != "loaded" {
			t.Fatalf("result[%d] = %q; want loaded", i, v)
		}
	}
	// Value must be cached now: a fresh Remember must not call fn again.
	calls.Store(0)
	v, err := Remember(ctx, c, "key", time.Minute, func() ([]byte, error) {
		calls.Add(1)
		return []byte("should-not-run"), nil
	})
	if err != nil || string(v) != "loaded" || calls.Load() != 0 {
		t.Fatalf("post-fill Remember = (%q, %v, calls=%d); want cached", v, err, calls.Load())
	}
}

func TestRemember_ProducerErrorNotCachedAndRetried(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()

	boom := errors.New("load failed")
	var calls atomic.Int64
	_, err := Remember(ctx, c, "k", time.Minute, func() ([]byte, error) {
		calls.Add(1)
		return nil, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v; want boom", err)
	}
	if ok, _ := c.Has(ctx, "k"); ok {
		t.Fatal("failed producer must not cache")
	}
	// A subsequent Remember must retry the producer (no negative caching).
	v, err := Remember(ctx, c, "k", time.Minute, func() ([]byte, error) {
		calls.Add(1)
		return []byte("ok"), nil
	})
	if err != nil || string(v) != "ok" {
		t.Fatalf("retry = (%q, %v); want ok", v, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("producer calls = %d; want 2 (error not cached, retried)", calls.Load())
	}
}

// Mutating a value returned by Remember must not corrupt the cached entry
// or another caller's shared result.
func TestRemember_ReturnsCopies(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	v, err := Remember(ctx, c, "k", time.Minute, func() ([]byte, error) {
		return []byte("hello"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	v[0] = 'X'
	if got, _, _ := c.Get(ctx, "k"); string(got) != "hello" {
		t.Fatalf("Remember must return a copy; cached became %q", got)
	}
}

// --- Many / PutMany consistency -------------------------------------------

func TestManyPutMany_ConsistentUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()

	keys := []string{"a", "b", "c", "d", "e"}
	// Writers repeatedly PutMany a coherent batch (all keys share the same
	// generation tag); readers Many them back. A read must never observe a
	// torn batch because PutMany writes in one critical section — though a
	// reader may legitimately see different generations across separate
	// Many calls, within one Many call every present key is from a single
	// consistent snapshot.
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		gen := 0
		for {
			select {
			case <-stop:
				return
			default:
				gen++
				batch := make(map[string][]byte, len(keys))
				tag := []byte(strconv.Itoa(gen))
				for _, k := range keys {
					batch[k] = tag
				}
				_ = c.PutMany(ctx, batch, time.Minute)
			}
		}
	}()

	// Seed so readers find a full batch.
	seed := make(map[string][]byte, len(keys))
	for _, k := range keys {
		seed[k] = []byte("0")
	}
	_ = c.PutMany(ctx, seed, time.Minute)

	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				got, err := c.Many(ctx, keys...)
				if err != nil {
					t.Errorf("Many: %v", err)
					return
				}
				// Every present key in one snapshot must carry the same
				// generation tag (no torn writes across the batch).
				var want string
				for _, k := range keys {
					v, ok := got[k]
					if !ok {
						continue
					}
					if want == "" {
						want = string(v)
					} else if string(v) != want {
						t.Errorf("torn batch: key %q=%q, expected %q", k, v, want)
						return
					}
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
