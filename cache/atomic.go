package cache

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// ErrUnsupported is returned by free helpers whose semantics cannot be
// emulated safely on a plain Store. For example, Increment needs an
// atomic read-modify-write that a basic Get+Put cannot provide without
// racing, so on stores that do not implement the incrementer optional
// interface the helper reports ErrUnsupported rather than silently
// corrupting the counter under concurrency.
var ErrUnsupported = errors.New("cache: operation not supported by store")

// expired reports whether it is past its expiry. A zero expiresAt means
// "no expiry" (ttl==0 semantics) and never expires.
func (it memoryItem) expired(now time.Time) bool {
	return !it.expiresAt.IsZero() && now.After(it.expiresAt)
}

// expiryFor converts a ttl into an absolute expiry time, matching Put's
// semantics: ttl>0 expires in the future, ttl==0 never expires (zero
// time). It must not be called with ttl<0 (callers handle that as an
// immediate miss before reaching here).
func expiryFor(ttl time.Duration) time.Time {
	if ttl > 0 {
		return time.Now().Add(ttl)
	}
	return time.Time{}
}

// Add stores value under key only if the key is absent (or expired),
// returning true when it stored. A return of false means a live value
// already occupied the key and was left untouched. The check and the
// write happen in one critical section, so Add is a safe lock primitive:
// of N concurrent Adds for the same key exactly one wins.
//
// TTL semantics match Put: ttl==0 stores forever, ttl<0 is treated as an
// already-expired write and stores nothing (Add returns false only when a
// live value blocks it, so a ttl<0 Add on an absent key returns true
// without storing).
func (m *Memory) Add(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if it, ok := m.items[key]; ok && !it.expired(time.Now()) {
		return false, nil
	}
	if ttl < 0 {
		// Already-expired write: nothing to store, but the key was free.
		delete(m.items, key)
		return true, nil
	}
	stored := make([]byte, len(value))
	copy(stored, value)
	m.items[key] = memoryItem{value: stored, expiresAt: expiryFor(ttl)}
	return true, nil
}

// Increment atomically adds delta to the integer counter stored at key
// and returns the new value. A missing or expired key starts from 0. The
// counter is persisted as its decimal ASCII representation so a plain Get
// reads it back consistently (e.g. Get after Increment by 5 returns
// []byte("5")). Incrementing a key whose value is not a base-10 integer
// returns an error and leaves the value unchanged.
//
// Counters are stored without expiry (forever); use Put/Forget to reset
// or bound their lifetime.
func (m *Memory) Increment(_ context.Context, key string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var cur int64
	if it, ok := m.items[key]; ok && !it.expired(time.Now()) {
		n, err := strconv.ParseInt(string(it.value), 10, 64)
		if err != nil {
			return 0, errors.New("cache: value at key is not an integer")
		}
		cur = n
	}
	next := cur + delta
	m.items[key] = memoryItem{value: strconv.AppendInt(nil, next, 10)}
	return next, nil
}

// Decrement atomically subtracts delta from the counter at key. It is
// Increment with a negated delta; see Increment for value encoding and
// error behaviour.
func (m *Memory) Decrement(ctx context.Context, key string, delta int64) (int64, error) {
	return m.Increment(ctx, key, -delta)
}

// Forever stores value under key with no expiry. It is shorthand for
// Put(ctx, key, value, 0).
func (m *Memory) Forever(ctx context.Context, key string, value []byte) error {
	return m.Put(ctx, key, value, 0)
}

// Many returns the values for the present, non-expired keys in a single
// critical section. Missing or expired keys are simply omitted from the
// result map, so len(result) may be < len(keys). Returned slices are
// copies; callers may mutate them freely.
func (m *Memory) Many(_ context.Context, keys ...string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(keys))
	now := time.Now()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, key := range keys {
		it, ok := m.items[key]
		if !ok || it.expired(now) {
			continue
		}
		v := make([]byte, len(it.value))
		copy(v, it.value)
		out[key] = v
	}
	return out, nil
}

