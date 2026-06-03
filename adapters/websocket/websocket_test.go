package websocket

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ws "github.com/coder/websocket"
)

// dialTestServer opens a Hub-handler server and returns a client conn
// plus the Hub so tests can broadcast / inspect state.
func dialTestServer(t *testing.T, onMsg MessageHandler, opts ...HubOption) (*Hub, *ws.Conn, func()) {
	t.Helper()
	hub := NewHub(opts...)
	srv := httptest.NewServer(hub.Handler(onMsg))
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cli, _, err := ws.Dial(ctx, url, nil)
	if err != nil {
		srv.Close()
		t.Fatalf("Dial: %v", err)
	}
	cleanup := func() {
		_ = cli.Close(ws.StatusNormalClosure, "")
		srv.Close()
		hub.Close()
	}
	return hub, cli, cleanup
}

func TestHandshakeRegistersConnection(t *testing.T) {
	hub, cli, cleanup := dialTestServer(t, nil)
	defer cleanup()
	defer cli.Close(ws.StatusNormalClosure, "")
	// give Accept goroutine a beat to register
	waitFor(t, func() bool { return hub.Count() == 1 })
}

func TestBroadcastDeliversToJoinedChannel(t *testing.T) {
	got := make(chan string, 1)
	hub, cli, cleanup := dialTestServer(t, func(ctx context.Context, c *Connection, _ []byte) error {
		c.Join("chat.1")
		return nil
	})
	defer cleanup()

	// poke server with a frame so it Joins the channel
	if err := cli.Write(context.Background(), ws.MessageText, []byte("join")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	waitFor(t, func() bool { return hub.ChannelCount("chat.1") == 1 })

	if err := hub.Broadcast(context.Background(), "chat.1", []byte("hello")); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, data, err := cli.Read(ctx)
		if err != nil {
			got <- "ERR:" + err.Error()
			return
		}
		got <- string(data)
	}()
	select {
	case msg := <-got:
		if msg != "hello" {
			t.Fatalf("client got %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not receive broadcast")
	}
}

func TestBroadcast_OnlyToJoinedChannel(t *testing.T) {
	// Two clients; only client A joins.
	hub := NewHub()
	defer hub.Close()
	srv := httptest.NewServer(hub.Handler(func(ctx context.Context, c *Connection, m []byte) error {
		if string(m) == "join" {
			c.Join("room")
		}
		return nil
	}))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	dial := func() *ws.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c, _, err := ws.Dial(ctx, url, nil)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		return c
	}
	a := dial()
	b := dial()
	defer a.Close(ws.StatusNormalClosure, "")
	defer b.Close(ws.StatusNormalClosure, "")

	if err := a.Write(context.Background(), ws.MessageText, []byte("join")); err != nil {
		t.Fatalf("a join: %v", err)
	}
	waitFor(t, func() bool { return hub.ChannelCount("room") == 1 })

	_ = hub.Broadcast(context.Background(), "room", []byte("hi"))

	// A should receive; B should NOT.
	aGot := readWithTimeout(a, 500*time.Millisecond)
	bGot := readWithTimeout(b, 200*time.Millisecond)
	if aGot != "hi" {
		t.Fatalf("a got %q", aGot)
	}
	if bGot != "" {
		t.Fatalf("b should not receive; got %q", bGot)
	}
}

func TestLeave_StopsDelivery(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	srv := httptest.NewServer(hub.Handler(func(ctx context.Context, c *Connection, m []byte) error {
		switch string(m) {
		case "join":
			c.Join("room")
		case "leave":
			c.Leave("room")
		}
		return nil
	}))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cli, _, err := ws.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close(ws.StatusNormalClosure, "")

	_ = cli.Write(context.Background(), ws.MessageText, []byte("join"))
	waitFor(t, func() bool { return hub.ChannelCount("room") == 1 })

	_ = cli.Write(context.Background(), ws.MessageText, []byte("leave"))
	waitFor(t, func() bool { return hub.ChannelCount("room") == 0 })
}

func TestSendTo_AddressesSingleConnection(t *testing.T) {
	hub, cli, cleanup := dialTestServer(t, nil)
	defer cleanup()
	waitFor(t, func() bool { return hub.Count() == 1 })

	var connID string
	hub.mu.RLock()
	for id := range hub.conns {
		connID = id
		break
	}
	hub.mu.RUnlock()

	if err := hub.SendTo(connID, []byte("hi")); err != nil {
		t.Fatalf("SendTo: %v", err)
	}
	if got := readWithTimeout(cli, time.Second); got != "hi" {
		t.Fatalf("SendTo client got %q", got)
	}
}

func TestSendTo_UnknownIDErrors(t *testing.T) {
	h := NewHub()
	defer h.Close()
	if err := h.SendTo("does-not-exist", []byte("x")); err == nil {
		t.Fatal("must error on unknown id")
	}
}

func TestDropped_OnFullOutbox(t *testing.T) {
	hub := NewHub(WithOutbox(1))
	defer hub.Close()
	srv := httptest.NewServer(hub.Handler(func(ctx context.Context, c *Connection, _ []byte) error {
		c.Join("ch")
		return nil
	}))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	// Block client from reading by never draining; the server's writer
	// will fill the outbox and then drop subsequent frames.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cli, _, err := ws.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close(ws.StatusNormalClosure, "")
	_ = cli.Write(context.Background(), ws.MessageText, []byte("join"))
	waitFor(t, func() bool { return hub.ChannelCount("ch") == 1 })

	// Burst-broadcast — first lands, subsequent ones may be dropped
	// while the single-slot outbox is busy.
	for i := 0; i < 200; i++ {
		_ = hub.Broadcast(context.Background(), "ch", []byte("x"))
	}
	// Give it a tiny moment for the writer goroutine to back-pressure.
	time.Sleep(100 * time.Millisecond)

	var totalDropped uint64
	hub.mu.RLock()
	for _, c := range hub.conns {
		totalDropped += c.Dropped()
	}
	hub.mu.RUnlock()
	if totalDropped == 0 {
		t.Fatalf("expected some dropped messages, got %d", totalDropped)
	}
}

func TestHubClose_TerminatesConnections(t *testing.T) {
	hub, cli, _ := dialTestServer(t, nil)
	defer cli.Close(ws.StatusNormalClosure, "")
	waitFor(t, func() bool { return hub.Count() == 1 })

	hub.Close()
	// Client should now hit EOF.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := cli.Read(ctx); err == nil {
		t.Fatal("client read must fail after hub close")
	}
}

func TestHandlerAfterClose_503(t *testing.T) {
	hub := NewHub()
	hub.Close()
	srv := httptest.NewServer(hub.Handler(nil))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, resp, err := ws.Dial(ctx, url, nil)
	if err == nil {
		t.Fatal("Dial against closed hub must fail")
	}
	if resp != nil && resp.StatusCode != 503 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// --- helpers ------------------------------------------------------------

// waitFor polls cond every 5ms up to 2s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never satisfied")
}

func readWithTimeout(c *ws.Conn, d time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		return ""
	}
	return string(data)
}

// Suppress "imported and not used" complaints when only Hub/Connection
// types are used by callers in production code.
var (
	_ = sync.RWMutex{}
	_ = atomic.AddUint64
)
