package realtime

import (
	"errors"
	"sync"
	"sync/atomic"
)

// SlowConsumerPolicy selects what the Hub does when a client's outbound
// buffer is full at enqueue time.
type SlowConsumerPolicy int

const (
	// DropMessage discards the message and bumps the client's drop
	// counter. The connection survives. Good for lossy telemetry feeds.
	DropMessage SlowConsumerPolicy = iota
	// DisconnectClient closes the connection on the first overflow. Good
	// for ordered, can't-miss streams where a gap is worse than a drop.
	DisconnectClient
)

// HubOption customises Hub construction.
type HubOption func(*Hub)

// WithOutbox sets the per-client outbound buffer size. Default 64.
func WithOutbox(n int) HubOption {
	if n < 1 {
		panic("realtime: WithOutbox must be > 0")
	}
	return func(h *Hub) { h.outbox = n }
}

// WithAuthorizer installs the hook consulted for private/presence joins.
// Without it, every private/presence join is denied.
func WithAuthorizer(a Authorizer) HubOption {
	return func(h *Hub) { h.authorizer = a }
}

// WithSlowConsumerPolicy selects the overflow behaviour. Default
// DropMessage.
func WithSlowConsumerPolicy(p SlowConsumerPolicy) HubOption {
	return func(h *Hub) { h.slowPolicy = p }
}

// WithLogger installs a structured logger callback invoked on drops and
// authorization denials. Default silent.
func WithLogger(fn func(string, ...any)) HubOption {
	return func(h *Hub) { h.logger = fn }
}

// PresenceEvent describes a roster change on a presence channel. It is
// delivered to the application via the OnPresence hook, separate from the
// wire frames the Hub sends to members.
type PresenceEvent struct {
	Channel string
	// Joined is set on a join, Left on a leave.
	Joined bool
	Member Member
}

// Hub is the per-app realtime gateway. It is safe for concurrent use.
type Hub struct {
	mu       sync.RWMutex
	clients  map[string]*Client
	channels map[string]*channel
	closed   bool

	outbox     int
	authorizer Authorizer
	slowPolicy SlowConsumerPolicy
	logger     func(string, ...any)

	onPresence func(PresenceEvent)

	counter uint64
}

