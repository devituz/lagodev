package realtime

import (
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stressConn is a self-draining in-memory Conn for the stress tests. Unlike
// the deterministic fakeConn used elsewhere, it does not buffer outbound
// frames for inspection: WriteMessage either drains immediately (healthy
// consumer) or blocks until Close (slow consumer), and it counts frames that
// reached the wire. This lets the stress tests run millions of broadcasts
// without unbounded memory growth in the test harness itself.
type stressConn struct {
	slow    bool          // never drains: WriteMessage blocks until Close
	release chan struct{} // closed by Close to unblock a stalled writer

	mu     sync.Mutex
	closed bool

	writes uint64 // frames that reached the wire
}

func newStressConn(slow bool) *stressConn {
	return &stressConn{slow: slow, release: make(chan struct{})}
}

// ReadMessage parks until Close: the stress clients are send-only. A slow or
// healthy consumer alike never produces inbound frames; teardown comes from
// the test driving Hub.Close / Client.Close, which closes release.
func (c *stressConn) ReadMessage() (MessageType, []byte, error) {
	<-c.release
	return 0, nil, ErrClosed
}

func (c *stressConn) WriteMessage(_ MessageType, _ []byte) error {
	if c.slow {
		// Simulate a consumer that never drains: park the writer goroutine
		// until the client is torn down. This is exactly the condition that
		// fills the bounded outbox and exercises the slow-consumer policy.
		<-c.release
		return ErrClosed
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	atomic.AddUint64(&c.writes, 1)
	return nil
}

func (c *stressConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.release)
	c.mu.Unlock()
	return nil
}

// settleGoroutines waits for the live goroutine count to drop to at most
// baseline+slack, polling until deadline. It returns the final count. Used to
// assert no goroutine leak after teardown without flaking on scheduler lag.
func settleGoroutines(baseline, slack int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for {
		runtime.GC()
		n := runtime.NumGoroutine()
		if n <= baseline+slack || time.Now().After(deadline) {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stressScale returns the (clients, channels, broadcasts) the stress test
// exercises, shrunk under -short so CI stays fast but the full path runs by
// default.
func stressScale(t *testing.T) (clients, channels, broadcasts int) {
	if testing.Short() {
		return 200, 16, 50
	}
	return 4000, 128, 200
}

// TestStressBroadcastStormPresenceChurn spins up several thousand healthy
// clients spread across many channels, hammers them with broadcast storms
// while presence rosters churn (rapid join/leave), then asserts a clean
// shutdown: every writer goroutine exits and the client/channel maps drain.
func TestStressBroadcastStormPresenceChurn(t *testing.T) {
	clients, channels, broadcasts := stressScale(t)

	base := settleGoroutines(0, 0, time.Second) // quiesce before measuring
	base = runtime.NumGoroutine()

	var presenceEvents uint64
	h := NewHub(
		WithAuthorizer(allow()),
		WithOutbox(32),
		WithSlowConsumerPolicy(DropMessage),
	)
	h.OnPresence(func(PresenceEvent) { atomic.AddUint64(&presenceEvents, 1) })

	conns := make([]*stressConn, clients)
	cs := make([]*Client, clients)
	for i := 0; i < clients; i++ {
		conns[i] = newStressConn(false)
		c, err := h.Add(conns[i], []byte(strconv.Itoa(i)))
		if err != nil {
			t.Fatalf("Add[%d]: %v", i, err)
		}
		cs[i] = c
	}

	// Subscribe every client to a public, a private and a presence channel,
	// spread across the channel space.
	chanName := func(prefix string, n int) string { return prefix + strconv.Itoa(n%channels) }
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := cs[i]
			_ = h.Subscribe(c, chanName("news-", i))
			_ = h.Subscribe(c, chanName("private-room-", i))
			_ = h.Subscribe(c, chanName("presence-room-", i))
		}(i)
	}
	wg.Wait()

	// Broadcast storm across every channel concurrently, while a churn
	// goroutine rapidly leaves and rejoins presence channels.
	var churnStop int32
	var churnWG sync.WaitGroup
	churnWG.Add(1)
	go func() {
		defer churnWG.Done()
		for atomic.LoadInt32(&churnStop) == 0 {
			for i := 0; i < clients; i += 7 {
				name := chanName("presence-room-", i)
				h.Unsubscribe(cs[i], name)
				_ = h.Subscribe(cs[i], name)
			}
		}
	}()

	var bwg sync.WaitGroup
	for b := 0; b < broadcasts; b++ {
		bwg.Add(1)
		go func(b int) {
			defer bwg.Done()
			payload := []byte("storm-" + strconv.Itoa(b))
			for ch := 0; ch < channels; ch++ {
				_ = h.Broadcast("news-"+strconv.Itoa(ch), payload)
				_ = h.Broadcast("private-room-"+strconv.Itoa(ch), payload)
				_ = h.Broadcast("presence-room-"+strconv.Itoa(ch), payload)
			}
		}(b)
	}
	bwg.Wait()

	atomic.StoreInt32(&churnStop, 1)
	churnWG.Wait()

	if atomic.LoadUint64(&presenceEvents) == 0 {
		t.Fatal("expected presence events during churn, got 0")
	}

	// Clean shutdown: Close must reject further Add and tear down every client.
	h.Close()
	if _, err := h.Add(newStressConn(false), nil); err != ErrClosed {
		t.Fatalf("Add after Close: err = %v, want ErrClosed", err)
	}
	if got := h.Count(); got != 0 {
		t.Fatalf("Count after Close = %d, want 0", got)
	}

	// No goroutine leak: every writeLoop must have exited. Allow a small slack
	// for runtime/test bookkeeping goroutines.
	final := settleGoroutines(base, 4, 10*time.Second)
	if final > base+4 {
		t.Fatalf("goroutine leak: baseline %d, final %d (delta %d)", base, final, final-base)
	}
	t.Logf("scale: clients=%d channels=%d broadcasts=%d; goroutines base=%d final=%d presenceEvents=%d",
		clients, channels, broadcasts, base, final, atomic.LoadUint64(&presenceEvents))
}

// TestStressSlowConsumerDrop drives never-draining clients under the
// DropMessage policy: the outbox must stay bounded, drops must be counted, and
// nothing may block the broadcasting goroutines. Verifies the outbox is a hard
// cap (drops grow under sustained overflow) and shutdown is clean.
func TestStressSlowConsumerDrop(t *testing.T) {
	clients, channels, broadcasts := stressScale(t)
	const outbox = 16

	base := settleGoroutines(0, 0, time.Second)
	base = runtime.NumGoroutine()

	h := NewHub(WithOutbox(outbox), WithSlowConsumerPolicy(DropMessage))

	cs := make([]*Client, clients)
	for i := 0; i < clients; i++ {
		c, err := h.Add(newStressConn(true), nil) // slow: never drains
		if err != nil {
			t.Fatalf("Add[%d]: %v", i, err)
		}
		cs[i] = c
		_ = h.Subscribe(c, "news-"+strconv.Itoa(i%channels))
	}

	// Storm: every broadcast to a stalled client overflows after the outbox
	// fills. Must never block the broadcaster.
	done := make(chan struct{})
	go func() {
		for b := 0; b < broadcasts; b++ {
			for ch := 0; ch < channels; ch++ {
				_ = h.Broadcast("news-"+strconv.Itoa(ch), []byte("x"))
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("broadcast storm blocked: slow consumer stalled the broadcaster")
	}

	// Outbox is bounded: each client buffered at most `outbox` frames; the
	// rest were dropped and counted. With a sustained storm well over the
	// buffer, total drops must be non-zero.
	var totalDrops uint64
	for _, c := range cs {
		totalDrops += c.Drops()
	}
	if totalDrops == 0 {
		t.Fatal("expected dropped frames under DropMessage with stalled consumers, got 0")
	}

	h.Close()
	final := settleGoroutines(base, 4, 10*time.Second)
	if final > base+4 {
		t.Fatalf("goroutine leak: baseline %d, final %d (delta %d)", base, final, final-base)
	}
	t.Logf("scale: clients=%d channels=%d broadcasts=%d; drops=%d goroutines base=%d final=%d",
		clients, channels, broadcasts, totalDrops, base, final)
}

// TestStressSlowConsumerDisconnect drives never-draining clients under the
// DisconnectClient policy under a broadcast storm. This is the case that, with
// an unguarded `go c.Close()` per overflow, floods the scheduler with
// transient teardown goroutines. The test asserts the broadcaster never
// blocks, every client is disconnected and removed, and goroutines settle —
// i.e. the per-client teardown is launched at most once.
func TestStressSlowConsumerDisconnect(t *testing.T) {
	clients, channels, broadcasts := stressScale(t)

	base := settleGoroutines(0, 0, time.Second)
	base = runtime.NumGoroutine()

	h := NewHub(WithOutbox(8), WithSlowConsumerPolicy(DisconnectClient))

	for i := 0; i < clients; i++ {
		c, err := h.Add(newStressConn(true), nil) // slow: never drains
		if err != nil {
			t.Fatalf("Add[%d]: %v", i, err)
		}
		// Each client owns a read loop too, so we exercise Serve teardown.
		go c.Serve(nil)
		_ = h.Subscribe(c, "news-"+strconv.Itoa(i%channels))
	}

	done := make(chan struct{})
	go func() {
		for b := 0; b < broadcasts; b++ {
			for ch := 0; ch < channels; ch++ {
				_ = h.Broadcast("news-"+strconv.Itoa(ch), []byte("x"))
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("broadcast storm blocked under DisconnectClient")
	}

	// Slow clients overflow on first storm frame and self-disconnect. Wait for
	// the client table to drain — no client/channel map entry may linger.
	deadline := time.Now().Add(15 * time.Second)
	for h.Count() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := h.Count(); got != 0 {
		t.Fatalf("Count after disconnect storm = %d, want 0 (clients not removed)", got)
	}
	if got := h.ChannelCount("news-0"); got != 0 {
		t.Fatalf("ChannelCount after disconnect = %d, want 0 (channel not cleaned)", got)
	}

	h.Close()
	final := settleGoroutines(base, 4, 10*time.Second)
	if final > base+4 {
		t.Fatalf("goroutine leak: baseline %d, final %d (delta %d)", base, final, final-base)
	}
	t.Logf("scale: clients=%d channels=%d broadcasts=%d; goroutines base=%d final=%d",
		clients, channels, broadcasts, base, final)
}
