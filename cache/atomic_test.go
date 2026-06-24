package cache

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestMemory_Add(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()

	ok, err := c.Add(ctx, "k", []byte("first"), 0)
	if err != nil || !ok {
		t.Fatalf("first Add = (%v, %v); want (true, nil)", ok, err)
	}
	ok, err = c.Add(ctx, "k", []byte("second"), 0)
	if err != nil || ok {
		t.Fatalf("second Add = (%v, %v); want (false, nil)", ok, err)
	}
	// Value must be the first writer's, untouched.
	if v, _, _ := c.Get(ctx, "k"); string(v) != "first" {
		t.Fatalf("Add must not overwrite; got %q", v)
	}
}

func TestMemory_AddAfterExpiry(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	_ = c.Put(ctx, "k", []byte("old"), 20*time.Millisecond)
	time.Sleep(40 * time.Millisecond)
	ok, err := c.Add(ctx, "k", []byte("new"), 0)
	if err != nil || !ok {
		t.Fatalf("Add over expired key = (%v, %v); want (true, nil)", ok, err)
	}
	if v, _, _ := c.Get(ctx, "k"); string(v) != "new" {
		t.Fatalf("got %q; want new", v)
	}
}

func TestMemory_AddSingleWinner(t *testing.T) {
	// Exactly one of N concurrent Adds for the same key may win. -race
	// must stay clean.
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	const n = 128

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if ok, _ := c.Add(ctx, "lock", []byte("v"), 0); ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins != 1 {
		t.Fatalf("Add winners = %d; want 1", wins)
	}
}

func TestMemory_IncrementDecrement(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()

	n, err := c.Increment(ctx, "ctr", 1)
	if err != nil || n != 1 {
		t.Fatalf("Increment missing = (%d, %v); want (1, nil)", n, err)
	}
	n, _ = c.Increment(ctx, "ctr", 4)
	if n != 5 {
		t.Fatalf("Increment = %d; want 5", n)
	}
	n, _ = c.Decrement(ctx, "ctr", 2)
	if n != 3 {
		t.Fatalf("Decrement = %d; want 3", n)
	}
	// Get must read the counter back consistently.
	if v, ok, _ := c.Get(ctx, "ctr"); !ok || string(v) != "3" {
		t.Fatalf("Get ctr = (%q, %v); want (3, true)", v, ok)
	}
}

func TestMemory_IncrementNonInteger(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	_ = c.Put(ctx, "k", []byte("not-a-number"), 0)
	if _, err := c.Increment(ctx, "k", 1); err == nil {
		t.Fatal("Increment over non-integer must error")
	}
	// Original value must be untouched.
	if v, _, _ := c.Get(ctx, "k"); string(v) != "not-a-number" {
		t.Fatalf("value changed to %q", v)
	}
}

