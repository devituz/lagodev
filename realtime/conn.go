// Package realtime is a high-level realtime gateway for lagodev,
// modelled on Laravel Echo / NestJS gateways. It layers channels,
// presence and per-channel authorization on top of a transport-agnostic
// connection interface.
//
// The package is a dependency-free leaf: it imports only the standard
// library. A WebSocket transport (RFC 6455 server handshake + framing)
// ships in-box (ws.go / upgrader.go), but the Hub is written against the
// Conn interface so it is fully testable without real sockets.
//
// Layers:
//
//   - Conn       — transport abstraction: ReadMessage / WriteMessage /
//     Close. Implemented by wsConn (stdlib WebSocket) and by
//     the in-memory fake used in tests.
//   - Client     — a Conn registered on a Hub, with a bounded outbound
//     buffer and slow-consumer handling.
//   - Channel    — a named room. Public, Private (authorized) or
//     Presence (authorized + membership roster with
//     join/leave events).
//   - Hub        — the per-app singleton tracking clients and channels;
//     subscribe / unsubscribe / broadcast / direct send /
//     presence.
//   - Authorizer — hook deciding whether a client may join a private or
//     presence channel.
//
// Broadcasting bridge: realtime does not import the broadcasting package
// (avoids a dependency cycle and keeps this a leaf). Instead, callers
// wire events in by passing the Hub's Broadcast method to a
// broadcasting subscription. See Hub.Broadcast and the package example
// in doc.go.
package realtime

import (
	"errors"
	"io"
)

// MessageType distinguishes a UTF-8 text frame from a binary frame.
type MessageType int

const (
	// TextMessage carries UTF-8 text (WebSocket opcode 0x1).
	TextMessage MessageType = 1
	// BinaryMessage carries opaque bytes (WebSocket opcode 0x2).
	BinaryMessage MessageType = 2
)

// ErrClosed is returned by Conn operations after the connection — or the
// owning Hub — has been closed.
var ErrClosed = errors.New("realtime: closed")

// Conn is the transport abstraction the Hub is built against. A single
// reader goroutine and a single writer goroutine touch a Conn, so
// implementations need not make ReadMessage concurrent with itself nor
// WriteMessage concurrent with itself; Close must be safe to call
// concurrently with both.
//
// ReadMessage returns the next inbound application message. It returns
// io.EOF (or a wrapped transport error) once the peer closes.
//
// WriteMessage delivers one application message to the peer.
//
// Close terminates the connection; it is idempotent.
type Conn interface {
	ReadMessage() (MessageType, []byte, error)
	WriteMessage(MessageType, []byte) error
	Close() error
}

// isClosed reports whether err signals a normal end-of-connection rather
// than an unexpected failure.
func isClosed(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}
