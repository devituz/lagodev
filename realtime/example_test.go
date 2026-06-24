package realtime_test

import (
	"fmt"

	"github.com/devituz/lagodev/realtime"
)

// pipeConn is a minimal in-memory realtime.Conn. Outbound frames written by
// the Hub's writer goroutine land on the buffered writes channel, which the
// example drains synchronously to observe exactly what reached the wire.
type pipeConn struct {
	writes chan string
}

func newPipeConn() *pipeConn {
	return &pipeConn{writes: make(chan string, 8)}
}

// ReadMessage blocks forever: this example is send-only, so the peer never
// produces inbound frames. The Hub drives writes; reads are irrelevant here.
func (c *pipeConn) ReadMessage() (realtime.MessageType, []byte, error) {
	select {} // park; the example never starts a read loop
}

func (c *pipeConn) WriteMessage(_ realtime.MessageType, data []byte) error {
	c.writes <- string(data)
	return nil
}

func (c *pipeConn) Close() error { return nil }

// Example shows the core realtime flow a server performs per connection:
// register the Conn on the Hub, subscribe it to a public channel, broadcast a
// message, and read the exact frame the client received.
func Example() {
	hub := realtime.NewHub()
	defer hub.Close()

	conn := newPipeConn()
	client, err := hub.Add(conn, nil)
	if err != nil {
		fmt.Println("add:", err)
		return
	}

	if err := hub.Subscribe(client, "orders"); err != nil {
		fmt.Println("subscribe:", err)
		return
	}

	if err := hub.Broadcast("orders", []byte(`{"event":"created","id":42}`)); err != nil {
		fmt.Println("broadcast:", err)
		return
	}

	// The writer goroutine drains the outbox onto the Conn; receiving the
	// frame here makes the example deterministic without any sleeps.
	fmt.Println("subscribers:", hub.ChannelCount("orders"))
	fmt.Println("frame:", <-conn.writes)

	// Output:
	// subscribers: 1
	// frame: {"event":"created","id":42}
}
