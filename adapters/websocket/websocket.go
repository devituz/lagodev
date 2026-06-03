// Package websocket provides a Hub + Connection abstraction over
// WebSockets, modelled on Laravel Echo's room/channel semantics.
//
// The adapter is a separate Go module so the core lagodev module stays
// dependency-free:
//
//	go get github.com/devituz/lagodev/adapters/websocket@latest
//
// Architecture:
//
//   - Hub          — the per-app singleton. Tracks all live
//                    connections by ID and by channel (Laravel "room").
//   - Connection   — a single open WebSocket; sends are non-blocking
//                    with a bounded outbox.
//   - Handler      — http.Handler that performs the WebSocket
//                    handshake and registers the connection on the Hub.
//
// Usage:
//
//	hub := websocket.NewHub()
//	go hub.Run(ctx)
//
//	mux.Handle("/ws", hub.Handler(func(ctx context.Context, c *websocket.Connection, m []byte) error {
//	    // m is one incoming text/binary message
//	    return hub.Broadcast(ctx, "chat.42", m)
//	}))
//
//	// later, from anywhere:
//	_ = hub.Broadcast(ctx, "chat.42", []byte(`{"text":"hello"}`))
//
// The connection lifecycle (join channel, receive, broadcast, leave)
// is fully exposed so applications can layer auth, presence, or
// per-message ACK semantics on top.
package websocket

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	ws "github.com/coder/websocket"
)

// ErrClosed is returned when an operation is attempted on a closed
// Hub or Connection.
var ErrClosed = errors.New("websocket: closed")

// MessageHandler receives each frame the client sends. Return non-nil
// to close the connection.
type MessageHandler func(ctx context.Context, c *Connection, message []byte) error

// HubOption customises Hub construction.
type HubOption func(*Hub)

// WithOutbox sets the per-connection outbound buffer size. Default 64.
func WithOutbox(n int) HubOption {
	if n < 1 {
		panic("websocket: WithOutbox must be > 0")
	}
	return func(h *Hub) { h.outbox = n }
}

// WithOrigins restricts the handshake's Origin header. Empty list
// disables the check (DEV ONLY). Default empty.
func WithOrigins(origins ...string) HubOption {
	return func(h *Hub) { h.origins = append([]string{}, origins...) }
}

// WithPingInterval sets the server-side keep-alive ping cadence.
// Default 30s.
func WithPingInterval(d time.Duration) HubOption {
	return func(h *Hub) { h.pingInterval = d }
}

// Hub orchestrates Connections.
type Hub struct {
	mu           sync.RWMutex
	conns        map[string]*Connection
	channels     map[string]map[string]*Connection // channel → connID → conn
	closed       bool
	outbox       int
	origins      []string
	pingInterval time.Duration
	counter      uint64
}

// NewHub returns an empty Hub.
func NewHub(opts ...HubOption) *Hub {
	h := &Hub{
		conns:        make(map[string]*Connection),
		channels:     make(map[string]map[string]*Connection),
		outbox:       64,
		pingInterval: 30 * time.Second,
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Run is reserved for future per-Hub housekeeping (cleanup tickers,
// metrics flush, …). It blocks until ctx is done; safe to omit if
// nothing else uses the Hub's background ticks.
func (h *Hub) Run(ctx context.Context) {
	<-ctx.Done()
}

// Close terminates every open connection. After Close, further
// Handler calls accept handshakes but immediately reject them.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	conns := h.conns
	h.conns = nil
	h.channels = nil
	h.mu.Unlock()
	for _, c := range conns {
		c.close(ws.StatusNormalClosure, "hub closed")
	}
}

// Count returns the number of live connections.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// ChannelCount returns the number of subscribers on channel.
func (h *Hub) ChannelCount(channel string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.channels[channel])
}

// Broadcast sends payload to every connection joined to channel.
// Per-connection delivery is best-effort: full outboxes drop the
// message and increment the connection's drop counter.
func (h *Hub) Broadcast(_ context.Context, channel string, payload []byte) error {
	h.mu.RLock()
	if h.closed {
		h.mu.RUnlock()
		return ErrClosed
	}
	subs := make([]*Connection, 0, len(h.channels[channel]))
	for _, c := range h.channels[channel] {
		subs = append(subs, c)
	}
	h.mu.RUnlock()
	for _, c := range subs {
		c.enqueue(payload)
	}
	return nil
}

// SendTo addresses a single connection by ID.
func (h *Hub) SendTo(connID string, payload []byte) error {
	h.mu.RLock()
	c, ok := h.conns[connID]
	h.mu.RUnlock()
	if !ok {
		return errors.New("websocket: connection not found")
	}
	c.enqueue(payload)
	return nil
}

// Handler returns an http.Handler that upgrades the request to a
// WebSocket connection, registers it on the Hub, and runs onMessage
// for each incoming frame until the connection closes.
func (h *Hub) Handler(onMessage MessageHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.RLock()
		closed := h.closed
		h.mu.RUnlock()
		if closed {
			http.Error(w, "websocket: hub closed", http.StatusServiceUnavailable)
			return
		}
		opts := &ws.AcceptOptions{
			InsecureSkipVerify: len(h.origins) == 0,
			OriginPatterns:     h.origins,
		}
		conn, err := ws.Accept(w, r, opts)
		if err != nil {
			return
		}
		c := h.register(conn)
		defer h.unregister(c)
		c.run(r.Context(), onMessage)
	})
}

