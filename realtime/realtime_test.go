package realtime

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeConn is an in-memory Conn for tests. Inbound frames are fed via the
// inbound channel (consumed by Serve's read loop); outbound frames written by
// the Client's writeLoop land on the writes channel. WriteMessage can be made
// to block (blockWrites) to simulate a stalled/never-draining consumer.
type fakeConn struct {
	inbound chan inFrame
	writes  chan outMsg

	mu       sync.Mutex
	closed   bool
	closeCh  chan struct{}
	gate     chan struct{} // non-nil => WriteMessage blocks until receive
	writeErr error
	writeCnt int
}

type inFrame struct {
	typ  MessageType
	data []byte
	err  error
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		inbound: make(chan inFrame, 16),
		writes:  make(chan outMsg, 256),
		closeCh: make(chan struct{}),
	}
}

// newBlockingConn returns a conn whose WriteMessage blocks on gate until the
// test releases it, used to stall the writer goroutine so the outbox fills.
func newBlockingConn() *fakeConn {
	c := newFakeConn()
	c.gate = make(chan struct{})
	return c
}

func (c *fakeConn) ReadMessage() (MessageType, []byte, error) {
	select {
	case f, ok := <-c.inbound:
		if !ok {
			return 0, nil, io.EOF
		}
		return f.typ, f.data, f.err
	case <-c.closeCh:
		return 0, nil, ErrClosed
	}
}

func (c *fakeConn) WriteMessage(typ MessageType, data []byte) error {
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-c.closeCh:
			return ErrClosed
		}
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	err := c.writeErr
	c.writeCnt++
	c.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case c.writes <- outMsg{typ: typ, data: data}:
	case <-c.closeCh:
		return ErrClosed
	}
	return nil
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.closeCh)
	c.mu.Unlock()
	return nil
}

func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// feed pushes an inbound application frame for Serve to read.
func (c *fakeConn) feed(typ MessageType, data []byte) {
	c.inbound <- inFrame{typ: typ, data: data}
}

// feedErr pushes a read error (e.g. io.EOF) to trigger Serve teardown.
func (c *fakeConn) feedErr(err error) {
	c.inbound <- inFrame{err: err}
}

// nextWrite waits for the next outbound frame or fails after timeout.
func (c *fakeConn) nextWrite(t *testing.T) outMsg {
	t.Helper()
	select {
	case m := <-c.writes:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for outbound frame")
		return outMsg{}
	}
}

// expectNoWrite asserts nothing is delivered within d.
func (c *fakeConn) expectNoWrite(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case m := <-c.writes:
		t.Fatalf("unexpected outbound frame: %q", m.data)
	case <-time.After(d):
	}
}

// decode parses an outbound frame as a presence wireFrame.
func decode(t *testing.T, m outMsg) wireFrame {
	t.Helper()
	var w wireFrame
	if err := json.Unmarshal(m.data, &w); err != nil {
		t.Fatalf("decode frame %q: %v", m.data, err)
	}
	return w
}

// drainEvent collects the next frame matching one of the wanted events,
// skipping others, until timeout.
func nextEvent(t *testing.T, c *fakeConn, want string) wireFrame {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case m := <-c.writes:
			w := decode(t, m)
			if w.Event == want {
				return w
			}
		case <-deadline:
			t.Fatalf("did not receive event %q", want)
		}
	}
}

func allow() AuthorizerFunc {
	return func(AuthRequest) AuthResult { return AuthResult{Allowed: true} }
}

func deny() AuthorizerFunc {
	return func(AuthRequest) AuthResult { return AuthResult{Allowed: false} }
}

// --- tests ------------------------------------------------------------------

