// Package cache provides a key-value cache abstraction with a built-in
// in-memory store. Modelled on Laravel's Cache facade.
//
// The Store interface is small enough to back with Redis, Memcached, or
// a database — those drivers live in separate sub-packages so the core
// module has zero extra dependencies.
//
// Usage:
//
//	c := cache.NewMemory()
//	c.Put(ctx, "user:1", []byte("Ada"), 5*time.Minute)
//	v, ok, _ := c.Get(ctx, "user:1")
//	c.Forget(ctx, "user:1")
//
// Helper:
//
//	v, err := cache.Remember(ctx, c, "users:list", time.Minute, func() ([]byte, error) {
//	    return fetchAndSerializeUsers()
//	})
package cache

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrMiss is returned by Get-shaped APIs that signal absence with an
// error. The basic Store interface uses (value, ok, error) instead, so
// ErrMiss is mostly informational for higher layers.
var ErrMiss = errors.New("cache: key not found")

// Store is the minimum surface a cache driver must implement.
//
// All methods take a context so callers can cancel slow remote stores.
// The in-memory implementation ignores the context value but still
// respects the deadline via the (rare) blocking codepaths.
type Store interface {
	// Get returns the value, whether the key existed, and any driver
	// error. An expired entry behaves like a miss (ok=false, err=nil).
	Get(ctx context.Context, key string) ([]byte, bool, error)
	// Put stores value under key with the given TTL. ttl==0 means store
	// without expiry (until explicit Forget / Flush); ttl<0 means the
	// entry is already expired and is treated as an immediate miss (the
	// key is removed rather than stored).
	Put(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Forget removes the key (no error if missing).
	Forget(ctx context.Context, key string) error
	// Flush wipes everything.
	Flush(ctx context.Context) error
	// Has reports whether the key exists and is not expired.
	Has(ctx context.Context, key string) (bool, error)
}

// Memory is a process-local Store backed by a sync.RWMutex map. Safe
// for concurrent use; expiry is lazy (checked on Get/Has) with a
// background sweeper to keep the map from growing unbounded.
type Memory struct {
	mu    sync.RWMutex
	items map[string]memoryItem
	stop  chan struct{}
}

type memoryItem struct {
	value     []byte
	expiresAt time.Time // zero = no expiry
}

// NewMemory returns a fresh in-memory cache. A goroutine periodically
// purges expired keys; call Close() when discarding the cache to stop
// it.
func NewMemory() *Memory {
	m := &Memory{
		items: make(map[string]memoryItem),
		stop:  make(chan struct{}),
	}
	go m.sweep()
	return m
}

// Close stops the background sweeper. Subsequent operations remain
// safe but expired entries will only be evicted on access.
func (m *Memory) Close() {
	select {
	case <-m.stop:
		// already closed
	default:
		close(m.stop)
	}
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.RLock()
	it, ok := m.items[key]
	m.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	if !it.expiresAt.IsZero() && time.Now().After(it.expiresAt) {
		m.mu.Lock()
		delete(m.items, key)
		m.mu.Unlock()
		return nil, false, nil
	}
	// Return a copy so callers can't mutate the stored slice.
	out := make([]byte, len(it.value))
	copy(out, it.value)
	return out, true, nil
}

func (m *Memory) Put(_ context.Context, key string, value []byte, ttl time.Duration) error {
	// A negative TTL means the entry is already expired: don't store it,
	// and drop any existing value so the key reads as a miss.
	if ttl < 0 {
		m.mu.Lock()
		delete(m.items, key)
		m.mu.Unlock()
		return nil
	}
	stored := make([]byte, len(value))
	copy(stored, value)
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	m.mu.Lock()
	m.items[key] = memoryItem{value: stored, expiresAt: exp}
	m.mu.Unlock()
	return nil
}

func (m *Memory) Forget(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.items, key)
	m.mu.Unlock()
	return nil
}

func (m *Memory) Flush(_ context.Context) error {
	m.mu.Lock()
	m.items = make(map[string]memoryItem)
	m.mu.Unlock()
	return nil
}

func (m *Memory) Has(ctx context.Context, key string) (bool, error) {
	_, ok, err := m.Get(ctx, key)
	return ok, err
}

// Pull returns the value for key and removes it in a single critical
// section, so a concurrent Pull of the same key can succeed at most
// once. Returns (nil, false, nil) on miss or expiry.
func (m *Memory) Pull(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[key]
	if !ok {
		return nil, false, nil
	}
	delete(m.items, key)
	if !it.expiresAt.IsZero() && time.Now().After(it.expiresAt) {
		return nil, false, nil
	}
	out := make([]byte, len(it.value))
	copy(out, it.value)
	return out, true, nil
}

func (m *Memory) sweep() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			now := time.Now()
			m.mu.Lock()
			for k, it := range m.items {
				if !it.expiresAt.IsZero() && now.After(it.expiresAt) {
					delete(m.items, k)
				}
			}
			m.mu.Unlock()
		}
	}
}

// Remember returns the value for key if present, otherwise calls fn,
// stores the result with ttl, and returns it. The classic
// "cache-aside" pattern.
//
//	users, err := cache.Remember(ctx, store, "users:list", time.Minute,
//	    func() ([]byte, error) { return json.Marshal(listUsers()) })
func Remember(ctx context.Context, s Store, key string, ttl time.Duration, fn func() ([]byte, error)) ([]byte, error) {
	if v, ok, err := s.Get(ctx, key); err != nil {
		return nil, err
	} else if ok {
		return v, nil
	}
	v, err := fn()
	if err != nil {
		return nil, err
	}
	if err := s.Put(ctx, key, v, ttl); err != nil {
		return v, err
	}
	return v, nil
}

// puller is an optional Store extension that reads-and-deletes a key in
// a single atomic operation. *Memory implements it.
type puller interface {
	Pull(ctx context.Context, key string) ([]byte, bool, error)
}

// Pull reads a key and removes it. If the underlying store implements
// puller (e.g. *Memory) the read-and-delete is atomic; otherwise it
// falls back to a non-atomic Get followed by Forget, which may race with
// a concurrent Pull/Put of the same key. Returns (nil, false, nil) on
// miss.
func Pull(ctx context.Context, s Store, key string) ([]byte, bool, error) {
	if p, ok := s.(puller); ok {
		return p.Pull(ctx, key)
	}
	v, ok, err := s.Get(ctx, key)
	if err != nil || !ok {
		return nil, ok, err
	}
	_ = s.Forget(ctx, key)
	return v, true, nil
}