func (h *Hub) register(raw *ws.Conn) *Connection {
	id := newConnID(atomic.AddUint64(&h.counter, 1))
	c := &Connection{
		id:       id,
		hub:      h,
		raw:      raw,
		outbox:   make(chan []byte, h.outbox),
		done:     make(chan struct{}),
		channels: make(map[string]struct{}),
	}
	h.mu.Lock()
	if h.conns == nil {
		// Hub was closed concurrently.
		h.mu.Unlock()
		_ = raw.Close(ws.StatusGoingAway, "hub closed")
		close(c.done)
		return c
	}
	h.conns[id] = c
	h.mu.Unlock()
	return c
}

func (h *Hub) unregister(c *Connection) {
	h.mu.Lock()
	delete(h.conns, c.id)
	for ch := range c.channels {
		if m, ok := h.channels[ch]; ok {
			delete(m, c.id)
			if len(m) == 0 {
				delete(h.channels, ch)
			}
		}
	}
	h.mu.Unlock()
}

func newConnID(n uint64) string {
	// Simple monotonic ID; collision-free per Hub.
	return "conn-" + uitoa(n)
}

func uitoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// Connection represents one live WebSocket. Methods are safe for
// concurrent use unless otherwise noted.
type Connection struct {
	id       string
	hub      *Hub
	raw      *ws.Conn
	outbox   chan []byte
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	channels map[string]struct{}
	dropped  uint64
}

// ID returns the connection's identifier within its Hub.
func (c *Connection) ID() string { return c.id }

// Join adds the connection to a channel. Idempotent.
func (c *Connection) Join(channel string) {
	c.mu.Lock()
	c.channels[channel] = struct{}{}
	c.mu.Unlock()
	c.hub.mu.Lock()
	if c.hub.channels == nil {
		c.hub.mu.Unlock()
		return
	}
	m, ok := c.hub.channels[channel]
	if !ok {
		m = make(map[string]*Connection)
		c.hub.channels[channel] = m
	}
	m[c.id] = c
	c.hub.mu.Unlock()
}

// Leave removes the connection from a channel. Idempotent.
func (c *Connection) Leave(channel string) {
	c.mu.Lock()
	delete(c.channels, channel)
	c.mu.Unlock()
	c.hub.mu.Lock()
	if m, ok := c.hub.channels[channel]; ok {
		delete(m, c.id)
		if len(m) == 0 {
			delete(c.hub.channels, channel)
		}
	}
	c.hub.mu.Unlock()
}

// Send queues payload for delivery to the client. Non-blocking — a
// full outbox drops the payload and bumps the dropped counter.
func (c *Connection) Send(payload []byte) { c.enqueue(payload) }

// Dropped returns how many outbound payloads have been dropped due
// to a full outbox. Useful as a saturation metric.
func (c *Connection) Dropped() uint64 { return atomic.LoadUint64(&c.dropped) }

func (c *Connection) enqueue(payload []byte) {
	select {
	case c.outbox <- payload:
	default:
		atomic.AddUint64(&c.dropped, 1)
	}
}

func (c *Connection) close(code ws.StatusCode, reason string) {
	c.once.Do(func() {
		close(c.done)
		_ = c.raw.Close(code, reason)
	})
}

// Close gracefully terminates the connection.
func (c *Connection) Close() { c.close(ws.StatusNormalClosure, "") }

func (c *Connection) run(ctx context.Context, onMessage MessageHandler) {
	defer c.Close()
	// Writer goroutine
	go func() {
		ticker := time.NewTicker(c.hub.pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-c.done:
				return
			case msg := <-c.outbox:
				wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := c.raw.Write(wctx, ws.MessageText, msg)
				cancel()
				if err != nil {
					return
				}
			case <-ticker.C:
				pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := c.raw.Ping(pctx)
				cancel()
				if err != nil {
					return
				}
			}
		}
	}()
	// Reader loop (blocks)
	for {
		_, data, err := c.raw.Read(ctx)
		if err != nil {
			return
		}
		if onMessage != nil {
			if err := onMessage(ctx, c, data); err != nil {
				return
			}
		}
	}
}