func TestPublicSubscribeBroadcast(t *testing.T) {
	h := NewHub()
	defer h.Close()
	conn := newFakeConn()
	c, err := h.Add(conn, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := h.Subscribe(c, "news"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if got := h.ChannelCount("news"); got != 1 {
		t.Fatalf("ChannelCount = %d, want 1", got)
	}
	if err := h.Broadcast("news", []byte("hello")); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	m := conn.nextWrite(t)
	if m.typ != TextMessage || string(m.data) != "hello" {
		t.Fatalf("got %v %q, want text \"hello\"", m.typ, m.data)
	}
}

func TestBroadcastBinary(t *testing.T) {
	h := NewHub()
	defer h.Close()
	conn := newFakeConn()
	c, _ := h.Add(conn, nil)
	_ = h.Subscribe(c, "feed")
	if err := h.BroadcastBinary("feed", []byte{0x01, 0x02}); err != nil {
		t.Fatalf("BroadcastBinary: %v", err)
	}
	m := conn.nextWrite(t)
	if m.typ != BinaryMessage || string(m.data) != "\x01\x02" {
		t.Fatalf("got %v %q, want binary", m.typ, m.data)
	}
}

func TestBroadcastUnknownChannelNoError(t *testing.T) {
	h := NewHub()
	defer h.Close()
	if err := h.Broadcast("nobody", []byte("x")); err != nil {
		t.Fatalf("Broadcast unknown channel: %v", err)
	}
}

func TestPrivateDeniedWithoutAuthorizer(t *testing.T) {
	h := NewHub()
	defer h.Close()
	c, _ := h.Add(newFakeConn(), nil)
	if err := h.Subscribe(c, "private-room"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Subscribe = %v, want ErrUnauthorized", err)
	}
	if got := h.ChannelCount("private-room"); got != 0 {
		t.Fatalf("ChannelCount = %d, want 0 after denial", got)
	}
}

func TestPresenceDeniedWithoutAuthorizer(t *testing.T) {
	h := NewHub()
	defer h.Close()
	c, _ := h.Add(newFakeConn(), nil)
	if err := h.Subscribe(c, "presence-room"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Subscribe = %v, want ErrUnauthorized", err)
	}
}

func TestPrivateAllowedWithAuthorizer(t *testing.T) {
	h := NewHub(WithAuthorizer(allow()))
	defer h.Close()
	conn := newFakeConn()
	c, _ := h.Add(conn, nil)
	if err := h.Subscribe(c, "private-room"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if got := h.ChannelCount("private-room"); got != 1 {
		t.Fatalf("ChannelCount = %d, want 1", got)
	}
	if err := h.Broadcast("private-room", []byte("secret")); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if m := conn.nextWrite(t); string(m.data) != "secret" {
		t.Fatalf("got %q, want secret", m.data)
	}
}

func TestPrivateDeniedByAuthorizer(t *testing.T) {
	h := NewHub(WithAuthorizer(deny()))
	defer h.Close()
	c, _ := h.Add(newFakeConn(), nil)
	if err := h.Subscribe(c, "private-room"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Subscribe = %v, want ErrUnauthorized", err)
	}
}

func TestAuthorizerReceivesRequest(t *testing.T) {
	var got AuthRequest
	az := AuthorizerFunc(func(r AuthRequest) AuthResult {
		got = r
		return AuthResult{Allowed: true}
	})
	h := NewHub(WithAuthorizer(az))
	defer h.Close()
	c, _ := h.Add(newFakeConn(), []byte("token-xyz"))
	if err := h.Subscribe(c, "private-room"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if got.Channel != "private-room" || got.Type != Private {
		t.Fatalf("AuthRequest channel/type = %q/%v", got.Channel, got.Type)
	}
	if got.ClientID != c.ID() {
		t.Fatalf("AuthRequest ClientID = %q, want %q", got.ClientID, c.ID())
	}
	if string(got.Auth) != "token-xyz" {
		t.Fatalf("AuthRequest Auth = %q, want token-xyz", got.Auth)
	}
}

func TestPresenceRosterAndJoinEvents(t *testing.T) {
	az := AuthorizerFunc(func(r AuthRequest) AuthResult {
		// Identity derives from the client-supplied auth blob.
		return AuthResult{Allowed: true, Member: Member{ID: string(r.Auth)}}
	})
	h := NewHub(WithAuthorizer(az))
	defer h.Close()

	var events []PresenceEvent
	var emu sync.Mutex
	h.OnPresence(func(e PresenceEvent) {
		emu.Lock()
		events = append(events, e)
		emu.Unlock()
	})

	connA := newFakeConn()
	a, _ := h.Add(connA, []byte("alice"))
	if err := h.Subscribe(a, "presence-room"); err != nil {
		t.Fatalf("subscribe a: %v", err)
	}

	// Newcomer alice gets her own subscription_succeeded (empty roster) then
	// her own member_added.
	state := nextEvent(t, connA, "presence:subscription_succeeded")
	if state.Channel != "presence-room" || len(state.Members) != 0 {
		t.Fatalf("alice roster = %+v, want empty", state.Members)
	}
	added := nextEvent(t, connA, "presence:member_added")
	if added.Member == nil || added.Member.ID != "alice" {
		t.Fatalf("alice member_added = %+v", added.Member)
	}

	connB := newFakeConn()
	b, _ := h.Add(connB, []byte("bob"))
	if err := h.Subscribe(b, "presence-room"); err != nil {
		t.Fatalf("subscribe b: %v", err)
	}

	// Bob's subscription_succeeded must contain alice already in the roster.
	bobState := nextEvent(t, connB, "presence:subscription_succeeded")
	if len(bobState.Members) != 1 || bobState.Members[0].ID != "alice" {
		t.Fatalf("bob roster = %+v, want [alice]", bobState.Members)
	}
	// Alice is notified of bob's join via member_added.
	aliceSeesBob := nextEvent(t, connA, "presence:member_added")
	if aliceSeesBob.Member == nil || aliceSeesBob.Member.ID != "bob" {
		t.Fatalf("alice should see bob added, got %+v", aliceSeesBob.Member)
	}

	// Presence() roster reflects both.
	roster := h.Presence("presence-room")
	if len(roster) != 2 {
		t.Fatalf("Presence roster len = %d, want 2", len(roster))
	}

	// Unsubscribe bob → member_removed to alice + OnPresence leave.
	h.Unsubscribe(b, "presence-room")
	removed := nextEvent(t, connA, "presence:member_removed")
	if removed.Member == nil || removed.Member.ID != "bob" {
		t.Fatalf("alice should see bob removed, got %+v", removed.Member)
	}
	if got := h.ChannelCount("presence-room"); got != 1 {
		t.Fatalf("ChannelCount = %d, want 1 after bob leaves", got)
	}

	// OnPresence fired: 2 joins (alice, bob) + 1 leave (bob).
	waitFor(t, func() bool {
		emu.Lock()
		defer emu.Unlock()
		joins, leaves := 0, 0
		for _, e := range events {
			if e.Joined {
				joins++
			} else {
				leaves++
			}
		}
		return joins == 2 && leaves == 1
	}, "OnPresence join/leave counts")
}

func TestPresenceLeaveOnDisconnect(t *testing.T) {
	az := AuthorizerFunc(func(r AuthRequest) AuthResult {
		return AuthResult{Allowed: true, Member: Member{ID: string(r.Auth)}}
	})
	h := NewHub(WithAuthorizer(az))
	defer h.Close()

	connA := newFakeConn()
	a, _ := h.Add(connA, []byte("alice"))
	_ = h.Subscribe(a, "presence-room")
	_ = nextEvent(t, connA, "presence:member_added")

	connB := newFakeConn()
	b, _ := h.Add(connB, []byte("bob"))
	_ = h.Subscribe(b, "presence-room")
	_ = nextEvent(t, connA, "presence:member_added") // bob joined

	// Disconnect bob via Close → alice gets member_removed for bob.
	if err := b.Close(); err != nil {
		t.Fatalf("close b: %v", err)
	}
	removed := nextEvent(t, connA, "presence:member_removed")
	if removed.Member == nil || removed.Member.ID != "bob" {
		t.Fatalf("got removed %+v, want bob", removed.Member)
	}
	waitFor(t, func() bool { return h.ChannelCount("presence-room") == 1 },
		"channel count back to 1 after bob disconnects")
}

func TestBroadcastExcept(t *testing.T) {
	h := NewHub()
	defer h.Close()
	connA := newFakeConn()
	connB := newFakeConn()
	a, _ := h.Add(connA, nil)
	b, _ := h.Add(connB, nil)
	_ = h.Subscribe(a, "news")
	_ = h.Subscribe(b, "news")

	if err := h.BroadcastExcept("news", []byte("hi"), a.ID()); err != nil {
		t.Fatalf("BroadcastExcept: %v", err)
	}
	// b receives, a must not.
	if m := connB.nextWrite(t); string(m.data) != "hi" {
		t.Fatalf("b got %q, want hi", m.data)
	}
	connA.expectNoWrite(t, 150*time.Millisecond)
}

func TestSendTo(t *testing.T) {
	h := NewHub()
	defer h.Close()
	conn := newFakeConn()
	c, _ := h.Add(conn, nil)
	if err := h.SendTo(c.ID(), []byte("direct")); err != nil {
		t.Fatalf("SendTo: %v", err)
	}
	if m := conn.nextWrite(t); string(m.data) != "direct" {
		t.Fatalf("got %q, want direct", m.data)
	}
	if err := h.SendTo("nope", []byte("x")); err == nil {
		t.Fatal("SendTo unknown client: want error")
	}
}

func TestCountAndChannelCount(t *testing.T) {
	h := NewHub()
	defer h.Close()
	if h.Count() != 0 {
		t.Fatalf("Count = %d, want 0", h.Count())
	}
	c1, _ := h.Add(newFakeConn(), nil)
	c2, _ := h.Add(newFakeConn(), nil)
	if h.Count() != 2 {
		t.Fatalf("Count = %d, want 2", h.Count())
	}
	_ = h.Subscribe(c1, "room")
	_ = h.Subscribe(c2, "room")
	if h.ChannelCount("room") != 2 {
		t.Fatalf("ChannelCount = %d, want 2", h.ChannelCount("room"))
	}
	if h.ChannelCount("ghost") != 0 {
		t.Fatalf("ChannelCount ghost = %d, want 0", h.ChannelCount("ghost"))
	}
	c1.Close()
	waitFor(t, func() bool { return h.Count() == 1 }, "Count drops to 1")
	waitFor(t, func() bool { return h.ChannelCount("room") == 1 }, "ChannelCount drops to 1")
	c2.Close()
	waitFor(t, func() bool { return h.ChannelCount("room") == 0 }, "channel pruned to 0")
}

func TestIdempotentSubscribe(t *testing.T) {
	h := NewHub()
	defer h.Close()
	c, _ := h.Add(newFakeConn(), nil)
	_ = h.Subscribe(c, "room")
	if err := h.Subscribe(c, "room"); err != nil {
		t.Fatalf("second Subscribe: %v", err)
	}
	if got := h.ChannelCount("room"); got != 1 {
		t.Fatalf("ChannelCount = %d, want 1 (idempotent)", got)
	}
}

func TestSlowConsumerDropMessage(t *testing.T) {
	// Small outbox + a writer stalled on a blocking conn so the outbox fills.
	h := NewHub(WithOutbox(1), WithSlowConsumerPolicy(DropMessage))
	defer h.Close()
	conn := newBlockingConn()
	c, _ := h.Add(conn, nil)
	_ = h.Subscribe(c, "feed")

	// First broadcast is pulled off the outbox by writeLoop and blocks in
	// WriteMessage (gate not released). Subsequent ones must fill the outbox
	// (capacity 1) then overflow → drop.
	flooded := false
	for i := 0; i < 200; i++ {
		_ = h.Broadcast("feed", []byte("m"))
		if c.Drops() > 0 {
			flooded = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !flooded {
		t.Fatalf("expected drops > 0, got %d", c.Drops())
	}
	if conn.isClosed() {
		t.Fatal("DropMessage policy must keep the connection open")
	}
	// Release the gate so the held write can complete and writeLoop drains.
	close(conn.gate)
}

func TestSlowConsumerDisconnectClient(t *testing.T) {
	h := NewHub(WithOutbox(1), WithSlowConsumerPolicy(DisconnectClient))
	defer h.Close()
	conn := newBlockingConn()
	c, _ := h.Add(conn, nil)
	_ = h.Subscribe(c, "feed")

	// Flood until the overflow disconnects the client.
	for i := 0; i < 500; i++ {
		_ = h.Broadcast("feed", []byte("m"))
		if conn.isClosed() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Release any blocked write so goroutines can unwind.
	close(conn.gate)
	waitFor(t, func() bool { return conn.isClosed() },
		"connection closed under DisconnectClient policy")
	waitFor(t, func() bool { return h.Count() == 0 },
		"client removed from hub after disconnect")
	if c.Drops() != 0 {
		t.Fatalf("Drops = %d under DisconnectClient, want 0", c.Drops())
	}
}

func TestCloseTearsDownClients(t *testing.T) {
	h := NewHub()
	conns := make([]*fakeConn, 3)
	for i := range conns {
		conns[i] = newFakeConn()
		c, _ := h.Add(conns[i], nil)
		_ = h.Subscribe(c, "room")
	}
	h.Close()
	for i, cn := range conns {
		idx := i
		conn := cn
		waitFor(t, func() bool { return conn.isClosed() },
			"conn "+strconv.Itoa(idx)+" closed after Hub.Close")
	}
	// Add after Close → ErrClosed.
	if _, err := h.Add(newFakeConn(), nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Add after Close = %v, want ErrClosed", err)
	}
	// Subscribe after close also errors.
	if h.Count() != 0 {
		t.Fatalf("Count after Close = %d, want 0", h.Count())
	}
}

func TestServeInvokesOnMessage(t *testing.T) {
	h := NewHub()
	defer h.Close()
	conn := newFakeConn()
	c, _ := h.Add(conn, nil)

	received := make(chan []byte, 4)
	done := make(chan struct{})
	go func() {
		c.Serve(func(_ *Client, _ MessageType, data []byte) {
			received <- append([]byte(nil), data...)
		})
		close(done)
	}()

	conn.feed(TextMessage, []byte("ping"))
	select {
	case got := <-received:
		if string(got) != "ping" {
			t.Fatalf("onMessage got %q, want ping", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onMessage not invoked")
	}

	// EOF tears down the client and ends Serve.
	conn.feedErr(io.EOF)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after EOF")
	}
	waitFor(t, func() bool { return conn.isClosed() }, "conn closed after Serve teardown")
	waitFor(t, func() bool { return h.Count() == 0 }, "client removed after Serve teardown")
}

func TestServeTeardownFiresPresenceLeave(t *testing.T) {
	az := AuthorizerFunc(func(r AuthRequest) AuthResult {
		return AuthResult{Allowed: true, Member: Member{ID: string(r.Auth)}}
	})
	h := NewHub(WithAuthorizer(az))
	defer h.Close()

	connA := newFakeConn()
	a, _ := h.Add(connA, []byte("alice"))
	_ = h.Subscribe(a, "presence-room")
	_ = nextEvent(t, connA, "presence:member_added")

	connB := newFakeConn()
	b, _ := h.Add(connB, []byte("bob"))
	go b.Serve(nil)
	_ = h.Subscribe(b, "presence-room")
	_ = nextEvent(t, connA, "presence:member_added") // bob joined

	// Drive Serve teardown by signalling read error → presence leave for bob.
	connB.feedErr(io.EOF)
	removed := nextEvent(t, connA, "presence:member_removed")
	if removed.Member == nil || removed.Member.ID != "bob" {
		t.Fatalf("got %+v, want bob removed", removed.Member)
	}
}

func TestWriteErrorClosesClient(t *testing.T) {
	h := NewHub()
	defer h.Close()
	conn := newFakeConn()
	conn.mu.Lock()
	conn.writeErr = errors.New("boom")
	conn.mu.Unlock()
	c, _ := h.Add(conn, nil)
	_ = h.Subscribe(c, "room")
	// Broadcast triggers a write that errors → writeLoop closes the client.
	_ = h.Broadcast("room", []byte("x"))
	waitFor(t, func() bool { return conn.isClosed() }, "conn closed after write error")
	waitFor(t, func() bool { return h.Count() == 0 }, "client removed after write error")
}

func TestConcurrentBroadcastSubscribe(t *testing.T) {
	h := NewHub(WithOutbox(8))
	defer h.Close()

	const nClients = 12
	conns := make([]*fakeConn, nClients)
	clients := make([]*Client, nClients)
	for i := range conns {
		conns[i] = newFakeConn()
		clients[i], _ = h.Add(conns[i], nil)
		// Drain writes so writeLoop never blocks and clients stay live.
		go func(cn *fakeConn) {
			for {
				select {
				case <-cn.writes:
				case <-cn.closeCh:
					return
				}
			}
		}(conns[i])
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Broadcasters.
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = h.Broadcast("room", []byte("tick"))
					_ = h.BroadcastExcept("room", []byte("tock"), clients[0].ID())
				}
			}
		}()
	}

	// Subscribe / unsubscribe churners.
	for i := 0; i < nClients; i++ {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = h.Subscribe(c, "room")
					h.Unsubscribe(c, "room")
				}
			}
		}(clients[i])
	}

	// Readers hammering Count/ChannelCount/Presence.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = h.Count()
				_ = h.ChannelCount("room")
				_ = h.Presence("presence-x")
			}
		}
	}()

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestClassifyChannelAndString(t *testing.T) {
	cases := []struct {
		name string
		typ  ChannelType
		str  string
	}{
		{"news", Public, "public"},
		{"private-room", Private, "private"},
		{"presence-room", Presence, "presence"},
		{"presence", Public, "public"}, // prefix requires the trailing dash
	}
	for _, c := range cases {
		if got := ClassifyChannel(c.name); got != c.typ {
			t.Fatalf("ClassifyChannel(%q) = %v, want %v", c.name, got, c.typ)
		}
		if got := c.typ.String(); got != c.str {
			t.Fatalf("%v.String() = %q, want %q", c.typ, got, c.str)
		}
	}
	// Out-of-range value falls through to the default branch.
	if got := ChannelType(99).String(); got != "public" {
		t.Fatalf("unknown ChannelType.String() = %q, want public", got)
	}
}

