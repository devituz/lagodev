// Package mailgun implements mail.Mailer against Mailgun's HTTP API
// (https://documentation.mailgun.com/en/latest/api-sending.html).
//
// Compared to the SMTP transport, the HTTP driver is friendlier to
// platforms that block outbound port 25/465/587 (Heroku, App Engine,
// Cloud Run, certain corporate networks) and gives Mailgun-specific
// affordances like tags, custom variables, and SMTP-suppression-
// list bypass via `v:`-headers.
//
// Usage:
//
//	m := mailgun.New(mailgun.Config{
//	    Domain: "mg.example.com",
//	    APIKey: os.Getenv("MAILGUN_KEY"),
//	    From:   "noreply@example.com",
//	})
//	err := m.Send(ctx, mail.NewMessage().
//	    To("user@example.com").
//	    Subject("Hi").
//	    HTML("<b>hello</b>").
//	    Build())
package mailgun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/devituz/lagodev/mail"
)

// Region selects the Mailgun region endpoint.
type Region string

const (
	RegionUS Region = "https://api.mailgun.net/v3"
	RegionEU Region = "https://api.eu.mailgun.net/v3"
)

// Config holds the Mailgun API credentials.
type Config struct {
	// Domain is the sending domain registered with Mailgun
	// (e.g. "mg.example.com").
	Domain string
	// APIKey is the private API key.
	APIKey string
	// From is the default From address used when Message.From is empty.
	From string
	// Region defaults to RegionUS when unset.
	Region Region
	// HTTPClient is the http.Client used for requests. Default has a
	// 30s timeout.
	HTTPClient *http.Client
}

// Mailer talks the Mailgun HTTP API.
type Mailer struct {
	cfg Config
}

// New constructs a Mailer.
func New(cfg Config) *Mailer {
	if cfg.Region == "" {
		cfg.Region = RegionUS
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Mailer{cfg: cfg}
}

// Static compile-time check that *Mailer satisfies mail.Mailer.
var _ mail.Mailer = (*Mailer)(nil)

// Send dispatches msg via Mailgun. Non-2xx responses surface the
// Mailgun error body in the returned error for easier debugging.
func (m *Mailer) Send(ctx context.Context, msg mail.Message) error {
	if m.cfg.Domain == "" {
		return errors.New("mailgun: Domain is required")
	}
	if m.cfg.APIKey == "" {
		return errors.New("mailgun: APIKey is required")
	}
	if msg.From == "" {
		msg.From = m.cfg.From
	}
	if msg.From == "" {
		return errors.New("mailgun: From is required")
	}
	if len(msg.Recipients()) == 0 {
		return mail.ErrEmptyRecipients
	}

	body, contentType, err := buildForm(msg)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/%s/messages", string(m.cfg.Region), m.cfg.Domain)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("mailgun: new request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.SetBasicAuth("api", m.cfg.APIKey)

	resp, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("mailgun: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mailgun: status %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}
	return nil
}

func buildForm(msg mail.Message) (io.Reader, string, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()
		defer mw.Close()

		add := func(k, v string) {
			_ = mw.WriteField(k, v)
		}
		add("from", msg.From)
		for _, to := range msg.To {
			add("to", to)
		}
		for _, cc := range msg.Cc {
			add("cc", cc)
		}
		for _, bcc := range msg.Bcc {
			add("bcc", bcc)
		}
		if msg.ReplyTo != "" {
			add("h:Reply-To", msg.ReplyTo)
		}
		add("subject", msg.Subject)
		if msg.Text != "" {
			add("text", msg.Text)
		}
		if msg.HTML != "" {
			add("html", msg.HTML)
		}
		for k, v := range msg.Headers {
			add("h:"+k, v)
		}
		// Attachments: Mailgun expects form field "attachment".
		for _, a := range msg.Attachments {
			header := make(textproto.MIMEHeader)
			header.Set("Content-Type", a.ContentType)
			header.Set("Content-Disposition",
				fmt.Sprintf(`form-data; name="attachment"; filename="%s"`, a.Filename))
			part, err := mw.CreatePart(header)
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if _, err := part.Write(a.Data); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr, mw.FormDataContentType(), nil
}

