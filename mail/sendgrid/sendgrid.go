// Package sendgrid implements mail.Mailer against SendGrid's v3 HTTP
// API (https://docs.sendgrid.com/api-reference/mail-send/mail-send).
//
// Usage:
//
//	m := sendgrid.New(sendgrid.Config{
//	    APIKey: os.Getenv("SENDGRID_API_KEY"),
//	    From:   "noreply@example.com",
//	})
//	err := m.Send(ctx, mail.NewMessage().
//	    To("user@example.com").
//	    Subject("Hi").
//	    HTML("<b>hello</b>").
//	    Build())
package sendgrid

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/devituz/lagodev/mail"
)

// DefaultEndpoint is SendGrid's mail-send v3 endpoint.
const DefaultEndpoint = "https://api.sendgrid.com/v3/mail/send"

// Config holds the SendGrid credentials and defaults.
type Config struct {
	APIKey string
	// From is the default From address used when Message.From is empty.
	From string
	// Endpoint allows overriding the API URL (default: DefaultEndpoint).
	// Mostly useful for tests.
	Endpoint string
	// HTTPClient defaults to a 30s-timeout client.
	HTTPClient *http.Client
}

// Mailer dispatches messages through SendGrid's HTTP API.
type Mailer struct {
	cfg Config
}

// New constructs a Mailer.
func New(cfg Config) *Mailer {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Mailer{cfg: cfg}
}

// Static compile-time guard.
var _ mail.Mailer = (*Mailer)(nil)

// Send dispatches msg. SendGrid replies with 202 on success; anything
// else surfaces the response body as an error for debugging.
func (m *Mailer) Send(ctx context.Context, msg mail.Message) error {
	if m.cfg.APIKey == "" {
		return errors.New("sendgrid: APIKey is required")
	}
	if msg.From == "" {
		msg.From = m.cfg.From
	}
	if msg.From == "" {
		return errors.New("sendgrid: From is required")
	}
	if len(msg.Recipients()) == 0 {
		return mail.ErrEmptyRecipients
	}

	payload := buildPayload(msg)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("sendgrid: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("sendgrid: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("sendgrid: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sendgrid: status %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}
	return nil
}

// --- Wire format --------------------------------------------------------

type sendgridPayload struct {
	Personalizations []personalization `json:"personalizations"`
	From             emailAddress      `json:"from"`
	ReplyTo          *emailAddress     `json:"reply_to,omitempty"`
	Subject          string            `json:"subject"`
	Content          []content         `json:"content"`
	Headers          map[string]string `json:"headers,omitempty"`
	Attachments      []attachment      `json:"attachments,omitempty"`
}

type personalization struct {
	To  []emailAddress `json:"to"`
	Cc  []emailAddress `json:"cc,omitempty"`
	Bcc []emailAddress `json:"bcc,omitempty"`
}

type emailAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type content struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type attachment struct {
	Content     string `json:"content"`
	Type        string `json:"type"`
	Filename    string `json:"filename"`
	Disposition string `json:"disposition"`
}

func buildPayload(msg mail.Message) sendgridPayload {
	p := personalization{
		To: toAddresses(msg.To),
	}
	if len(msg.Cc) > 0 {
		p.Cc = toAddresses(msg.Cc)
	}
	if len(msg.Bcc) > 0 {
		p.Bcc = toAddresses(msg.Bcc)
	}

	var body []content
	if msg.Text != "" {
		body = append(body, content{Type: "text/plain", Value: msg.Text})
	}
	if msg.HTML != "" {
		body = append(body, content{Type: "text/html", Value: msg.HTML})
	}
	// SendGrid requires at least one content block — fall back to empty
	// plain-text when the caller forgot.
	if len(body) == 0 {
		body = append(body, content{Type: "text/plain", Value: ""})
	}

	var atts []attachment
	for _, a := range msg.Attachments {
		atts = append(atts, attachment{
			Content:     base64.StdEncoding.EncodeToString(a.Data),
			Type:        a.ContentType,
			Filename:    a.Filename,
			Disposition: "attachment",
		})
	}

	out := sendgridPayload{
		Personalizations: []personalization{p},
		From:             emailAddress{Email: msg.From},
		Subject:          msg.Subject,
		Content:          body,
		Attachments:      atts,
	}
	if msg.ReplyTo != "" {
		out.ReplyTo = &emailAddress{Email: msg.ReplyTo}
	}
	if len(msg.Headers) > 0 {
		out.Headers = msg.Headers
	}
	return out
}

func toAddresses(in []string) []emailAddress {
	out := make([]emailAddress, len(in))
	for i, a := range in {
		out[i] = emailAddress{Email: a}
	}
	return out
}