func TestWithLoggerInvokedOnDenial(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	h := NewHub(WithLogger(func(format string, _ ...any) {
		mu.Lock()
		msgs = append(msgs, format)
		mu.Unlock()
	}))
	defer h.Close()

	c, _ := h.Add(newFakeConn(), nil)
	// No authorizer → private join denied → logger fires.
	if err := h.Subscribe(c, "private-x"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Subscribe = %v, want ErrUnauthorized", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(msgs) == 0 {
		t.Fatal("WithLogger callback was not invoked on denial")
	}
}

func TestWithOutboxRejectsNonPositive(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("WithOutbox(0) should panic")
		}
	}()
	WithOutbox(0)
}

func TestPresenceMemberInfoEncoding(t *testing.T) {
	az := AuthorizerFunc(func(r AuthRequest) AuthResult {
		// Valid-JSON Info is passed through verbatim; non-JSON is string-encoded.
		return AuthResult{Allowed: true, Member: Member{
			ID:   "alice",
			Info: []byte(`{"name":"Ada"}`),
		}}
	})
	h := NewHub(WithAuthorizer(az))
	defer h.Close()

	conn := newFakeConn()
	a, _ := h.Add(conn, nil)
	if err := h.Subscribe(a, "presence-room"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	added := nextEvent(t, conn, "presence:member_added")
	if added.Member == nil || string(added.Member.Info) != `{"name":"Ada"}` {
		t.Fatalf("member info = %s, want raw JSON object", added.Member.Info)
	}
}

func TestPresenceMemberInfoNonJSON(t *testing.T) {
	az := AuthorizerFunc(func(r AuthRequest) AuthResult {
		return AuthResult{Allowed: true, Member: Member{ID: "bob", Info: []byte("plain text")}}
	})
	h := NewHub(WithAuthorizer(az))
	defer h.Close()

	conn := newFakeConn()
	b, _ := h.Add(conn, nil)
	if err := h.Subscribe(b, "presence-room"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	added := nextEvent(t, conn, "presence:member_added")
	// Non-JSON Info is encoded as a JSON string literal.
	if added.Member == nil || string(added.Member.Info) != `"plain text"` {
		t.Fatalf("member info = %s, want quoted JSON string", added.Member.Info)
	}
}

func TestRawInfoEmpty(t *testing.T) {
	if got := rawInfo(nil); got != nil {
		t.Fatalf("rawInfo(nil) = %s, want nil", got)
	}
	if got := rawInfo([]byte{}); got != nil {
		t.Fatalf("rawInfo(empty) = %s, want nil", got)
	}
}

func TestUnsubscribeUnknownChannelNoop(t *testing.T) {
	h := NewHub()
	defer h.Close()
	c, _ := h.Add(newFakeConn(), nil)
	// Unsubscribe from a channel that doesn't exist, and from one the client
	// never joined — both must be silent no-ops.
	h.Unsubscribe(c, "ghost")
	_ = h.Subscribe(c, "room")
	c2, _ := h.Add(newFakeConn(), nil)
	h.Unsubscribe(c2, "room") // c2 is not a member
	if got := h.ChannelCount("room"); got != 1 {
		t.Fatalf("ChannelCount = %d, want 1 (no-op unsubscribes)", got)
	}
}

func TestSubscribeAfterCloseErrClosed(t *testing.T) {
	h := NewHub()
	c, _ := h.Add(newFakeConn(), nil)
	h.Close()
	if err := h.Subscribe(c, "room"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Subscribe after Close = %v, want ErrClosed", err)
	}
}

func TestBroadcastAfterCloseErrClosed(t *testing.T) {
	h := NewHub()
	h.Close()
	if err := h.Broadcast("room", []byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Broadcast after Close = %v, want ErrClosed", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	h := NewHub()
	h.Add(newFakeConn(), nil)
	h.Close()
	h.Close() // second Close must be a no-op, not panic
}

// waitFor polls cond until true or fails after a generous timeout. Used in
// place of fixed sleeps for state that settles asynchronously.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met: %s", what)
}
