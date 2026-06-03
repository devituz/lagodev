// Package notifications provides multi-channel notification delivery
// modelled on Laravel's Notification facade.
//
// A Notification describes what to send (subject, body, etc.) and
// which channels it should go through. A Notifiable is the recipient
// (typically a User struct) and knows its own routing per channel
// (email address, phone number, push token, …).
//
// The Manager dispatches a notification to every requested channel,
// collecting errors via errors.Join so partial failure is surfaced
// without aborting the rest.
//
// Channels shipped in-box:
//   - MailChannel — adapts a mail.Mailer (this module's mail package).
//
// Add custom channels (SMS, Slack, push) by implementing Channel.
package notifications

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/devituz/lagodev/mail"
)

// Notification is the message to be sent. Implementations expose the
// channels they support and produce a per-channel payload.
//
// Channels names are arbitrary strings ("mail", "sms", "slack",
// "database", …). The Manager routes by name; only registered
// channels receive a delivery attempt.
type Notification interface {
	// Channels returns the channel names this notification targets.
	Channels() []string
}

// MailNotification is the contract a Notification must satisfy to be
// dispatched through MailChannel.
type MailNotification interface {
	// ToMail builds the mail.Message for the given recipient address.
	ToMail(recipient string) mail.Message
}

// Notifiable is a recipient of notifications. Implementations expose a
// per-channel routing address: an email for "mail", a phone for "sms",
// etc.
type Notifiable interface {
	// RouteFor returns the address to use on the named channel.
	// Returning "" means "skip this channel for this user".
	RouteFor(channel string) string
}

// Channel is the transport interface. Implementations should validate
// that they can handle the notification (typically via a type
// assertion to the channel-specific interface).
type Channel interface {
	// Name returns the channel identifier ("mail", "sms", …).
	Name() string
	// Send delivers n to the recipient address returned by Notifiable.
	Send(ctx context.Context, recipient string, n Notification) error
}

// Manager wires Notifiable → Channels.
type Manager struct {
	mu       sync.RWMutex
	channels map[string]Channel
}

// NewManager returns an empty Manager. Register channels with
// Register or RegisterMany.
func NewManager() *Manager {
	return &Manager{channels: map[string]Channel{}}
}

// Register attaches a channel. Re-registering replaces the previous
// channel under the same name.
func (m *Manager) Register(c Channel) {
	m.mu.Lock()
	m.channels[c.Name()] = c
	m.mu.Unlock()
}

// RegisterMany is a convenience for multiple channels.
func (m *Manager) RegisterMany(cs ...Channel) {
	for _, c := range cs {
		m.Register(c)
	}
}

// Channel returns the registered channel by name (nil if missing).
func (m *Manager) Channel(name string) Channel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.channels[name]
}

// Send dispatches n to every channel listed in n.Channels(). Per-
// channel errors are collected with errors.Join and returned
// together; if every channel succeeded, the return value is nil.
//
// Channels not registered on the Manager are silently skipped (with a
// nil error per channel) so a notification declaring extra channels
// doesn't fail when running in a stripped-down environment (e.g.
// tests without SMS configured).
func (m *Manager) Send(ctx context.Context, to Notifiable, n Notification) error {
	var errs []error
	for _, name := range n.Channels() {
		m.mu.RLock()
		ch, ok := m.channels[name]
		m.mu.RUnlock()
		if !ok {
			continue
		}
		recipient := to.RouteFor(name)
		if recipient == "" {
			continue
		}
		if err := ch.Send(ctx, recipient, n); err != nil {
			errs = append(errs, fmt.Errorf("notifications: %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// SendNow is an alias for Send — kept for parity with Laravel
// terminology where SendNow bypasses the queue. The queue-backed path
// lives in a sibling package; from this package's perspective every
// dispatch is synchronous.
func (m *Manager) SendNow(ctx context.Context, to Notifiable, n Notification) error {
	return m.Send(ctx, to, n)
}

// --- MailChannel --------------------------------------------------------

// MailChannel routes notifications through a mail.Mailer.
type MailChannel struct {
	mailer mail.Mailer
}

// NewMailChannel wraps a mail.Mailer as a notification Channel.
func NewMailChannel(m mail.Mailer) *MailChannel {
	return &MailChannel{mailer: m}
}

// Name returns "mail".
func (c *MailChannel) Name() string { return "mail" }

// Send adapts the notification (which must implement MailNotification)
// to the mailer's Message.
func (c *MailChannel) Send(ctx context.Context, recipient string, n Notification) error {
	mn, ok := n.(MailNotification)
	if !ok {
		return errors.New("notifications: notification does not implement MailNotification")
	}
	msg := mn.ToMail(recipient)
	return c.mailer.Send(ctx, msg)
}