// NewHub returns an empty Hub.
func NewHub(opts ...HubOption) *Hub {
	h := &Hub{
		clients:    make(map[string]*Client),
		channels:   make(map[string]*channel),
		outbox:     64,
		slowPolicy: DropMessage,
		logger:     func(string, ...any) {},
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// OnPresence registers a callback invoked for every presence join/leave.
// It runs synchronously on the goroutine driving the roster change, so it
// must not block. Replaces any previous callback.
func (h *Hub) OnPresence(fn func(PresenceEvent)) {
	h.mu.Lock()
	h.onPresence = fn
	h.mu.Unlock()
}

// Add registers conn on the Hub and returns the resulting Client. The
// caller owns the read loop: call Client.Serve(onMessage) to pump inbound
// frames (typically in a fresh goroutine), and Client.Close / Hub.Close to
// tear down. auth carries optional client credentials forwarded to the
// Authorizer on private/presence joins.
func (h *Hub) Add(conn Conn, auth []byte) (*Client, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	id := newClientID(atomic.AddUint64(&h.counter, 1))
	c := &Client{
		id:       id,
		hub:      h,
		conn:     conn,
		auth:     auth,
		outbox:   make(chan outMsg, h.outbox),
		done:     make(chan struct{}),
		channels: make(map[string]struct{}),
	}
	h.clients[id] = c
	h.mu.Unlock()
	go c.writeLoop()
	return c, nil
}

// Subscribe joins client to channel, applying authorization for
// private/presence channels and emitting presence join events. It returns
// ErrUnauthorized if the Authorizer denies the join.
func (h *Hub) Subscribe(c *Client, channel string) error {
	return h.subscribe(c, channel, Member{})
}

// ErrUnauthorized is returned by Subscribe when a private/presence join is
// denied by the Authorizer (or when none is configured).
var ErrUnauthorized = errors.New("realtime: unauthorized")

func (h *Hub) subscribe(c *Client, name string, _ Member) error {
	typ := ClassifyChannel(name)

	// Authorize private/presence joins outside the lock (the hook is
	// user code and may be slow).
	var member Member
	if typ == Private || typ == Presence {
		if h.authorizer == nil {
			h.logger("realtime: join denied on %q — no authorizer", name)
			return ErrUnauthorized
		}
		res := h.authorizer.Authorize(AuthRequest{
			Channel:  name,
			Type:     typ,
			ClientID: c.id,
			Auth:     c.auth,
		})
		if !res.Allowed {
			h.logger("realtime: join denied on %q for client %s", name, c.id)
			return ErrUnauthorized
		}
		member = res.Member
		if member.ID == "" {
			member.ID = c.id
		}
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ErrClosed
	}
	ch, ok := h.channels[name]
	if !ok {
		ch = newChannel(name)
		h.channels[name] = ch
	}
	if _, already := ch.members[c.id]; already {
		h.mu.Unlock()
		return nil // idempotent
	}
	ch.members[c.id] = c
	c.mu.Lock()
	c.channels[name] = struct{}{}
	c.mu.Unlock()

	var joins []*Client
	var roster []Member
	if typ == Presence {
		// Snapshot the existing roster to hand to the newcomer, then add
		// the newcomer and gather the members to notify.
		roster = make([]Member, 0, len(ch.roster))
		for _, m := range ch.roster {
			roster = append(roster, m)
		}
		ch.roster[c.id] = member
		joins = make([]*Client, 0, len(ch.members))
		for _, m := range ch.members {
			joins = append(joins, m)
		}
	}
	onPresence := h.onPresence
	h.mu.Unlock()

	if typ == Presence {
		// Send the current roster to the newcomer only.
		c.enqueue(presenceState(name, roster))
		// Notify every member (including the newcomer) of the join.
		frame := presenceJoin(name, member)
		for _, m := range joins {
			m.enqueue(frame)
		}
		if onPresence != nil {
			onPresence(PresenceEvent{Channel: name, Joined: true, Member: member})
		}
	}
	return nil
}

// Unsubscribe removes client from channel, emitting a presence leave event
// for presence channels. Idempotent.
func (h *Hub) Unsubscribe(c *Client, name string) {
	h.mu.Lock()
	ch, ok := h.channels[name]
	if !ok {
		h.mu.Unlock()
		return
	}
	if _, member := ch.members[c.id]; !member {
		h.mu.Unlock()
		return
	}
	delete(ch.members, c.id)
	c.mu.Lock()
	delete(c.channels, name)
	c.mu.Unlock()

	var leaver Member
	var notify []*Client
	emit := false
	if ch.typ == Presence {
		leaver = ch.roster[c.id]
		delete(ch.roster, c.id)
		emit = true
		notify = make([]*Client, 0, len(ch.members))
		for _, m := range ch.members {
			notify = append(notify, m)
		}
	}
	if len(ch.members) == 0 {
		delete(h.channels, name)
	}
	onPresence := h.onPresence
	h.mu.Unlock()

	if emit {
		frame := presenceLeave(name, leaver)
		for _, m := range notify {
			m.enqueue(frame)
		}
		if onPresence != nil {
			onPresence(PresenceEvent{Channel: name, Joined: false, Member: leaver})
		}
	}
}

// Broadcast delivers payload (as a text frame) to every client subscribed
// to channel. Per-client delivery is non-blocking subject to the Hub's
// slow-consumer policy. Safe to pass as a broadcasting.Handler bridge.
func (h *Hub) Broadcast(channel string, payload []byte) error {
	return h.broadcast(channel, TextMessage, payload, "")
}

// BroadcastBinary is Broadcast for binary frames.
func (h *Hub) BroadcastBinary(channel string, payload []byte) error {
	return h.broadcast(channel, BinaryMessage, payload, "")
}

// BroadcastExcept is Broadcast that skips one client id (e.g. the sender),
// matching Echo's toOthers semantics.
func (h *Hub) BroadcastExcept(channel string, payload []byte, exceptID string) error {
	return h.broadcast(channel, TextMessage, payload, exceptID)
}

func (h *Hub) broadcast(name string, typ MessageType, payload []byte, except string) error {
	h.mu.RLock()
	if h.closed {
		h.mu.RUnlock()
		return ErrClosed
	}
	ch, ok := h.channels[name]
	if !ok {
		h.mu.RUnlock()
		return nil
	}
	subs := make([]*Client, 0, len(ch.members))
	for id, c := range ch.members {
		if id == except {
			continue
		}
		subs = append(subs, c)
	}
	h.mu.RUnlock()
	for _, c := range subs {
		c.enqueueMsg(outMsg{typ: typ, data: payload})
	}
	return nil
}

// SendTo delivers payload directly to a single client by id.
func (h *Hub) SendTo(clientID string, payload []byte) error {
	h.mu.RLock()
	c, ok := h.clients[clientID]
	h.mu.RUnlock()
	if !ok {
		return errors.New("realtime: client not found")
	}
	c.enqueue(payload)
	return nil
}

// Presence returns the current roster of a presence channel. The slice is
// a copy; order is unspecified. Returns nil for unknown or non-presence
// channels.
func (h *Hub) Presence(channel string) []Member {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ch, ok := h.channels[channel]
	if !ok || ch.typ != Presence {
		return nil
	}
	out := make([]Member, 0, len(ch.roster))
	for _, m := range ch.roster {
		out = append(out, m)
	}
	return out
}

// Count returns the number of live clients.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ChannelCount returns the number of subscribers on channel.
func (h *Hub) ChannelCount(channel string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if ch, ok := h.channels[channel]; ok {
		return len(ch.members)
	}
	return 0
}

// remove tears a client out of every channel and the client table. It is
// the single point that fires leave events on disconnect, so no
// subscription leaks. Called by Client.Close.
func (h *Hub) remove(c *Client) {
	h.mu.Lock()
	delete(h.clients, c.id)
	c.mu.Lock()
	names := make([]string, 0, len(c.channels))
	for name := range c.channels {
		names = append(names, name)
	}
	c.channels = make(map[string]struct{})
	c.mu.Unlock()

	type leave struct {
		channel string
		member  Member
		notify  []*Client
	}
	var leaves []leave
	for _, name := range names {
		ch, ok := h.channels[name]
		if !ok {
			continue
		}
		delete(ch.members, c.id)
		if ch.typ == Presence {
			l := leave{channel: name, member: ch.roster[c.id]}
			delete(ch.roster, c.id)
			for _, m := range ch.members {
				l.notify = append(l.notify, m)
			}
			leaves = append(leaves, l)
		}
		if len(ch.members) == 0 {
			delete(h.channels, name)
		}
	}
	onPresence := h.onPresence
	h.mu.Unlock()

	for _, l := range leaves {
		frame := presenceLeave(l.channel, l.member)
		for _, m := range l.notify {
			m.enqueue(frame)
		}
		if onPresence != nil {
			onPresence(PresenceEvent{Channel: l.channel, Joined: false, Member: l.member})
		}
	}
}

// Close terminates every client and rejects further Add calls.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	clients := h.clients
	h.clients = make(map[string]*Client)
	h.channels = make(map[string]*channel)
	h.mu.Unlock()
	for _, c := range clients {
		c.Close()
	}
}

func newClientID(n uint64) string {
	return "client-" + uitoa(n)
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