func TestMemory_IncrementConcurrentExact(t *testing.T) {
	// Hammer Increment from many goroutines; the final count must be
	// exact. -race must stay clean.
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	const (
		goroutines = 64
		perG       = 1000
	)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				if _, err := c.Increment(ctx, "ctr", 1); err != nil {
					t.Errorf("Increment: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	want := int64(goroutines * perG)
	v, ok, _ := c.Get(ctx, "ctr")
	if !ok {
		t.Fatal("counter missing")
	}
	got, _ := strconv.ParseInt(string(v), 10, 64)
	if got != want {
		t.Fatalf("final count = %d; want %d", got, want)
	}
}

func TestMemory_Forever(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	if err := c.Forever(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.Has(ctx, "k"); !ok {
		t.Fatal("Forever must store the key")
	}
}

func TestMemory_ManyPutMany(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	in := map[string][]byte{"a": []byte("1"), "b": []byte("2"), "c": []byte("3")}
	if err := c.PutMany(ctx, in, 0); err != nil {
		t.Fatal(err)
	}
	got, err := c.Many(ctx, "a", "b", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["a"]) != "1" || string(got["b"]) != "2" {
		t.Fatalf("Many = %v; want {a:1, b:2}", got)
	}
	if _, present := got["missing"]; present {
		t.Fatal("Many must omit missing keys")
	}
}

func TestMemory_PutManyNegativeTTLDeletes(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	_ = c.Put(ctx, "a", []byte("keep"), 0)
	if err := c.PutMany(ctx, map[string][]byte{"a": []byte("gone")}, -time.Second); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.Has(ctx, "a"); ok {
		t.Fatal("negative-TTL PutMany must delete keys")
	}
}

func TestMemory_ManyReturnsCopies(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	_ = c.Put(ctx, "k", []byte("hello"), 0)
	got, _ := c.Many(ctx, "k")
	got["k"][0] = 'X'
	if v, _, _ := c.Get(ctx, "k"); string(v) != "hello" {
		t.Fatalf("Many must return copies; stored value became %q", v)
	}
}

// degradeStore implements only the base Store interface (no optional
// extensions) to exercise the free-helper fallback paths.
type degradeStore struct{ m *Memory }

func newDegradeStore() *degradeStore { return &degradeStore{m: NewMemory()} }

func (d *degradeStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return d.m.Get(ctx, key)
}
func (d *degradeStore) Put(ctx context.Context, key string, v []byte, ttl time.Duration) error {
	return d.m.Put(ctx, key, v, ttl)
}
func (d *degradeStore) Forget(ctx context.Context, key string) error { return d.m.Forget(ctx, key) }
func (d *degradeStore) Flush(ctx context.Context) error              { return d.m.Flush(ctx) }
func (d *degradeStore) Has(ctx context.Context, key string) (bool, error) {
	return d.m.Has(ctx, key)
}

func TestFreeHelpers_OnMemory(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()

	if ok, err := Add(ctx, c, "k", []byte("v"), 0); err != nil || !ok {
		t.Fatalf("Add = (%v, %v)", ok, err)
	}
	if ok, _ := Add(ctx, c, "k", []byte("v2"), 0); ok {
		t.Fatal("second Add must report false")
	}
	if n, err := Increment(ctx, c, "ctr", 3); err != nil || n != 3 {
		t.Fatalf("Increment = (%d, %v)", n, err)
	}
	if n, err := Decrement(ctx, c, "ctr", 1); err != nil || n != 2 {
		t.Fatalf("Decrement = (%d, %v)", n, err)
	}
	if err := PutMany(ctx, c, map[string][]byte{"a": []byte("1")}, 0); err != nil {
		t.Fatal(err)
	}
	if got, err := Many(ctx, c, "a", "nope"); err != nil || len(got) != 1 {
		t.Fatalf("Many = (%v, %v)", got, err)
	}
}

func TestFreeHelpers_Degraded(t *testing.T) {
	ctx := context.Background()
	d := newDegradeStore()
	defer d.m.Close()

	// Add falls back to Has+Put.
	if ok, err := Add(ctx, d, "k", []byte("v"), 0); err != nil || !ok {
		t.Fatalf("degraded Add = (%v, %v); want (true, nil)", ok, err)
	}
	if ok, _ := Add(ctx, d, "k", []byte("v2"), 0); ok {
		t.Fatal("degraded second Add must report false")
	}

	// Increment/Decrement are unsupported on a plain store.
	if _, err := Increment(ctx, d, "ctr", 1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("degraded Increment err = %v; want ErrUnsupported", err)
	}
	if _, err := Decrement(ctx, d, "ctr", 1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("degraded Decrement err = %v; want ErrUnsupported", err)
	}

	// Many/PutMany fall back to per-key loops.
	if err := PutMany(ctx, d, map[string][]byte{"a": []byte("1"), "b": []byte("2")}, 0); err != nil {
		t.Fatal(err)
	}
	got, err := Many(ctx, d, "a", "b", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["a"]) != "1" {
		t.Fatalf("degraded Many = %v; want {a:1,b:2}", got)
	}
}
