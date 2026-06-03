// Package redis provides Redis-backed implementations of lagodev's
// broadcasting.Broadcaster and queue.Queue interfaces. Built on
// github.com/redis/go-redis/v9.
//
// One Go module ships both drivers because they share the same Redis
// client dependency tree — install once, pick the type(s) you need:
//
//	go get github.com/devituz/lagodev/drivers/redis@latest
//
//	rdb := redislib.NewClient(&redislib.Options{Addr: "localhost:6379"})
//	b   := redis.NewBroadcaster(rdb)             // broadcasting.Broadcaster
//	q   := redis.NewQueue(rdb, "default")        // queue.Queue
package redis

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/devituz/lagodev/broadcasting"
	redislib "github.com/redis/go-redis/v9"
)

// Broadcaster fans events out over Redis Pub/Sub. One Redis client
// drives both Publish and Subscribe; each subscription holds its own
// goroutine reading from a PubSub.
type Broadcaster struct {
	client *redislib.Client
	prefix string
	logger func(string, ...any)

	mu      sync.Mutex
	closed  bool
	active  map[*redisSubscription]struct{}
	dropped uint64
}

// BroadcasterOption customises Broadcaster behaviour.
type BroadcasterOption func(*Broadcaster)

// WithPrefix prepends prefix + ":" to every channel name. Useful for
// running multiple lagodev apps against a shared Redis.
func WithPrefix(prefix string) BroadcasterOption {
	return func(b *Broadcaster) { b.prefix = prefix }
}

// WithLogger installs a structured logger called on handler errors or
// dropped events.
func WithLogger(fn func(string, ...any)) BroadcasterOption {
	return func(b *Broadcaster) { b.logger = fn }
}

// NewBroadcaster returns a Broadcaster bound to rdb.
func NewBroadcaster(rdb *redislib.Client, opts ...BroadcasterOption) *Broadcaster {
	b := &Broadcaster{
		client: rdb,
		logger: func(string, ...any) {},
		active: map[*redisSubscription]struct{}{},
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Static compile-time guard.
var _ broadcasting.Broadcaster = (*Broadcaster)(nil)

func (b *Broadcaster) chanKey(name string) string {
	if b.prefix == "" {
		return name
	}
	return b.prefix + ":" + name
}

// Publish encodes the event into a Redis PUBLISH frame
// ("<name>\n<payload>") so subscribers can recover the Name +
// Payload fields. The base lagodev Event has no Type other than Name
// and Payload, so this two-line encoding stays lossless.
func (b *Broadcaster) Publish(ctx context.Context, e broadcasting.Event) error {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return broadcasting.ErrClosed
	}
	frame := encodeEvent(e)
	if err := b.client.Publish(ctx, b.chanKey(e.Channel), frame).Err(); err != nil {
		return fmt.Errorf("redis: publish: %w", err)
	}
	return nil
}

// Subscribe spawns a goroutine that pumps the Redis PubSub channel
// into fn. The returned Subscription unbinds when Cancel is called.
func (b *Broadcaster) Subscribe(ctx context.Context, channel string, fn broadcasting.Handler) (broadcasting.Subscription, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, broadcasting.ErrClosed
	}
	b.mu.Unlock()
	ps := b.client.Subscribe(ctx, b.chanKey(channel))
	// Block until subscription is established so the caller doesn't
	// race against an immediate Publish.
	if _, err := ps.Receive(ctx); err != nil {
		_ = ps.Close()
		return nil, fmt.Errorf("redis: subscribe: %w", err)
	}
	sub := &redisSubscription{
		owner:   b,
		ps:      ps,
		channel: channel,
		fn:      fn,
		done:    make(chan struct{}),
	}
	b.mu.Lock()
	b.active[sub] = struct{}{}
	b.mu.Unlock()
	go sub.run(ctx)
	return sub, nil
}

// Close cancels every active subscription and releases the client
// resources held by this Broadcaster. The shared *redis.Client is NOT
// closed (the caller owns it and may share it across drivers).
func (b *Broadcaster) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	subs := b.active
	b.active = nil
	b.mu.Unlock()
	for s := range subs {
		s.Cancel()
	}
	return nil
}

// Dropped returns how many events were dropped due to handler errors
// being silently logged.
func (b *Broadcaster) Dropped() uint64 { return atomic.LoadUint64(&b.dropped) }

type redisSubscription struct {
	owner   *Broadcaster
	ps      *redislib.PubSub
	channel string
	fn      broadcasting.Handler
	done    chan struct{}
	once    sync.Once
}

func (s *redisSubscription) Cancel() {
	s.once.Do(func() {
		close(s.done)
		_ = s.ps.Close()
		if s.owner != nil {
			s.owner.mu.Lock()
			delete(s.owner.active, s)
			s.owner.mu.Unlock()
		}
	})
}

func (s *redisSubscription) run(ctx context.Context) {
	ch := s.ps.Channel()
	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			s.Cancel()
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			name, payload := decodeEvent(m.Payload)
			ev := broadcasting.Event{Channel: s.channel, Name: name, Payload: payload}
			if err := s.fn(ctx, ev); err != nil {
				atomic.AddUint64(&s.owner.dropped, 1)
				s.owner.logger("redis: handler on %q failed: %v", s.channel, err)
			}
		}
	}
}

// encodeEvent serialises name and payload into a single Redis Pub/Sub
// frame. Format: "<name>\n<payload-bytes>".
func encodeEvent(e broadcasting.Event) string {
	if e.Name == "" {
		return "\n" + string(e.Payload)
	}
	return e.Name + "\n" + string(e.Payload)
}

func decodeEvent(s string) (string, []byte) {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i], []byte(s[i+1:])
		}
	}
	return s, nil
}

// guard against incidental imports.
var _ = errors.New
