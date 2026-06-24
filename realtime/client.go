package realtime

import (
	"encoding/json"
	"sync"
	"sync/atomic"
)

// outMsg is one queued outbound frame waiting in a Client's outbox.
type outMsg struct {
	typ  MessageType
	data []byte
}

// Client is a Conn registered on a Hub. It owns a single writer goroutine
// draining a bounded outbox; the caller owns the reader via Serve. A Client
// is safe for concurrent use: enqueue/Close may be called from any goroutine.
type Client struct {
	id   string
	hub  *Hub
	conn Conn
	auth []byte

	outbox chan outMsg
	done   chan struct{}

	mu       sync.Mutex
	channels map[string]struct{}

	closeOnce sync.Once
	drops     uint64
}

// ID returns the Hub-assigned client identifier.
func (c *Client) ID() string { return c.id }

// Drops returns the number of messages dropped for this client under the
// DropMessage slow-consumer policy.
func (c *Client) Drops() uint64 { return atomic.LoadUint64(&c.drops) }

// Serve runs the inbound read loop, invoking onMessage for every frame until
// the peer closes or the connection errors. It blocks, so callers typically
// run it in a goroutine. Serve always tears the client down on return, firing
// presence-leave events for every joined channel. onMessage may be nil to
// ignore inbound frames (a send-only client).
func (c *Client) Serve(onMessage func(*Client, MessageType, []byte)) {
	defer c.Close()
	for {
		typ, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if onMessage != nil {
			onMessage(c, typ, data)
		}
	}
}

// writeLoop drains the outbox onto the wire. A write error closes the client.
func (c *Client) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case m := <-c.outbox:
			if err := c.conn.WriteMessage(m.typ, m.data); err != nil {
				go c.Close()
				return
			}
		}
	}
}

// enqueue queues a text frame for delivery.
func (c *Client) enqueue(data []byte) { c.enqueueMsg(outMsg{typ: TextMessage, data: data}) }

// enqueueMsg queues a frame subject to the Hub's slow-consumer policy. It
// never blocks the caller (a broadcasting goroutine must not stall on one
// slow client).
func (c *Client) enqueueMsg(m outMsg) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.outbox <- m:
	case <-c.done:
	default:
		switch c.hub.slowPolicy {
		case DisconnectClient:
			c.hub.logger("realtime: disconnecting slow client %s (outbox full)", c.id)
			go c.Close()
		default:
			atomic.AddUint64(&c.drops, 1)
			c.hub.logger("realtime: dropping frame to slow client %s (outbox full)", c.id)
		}
	}
}

// Close tears the client out of its Hub and closes the transport. It is
// idempotent and safe to call concurrently.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		c.hub.remove(c)
		c.conn.Close()
	})
	return nil
}

// ---- presence wire frames --------------------------------------------------

type presenceMember struct {
	ID   string          `json:"id"`
	Info json.RawMessage `json:"info,omitempty"`
}

type wireFrame struct {
	Event   string           `json:"event"`
	Channel string           `json:"channel"`
	Member  *presenceMember  `json:"member,omitempty"`
	Members []presenceMember `json:"members,omitempty"`
}

// rawInfo renders a member's opaque Info as JSON: passed through verbatim when
// already valid JSON, otherwise encoded as a JSON string.
func rawInfo(info []byte) json.RawMessage {
	if len(info) == 0 {
		return nil
	}
	if json.Valid(info) {
		return json.RawMessage(info)
	}
	b, _ := json.Marshal(string(info))
	return json.RawMessage(b)
}

func presenceState(channel string, roster []Member) []byte {
	members := make([]presenceMember, 0, len(roster))
	for _, m := range roster {
		members = append(members, presenceMember{ID: m.ID, Info: rawInfo(m.Info)})
	}
	b, _ := json.Marshal(wireFrame{
		Event:   "presence:subscription_succeeded",
		Channel: channel,
		Members: members,
	})
	return b
}

func presenceJoin(channel string, m Member) []byte {
	b, _ := json.Marshal(wireFrame{
		Event:   "presence:member_added",
		Channel: channel,
		Member:  &presenceMember{ID: m.ID, Info: rawInfo(m.Info)},
	})
	return b
}

func presenceLeave(channel string, m Member) []byte {
	b, _ := json.Marshal(wireFrame{
		Event:   "presence:member_removed",
		Channel: channel,
		Member:  &presenceMember{ID: m.ID, Info: rawInfo(m.Info)},
	})
	return b
}
