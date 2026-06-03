package cache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestMemory_PutGet(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	if err := c.Put(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatal(err)
	}
	v, ok, err := c.Get(ctx, "k")
	if err != nil || !ok || string(v) != "v" {
		t.Fatalf("Get k = (%q, %v, %v); want (v, true, nil)", v, ok, err)
	}
}

func TestMemory_MissReturnsFalseNoError(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	v, ok, err := c.Get(ctx, "missing")
	if v != nil || ok || err != nil {
		t.Fatalf("miss = (%v, %v, %v); want (nil, false, nil)", v, ok, err)
	}
}

func TestMemory_TTLExpiry(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	_ = c.Put(ctx, "k", []byte("v"), 20*time.Millisecond)
	if ok, _ := c.Has(ctx, "k"); !ok {
		t.Fatal("must exist immediately after Put")
	}
	time.Sleep(40 * time.Millisecond)
	if ok, _ := c.Has(ctx, "k"); ok {
		t.Fatal("must be expired after TTL")
	}
}

func TestMemory_ForgetAndFlush(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	_ = c.Put(ctx, "a", []byte("1"), 0)
	_ = c.Put(ctx, "b", []byte("2"), 0)
	_ = c.Forget(ctx, "a")
	if ok, _ := c.Has(ctx, "a"); ok {
		t.Fatal("a must be gone")
	}
	if ok, _ := c.Has(ctx, "b"); !ok {
		t.Fatal("b must remain")
	}
	_ = c.Flush(ctx)
	if ok, _ := c.Has(ctx, "b"); ok {
		t.Fatal("flush must clear everything")
	}
}

func TestMemory_StoresCopyNotReference(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	val := []byte("hello")
	_ = c.Put(ctx, "k", val, 0)
	val[0] = 'X' // mutate after Put
	got, _, _ := c.Get(ctx, "k")
	if string(got) != "hello" {
		t.Fatalf("stored value must be a copy, got %q", got)
	}
}

func TestRemember_FillsThenCaches(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	calls := 0
	fn := func() ([]byte, error) {
		calls++
		return []byte("loaded"), nil
	}
	for i := 0; i < 5; i++ {
		v, err := Remember(ctx, c, "users", time.Minute, fn)
		if err != nil || string(v) != "loaded" {
			t.Fatalf("Remember[%d] = (%q, %v)", i, v, err)
		}
	}
	if calls != 1 {
		t.Fatalf("loader must run once, ran %d times", calls)
	}
}

func TestRemember_PropagatesError(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	boom := errors.New("loader failed")
	_, err := Remember(ctx, c, "k", time.Minute, func() ([]byte, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("must propagate loader err, got %v", err)
	}
	// And nothing was cached.
	if ok, _ := c.Has(ctx, "k"); ok {
		t.Fatal("failed load must not cache")
	}
}

func TestPull_ReturnsAndRemoves(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	_ = c.Put(ctx, "k", []byte("v"), 0)
	v, ok, err := Pull(ctx, c, "k")
	if err != nil || !ok || string(v) != "v" {
		t.Fatalf("Pull = (%q, %v, %v)", v, ok, err)
	}
	if ok, _ := c.Has(ctx, "k"); ok {
		t.Fatal("Pull must remove key")
	}
}

func TestMemory_ConcurrentSafe(t *testing.T) {
	ctx := context.Background()
	c := NewMemory()
	defer c.Close()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_ = c.Put(ctx, "k", []byte{byte(i)}, time.Minute)
		}(i)
		go func() {
			defer wg.Done()
			_, _, _ = c.Get(ctx, "k")
		}()
	}
	wg.Wait()
}
