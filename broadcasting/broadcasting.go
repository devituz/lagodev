// Package broadcasting provides a pub/sub abstraction modelled on
// Laravel's Broadcasting facade.
//
// The Broadcaster interface is intentionally small so a Memory driver
// can ship in-box (single-process apps, tests) while Redis/NATS/Kafka
// drivers live in sub-packages without dragging external deps into the
// core module.
//
// Usage:
//
//	bcast := broadcasting.NewMemory()
//	bcast.Subscribe(ctx, "chat.42", func(ctx context.Context, e broadcasting.Event) error {
//	    fmt.Printf("got %s: %s\n", e.Name, string(e.Payload))
//	    return nil
//	})
//	_ = bcast.Publish(ctx, broadcasting.Event{
//	    Channel: "chat.42",
//	    Name:    "MessagePosted",
//	    Payload: []byte(`{"text":"hi"}`),
//	})
//
// Compared to events:
//   - events  — synchronous, in-process, typed via generics. For
//                aggregating domain reactions during a single request.
//   - broadcasting — many-to-many fan-out across processes (when paired
//                with a remote driver) or within a process; subscribers
//                hold a queue and run on their own goroutine.
package broadcasting

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// ErrClosed is returned by Publish/Subscribe after Close.
var ErrClosed = errors.New("broadcasting: closed")

// Event is the wire-format unit dispatched through a Broadcaster.
// Payload is opaque bytes — drivers do not assume JSON, but the
// helper Publish methods in user code typically pre-encode payloads
// as JSON.
type Event struct {
	Channel string
	Name    string
	Payload []byte
}

// Handler is invoked once per delivered event. Errors are surfaced
// via the driver's logger callback (or dropped by Memory) — they do
// not propagate back to the publisher.
type Handler func(ctx context.Context, e Event) error

// Subscription represents one active subscription. Cancel removes the
// handler and releases its queue.
type Subscription interface {
	Cancel()
}

// Broadcaster is the transport interface.
type Broadcaster interface {
	// Publish delivers e to every active subscription whose channel
	// matches e.Channel.
	Publish(ctx context.Context, e Event) error
	// Subscribe registers fn to receive every event published on
	// channel. The returned Subscription unbinds fn when Cancel is
	// called.
	Subscribe(ctx context.Context, channel string, fn Handler) (Subscription, error)
	// Close releases all resources. Further Publish/Subscribe calls
	// return ErrClosed.
	Close() error
}

// --- Memory driver -----------------------------------------------------

// Memory is an in-process Broadcaster. Each subscription runs its
// own goroutine reading from a buffered channel. Bursts beyond the
// buffer are dropped; the dropped-count is exposed via Dropped() so
// callers can spot under-sized buffers in production.
type Memory struct {
	mu      sync.RWMutex
	subs    map[string][]*subscription
	closed  bool
	buffer  int
	logger  func(string, ...any)
	dropped uint64
}

// NewMemory returns a Broadcaster with default per-subscription buffer
// 256 and a silent logger. Tune via options.
func NewMemory(opts ...MemoryOption) *Memory {
	m := &Memory{
		subs:   make(map[string][]*subscription),
		buffer: 256,
		logger: func(string, ...any) {},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// MemoryOption customises a Memory broadcaster.
type MemoryOption func(*Memory)

// WithBuffer sets the per-subscription channel buffer.
func WithBuffer(n int) MemoryOption {
	if n < 1 {
		panic("broadcasting: WithBuffer must be > 0")
	}
	return func(m *Memory) { m.buffer = n }
}

// WithLogger installs a structured logger callback (called when a
// handler returns an error or an event is dropped).
func WithLogger(fn func(string, ...any)) MemoryOption {
	return func(m *Memory) { m.logger = fn }
}

// Publish fans the event out to every subscription on its channel.
// Non-blocking: if a subscriber's queue is full the event is dropped
// for that subscriber and the global Dropped counter is incremented.
func (m *Memory) Publish(_ context.Context, e Event) error {
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return ErrClosed
	}
	subs := append([]*subscription(nil), m.subs[e.Channel]...)
	m.mu.RUnlock()
	for _, s := range subs {
		select {
		case s.ch <- e:
		default:
			atomic.AddUint64(&m.dropped, 1)
			m.logger("broadcasting: subscriber queue full on %q — dropping event %q", e.Channel, e.Name)
		}
	}
	return nil
}

// Subscribe binds fn to receive events on channel.
func (m *Memory) Subscribe(ctx context.Context, channel string, fn Handler) (Subscription, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	s := &subscription{
		ch:      make(chan Event, m.buffer),
		fn:      fn,
		done:    make(chan struct{}),
		channel: channel,
		owner:   m,
	}
	m.subs[channel] = append(m.subs[channel], s)
	m.mu.Unlock()
	go s.run(ctx)
	return s, nil
}

// Close releases all subscriptions. Returns nil.
func (m *Memory) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	subs := m.subs
	m.subs = nil
	m.mu.Unlock()
	for _, list := range subs {
		for _, s := range list {
			s.Cancel()
		}
	}
	return nil
}

// Dropped returns the cumulative number of events that could not be
// delivered because some subscriber's queue was full. Useful as a
// production saturation metric.
func (m *Memory) Dropped() uint64 { return atomic.LoadUint64(&m.dropped) }

// subscription is the per-Subscriber goroutine.
type subscription struct {
	ch      chan Event
	fn      Handler
	done    chan struct{}
	channel string
	owner   *Memory
	once    sync.Once
}

func (s *subscription) Cancel() {
	s.once.Do(func() {
		// Remove from the broadcaster's table first so no new events
		// land in s.ch after we close done.
		if s.owner != nil {
			s.owner.mu.Lock()
			if list := s.owner.subs[s.channel]; len(list) > 0 {
				out := list[:0]
				for _, it := range list {
					if it != s {
						out = append(out, it)
					}
				}
				s.owner.subs[s.channel] = out
			}
			s.owner.mu.Unlock()
		}
		close(s.done)
	})
}

func (s *subscription) run(ctx context.Context) {
	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			s.Cancel()
			return
		case e := <-s.ch:
			if err := s.fn(ctx, e); err != nil && s.owner != nil {
				s.owner.logger("broadcasting: handler on %q failed: %v", s.channel, err)
			}
		}
	}
}