// PutMany stores every item under the same TTL in a single critical
// section. TTL semantics match Put: ttl==0 stores forever, ttl<0 deletes
// each key (treated as an already-expired write).
func (m *Memory) PutMany(_ context.Context, items map[string][]byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ttl < 0 {
		for key := range items {
			delete(m.items, key)
		}
		return nil
	}
	exp := expiryFor(ttl)
	for key, value := range items {
		stored := make([]byte, len(value))
		copy(stored, value)
		m.items[key] = memoryItem{value: stored, expiresAt: exp}
	}
	return nil
}

// adder is an optional Store extension implementing atomic put-if-absent.
// *Memory implements it.
type adder interface {
	Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
}

// incrementer is an optional Store extension implementing atomic integer
// counters. *Memory implements it. Stores that cannot offer an atomic
// read-modify-write should not implement this; the Increment/Decrement
// helpers then return ErrUnsupported rather than racing.
type incrementer interface {
	Increment(ctx context.Context, key string, delta int64) (int64, error)
	Decrement(ctx context.Context, key string, delta int64) (int64, error)
}

// manyer is an optional Store extension implementing batch get/set.
// *Memory implements it.
type manyer interface {
	Many(ctx context.Context, keys ...string) (map[string][]byte, error)
	PutMany(ctx context.Context, items map[string][]byte, ttl time.Duration) error
}

// Add stores value under key only if it is absent. If the store
// implements adder (e.g. *Memory) the check-and-set is atomic, making
// this a reliable lock primitive. Otherwise it falls back to a
// non-atomic Has followed by Put, which may race with a concurrent
// Add/Put of the same key — acceptable for best-effort caching, not for
// locking. Returns true when the value was stored.
func Add(ctx context.Context, s Store, key string, value []byte, ttl time.Duration) (bool, error) {
	if a, ok := s.(adder); ok {
		return a.Add(ctx, key, value, ttl)
	}
	has, err := s.Has(ctx, key)
	if err != nil {
		return false, err
	}
	if has {
		return false, nil
	}
	if err := s.Put(ctx, key, value, ttl); err != nil {
		return false, err
	}
	return true, nil
}

// Increment atomically adds delta to the integer counter at key and
// returns the new value. It requires a store that implements incrementer
// (e.g. *Memory); on any other store it returns ErrUnsupported, because
// a Get+Put emulation cannot guarantee the atomic read-modify-write a
// counter needs and would silently lose updates under concurrency.
func Increment(ctx context.Context, s Store, key string, delta int64) (int64, error) {
	if inc, ok := s.(incrementer); ok {
		return inc.Increment(ctx, key, delta)
	}
	return 0, ErrUnsupported
}

// Decrement atomically subtracts delta from the counter at key. See
// Increment for store requirements and the ErrUnsupported fallback.
func Decrement(ctx context.Context, s Store, key string, delta int64) (int64, error) {
	if inc, ok := s.(incrementer); ok {
		return inc.Decrement(ctx, key, delta)
	}
	return 0, ErrUnsupported
}

// Many returns the values for the present keys. If the store implements
// manyer (e.g. *Memory) the reads happen in one critical section;
// otherwise it falls back to a per-key Get loop, which is not a
// consistent snapshot but is otherwise correct. Missing/expired keys are
// omitted.
func Many(ctx context.Context, s Store, keys ...string) (map[string][]byte, error) {
	if mr, ok := s.(manyer); ok {
		return mr.Many(ctx, keys...)
	}
	out := make(map[string][]byte, len(keys))
	for _, key := range keys {
		v, ok, err := s.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if ok {
			out[key] = v
		}
	}
	return out, nil
}

// PutMany stores every item under the same TTL. If the store implements
// manyer (e.g. *Memory) the writes happen in one critical section;
// otherwise it falls back to a per-key Put loop and stops at the first
// error.
func PutMany(ctx context.Context, s Store, items map[string][]byte, ttl time.Duration) error {
	if mr, ok := s.(manyer); ok {
		return mr.PutMany(ctx, items, ttl)
	}
	for key, value := range items {
		if err := s.Put(ctx, key, value, ttl); err != nil {
			return err
		}
	}
	return nil
}
