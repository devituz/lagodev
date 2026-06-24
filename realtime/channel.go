package realtime

import "strings"

// ChannelType classifies a channel's authorization and presence
// semantics, following Laravel Echo's channel naming convention.
type ChannelType int

const (
	// Public channels require no authorization; any client may join.
	Public ChannelType = iota
	// Private channels require the Authorizer to approve the join.
	Private
	// Presence channels require authorization and additionally maintain
	// a roster broadcast to members via join/leave events.
	Presence
)

func (t ChannelType) String() string {
	switch t {
	case Private:
		return "private"
	case Presence:
		return "presence"
	default:
		return "public"
	}
}

// ClassifyChannel derives a channel's type from its name prefix, mirroring
// Laravel Echo: "private-*" is Private, "presence-*" is Presence, anything
// else is Public.
func ClassifyChannel(name string) ChannelType {
	switch {
	case strings.HasPrefix(name, "presence-"):
		return Presence
	case strings.HasPrefix(name, "private-"):
		return Private
	default:
		return Public
	}
}

// Member is one participant's public identity inside a presence channel.
// ID must be stable per user; Info is an opaque payload (typically JSON)
// surfaced to other members in presence events.
type Member struct {
	ID   string
	Info []byte
}

// AuthRequest is handed to the Authorizer when a client attempts to join a
// private or presence channel.
type AuthRequest struct {
	// Channel is the target channel name.
	Channel string
	// Type is the channel's classification.
	Type ChannelType
	// ClientID is the Hub-assigned id of the joining client.
	ClientID string
	// Auth carries client-supplied credentials (e.g. a signed token)
	// extracted at connect time; opaque to the Hub.
	Auth []byte
}

// AuthResult is returned by an Authorizer. Allowed gates the join. For
// presence channels, Member supplies the identity published to the roster.
type AuthResult struct {
	Allowed bool
	Member  Member
}

// Authorizer decides whether a client may join a private or presence
// channel. It is never consulted for public channels. A nil Authorizer on
// the Hub denies every private/presence join.
type Authorizer interface {
	Authorize(AuthRequest) AuthResult
}

// AuthorizerFunc adapts a plain function to the Authorizer interface.
type AuthorizerFunc func(AuthRequest) AuthResult

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(r AuthRequest) AuthResult { return f(r) }

// channel is the Hub's internal per-room state.
type channel struct {
	name string
	typ  ChannelType
	// members maps clientID → client for every subscriber.
	members map[string]*Client
	// roster maps clientID → presence identity (presence channels only).
	roster map[string]Member
}

func newChannel(name string) *channel {
	return &channel{
		name:    name,
		typ:     ClassifyChannel(name),
		members: make(map[string]*Client),
		roster:  make(map[string]Member),
	}
}
