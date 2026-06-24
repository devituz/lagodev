// Package mail provides a Mailer abstraction with an SMTP driver
// modelled on Laravel's Mail facade.
//
// Two primitives:
//
//   - Message — the email payload (From/To/Cc/Bcc/Subject/Text/HTML +
//     attachments). Built by hand or via NewMessage(...).
//   - Mailer — the transport interface. The bundled SMTPMailer talks
//     to any RFC-compliant SMTP server (Postfix, Sendgrid, Mailgun,
//     Postmark, AWS SES, Mailtrap, MailHog).
//
// A LogMailer is shipped for tests/local dev — it captures sent
// messages in memory instead of delivering them.
//
// Usage:
//
//	m := mail.NewSMTPMailer(mail.SMTPConfig{
//	    Host:     "smtp.mailgun.org",
//	    Port:     587,
//	    Username: "postmaster@example.com",
//	    Password: os.Getenv("MAIL_PASSWORD"),
//	    From:     "noreply@example.com",
//	})
//	err := m.Send(ctx, mail.NewMessage().
//	    To("user@example.com").
//	    Subject("Hello").
//	    HTML("<h1>Hi!</h1>").
//	    Build())
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrEmptyRecipients is returned when a Message has no To/Cc/Bcc.
var ErrEmptyRecipients = errors.New("mail: at least one recipient required")

// ErrHeaderInjection is returned when an address or header value
// contains a CR/LF, which could be used to smuggle additional headers
// (e.g. a hidden Bcc) into the RFC 5322 header block.
var ErrHeaderInjection = errors.New("mail: CR/LF in header field rejected")

// Attachment is an inline file attached to a Message.
type Attachment struct {
	Filename    string
	ContentType string // e.g. "image/png"; auto-detected from extension when empty
	Data        []byte
}

// Message is an immutable email payload built via Builder.
type Message struct {
	From        string
	To          []string
	Cc          []string
	Bcc         []string
	ReplyTo     string
	Subject     string
	Text        string
	HTML        string
	Headers     map[string]string
	Attachments []Attachment
}

// Builder constructs a Message fluently.
type Builder struct {
	m Message
}

// NewMessage returns an empty Builder.
func NewMessage() *Builder { return &Builder{m: Message{Headers: map[string]string{}}} }

func (b *Builder) From(addr string) *Builder { b.m.From = addr; return b }
func (b *Builder) To(addrs ...string) *Builder {
	b.m.To = append(b.m.To, addrs...)
	return b
}
func (b *Builder) Cc(addrs ...string) *Builder {
	b.m.Cc = append(b.m.Cc, addrs...)
	return b
}
func (b *Builder) Bcc(addrs ...string) *Builder {
	b.m.Bcc = append(b.m.Bcc, addrs...)
	return b
}
func (b *Builder) ReplyTo(addr string) *Builder { b.m.ReplyTo = addr; return b }
func (b *Builder) Subject(s string) *Builder    { b.m.Subject = s; return b }
func (b *Builder) Text(s string) *Builder       { b.m.Text = s; return b }
func (b *Builder) HTML(s string) *Builder       { b.m.HTML = s; return b }
func (b *Builder) Header(k, v string) *Builder  { b.m.Headers[k] = v; return b }

// Attach appends an attachment. ContentType may be empty — it's
// auto-detected from the filename extension.
func (b *Builder) Attach(filename string, data []byte, contentType ...string) *Builder {
	a := Attachment{Filename: filename, Data: data}
	if len(contentType) > 0 && contentType[0] != "" {
		a.ContentType = contentType[0]
	} else {
		a.ContentType = mime.TypeByExtension(filepath.Ext(filename))
		if a.ContentType == "" {
			a.ContentType = "application/octet-stream"
		}
	}
	b.m.Attachments = append(b.m.Attachments, a)
	return b
}

// Build returns the finalised Message.
func (b *Builder) Build() Message {
	out := b.m
	if out.Headers == nil {
		out.Headers = map[string]string{}
	}
	return out
}

// Recipients returns every To/Cc/Bcc address (the envelope set).
func (m Message) Recipients() []string {
	out := make([]string, 0, len(m.To)+len(m.Cc)+len(m.Bcc))
	out = append(out, m.To...)
	out = append(out, m.Cc...)
	out = append(out, m.Bcc...)
	return out
}

// Encode renders the Message as a complete RFC 5322 email body
// (including headers + MIME multipart structure). The result is
// suitable for the DATA portion of an SMTP exchange.
//
// Structure:
//   - no attachments, text only          → text/plain
//   - no attachments, html only          → text/html
//   - no attachments, both               → multipart/alternative
//   - any attachments                    → multipart/mixed wrapping
//     alternative-or-single body
func (m Message) Encode() ([]byte, error) {
	if len(m.Recipients()) == 0 {
		return nil, ErrEmptyRecipients
	}
	// Validate/canonicalise every header-bound field before emitting so a
	// CR/LF in any address or custom header cannot smuggle extra headers
	// into the RFC 5322 block (header injection / Bcc smuggling).
	if err := sanitizeHeaders(&m); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	writeCommonHeaders(&buf, m)

	hasAttachments := len(m.Attachments) > 0
	hasBoth := m.Text != "" && m.HTML != ""

	switch {
	case hasAttachments:
		mixed := multipart.NewWriter(&buf)
		writeHeader(&buf, "Content-Type", "multipart/mixed; boundary=\""+mixed.Boundary()+"\"")
		buf.WriteString("\r\n")
		if err := writeBody(mixed, m); err != nil {
			return nil, err
		}
		if err := writeAttachments(mixed, m.Attachments); err != nil {
			return nil, err
		}
		if err := mixed.Close(); err != nil {
			return nil, err
		}
	case hasBoth:
		alt := multipart.NewWriter(&buf)
		writeHeader(&buf, "Content-Type", "multipart/alternative; boundary=\""+alt.Boundary()+"\"")
		buf.WriteString("\r\n")
		if err := writeTextPart(alt, m.Text); err != nil {
			return nil, err
		}
		if err := writeHTMLPart(alt, m.HTML); err != nil {
			return nil, err
		}
		if err := alt.Close(); err != nil {
			return nil, err
		}
	case m.HTML != "":
		writeHeader(&buf, "Content-Type", "text/html; charset=\"utf-8\"")
		writeHeader(&buf, "Content-Transfer-Encoding", "quoted-printable")
		buf.WriteString("\r\n")
		buf.WriteString(quotedPrintable(m.HTML))
	default:
		writeHeader(&buf, "Content-Type", "text/plain; charset=\"utf-8\"")
		writeHeader(&buf, "Content-Transfer-Encoding", "quoted-printable")
		buf.WriteString("\r\n")
		buf.WriteString(quotedPrintable(m.Text))
	}
	return buf.Bytes(), nil
}

// sanitizeHeaders validates and canonicalises every header-bound field
// of m in place. Addresses (From/To/Cc/ReplyTo) are parsed with the
// net/mail package and re-emitted in their canonical form; a parse
// failure or an embedded CR/LF is rejected with ErrHeaderInjection.
// Custom header keys and values are checked for CR/LF too. Bcc is an
// envelope-only field (never written into the header block) but is
// still validated so a malicious Bcc can't reach the SMTP transport.
func sanitizeHeaders(m *Message) error {
	if m.From != "" {
		addr, err := canonAddress(m.From)
		if err != nil {
			return err
		}
		m.From = addr
	}
	for _, list := range []*[]string{&m.To, &m.Cc, &m.Bcc} {
		canon, err := canonAddressList(*list)
		if err != nil {
			return err
		}
		*list = canon
	}
	if m.ReplyTo != "" {
		addr, err := canonAddress(m.ReplyTo)
		if err != nil {
			return err
		}
		m.ReplyTo = addr
	}
	for k, v := range m.Headers {
		if containsCRLF(k) || containsCRLF(v) {
			return ErrHeaderInjection
		}
	}
	return nil
}

// canonAddress parses a single address and returns its canonical RFC
// 5322 form. A CR/LF or parse failure yields ErrHeaderInjection.
func canonAddress(s string) (string, error) {
	if containsCRLF(s) {
		return "", ErrHeaderInjection
	}
	a, err := netmail.ParseAddress(s)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrHeaderInjection, err)
	}
	return a.String(), nil
}

// canonAddressList parses a comma-joined address list and returns the
// canonical form of each entry.
func canonAddressList(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if containsCRLF(s) {
			return nil, ErrHeaderInjection
		}
		addrs, err := netmail.ParseAddressList(s)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrHeaderInjection, err)
		}
		for _, a := range addrs {
			out = append(out, a.String())
		}
	}
	return out, nil
}

func containsCRLF(s string) bool {
	return strings.ContainsAny(s, "\r\n")
}

func writeCommonHeaders(buf *bytes.Buffer, m Message) {
	writeHeader(buf, "From", m.From)
	if len(m.To) > 0 {
		writeHeader(buf, "To", strings.Join(m.To, ", "))
	}
	if len(m.Cc) > 0 {
		writeHeader(buf, "Cc", strings.Join(m.Cc, ", "))
	}
	if m.ReplyTo != "" {
		writeHeader(buf, "Reply-To", m.ReplyTo)
	}
	writeHeader(buf, "Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	writeHeader(buf, "MIME-Version", "1.0")
	writeHeader(buf, "Date", time.Now().UTC().Format(time.RFC1123Z))
	writeHeader(buf, "Message-ID", generateMessageID(m.From))
	for k, v := range m.Headers {
		writeHeader(buf, k, v)
	}
}

// writeBody emits the body section of a multipart/mixed message. When
// both Text and HTML are present, a nested multipart/alternative
// section is created; otherwise a single body part is written.
func writeBody(mixed *multipart.Writer, m Message) error {
	if m.Text != "" && m.HTML != "" {
		// Nested alternative inside mixed.
		altBoundary := newBoundary()
		part, err := mixed.CreatePart(textproto.MIMEHeader{
			"Content-Type": []string{"multipart/alternative; boundary=\"" + altBoundary + "\""},
		})
		if err != nil {
			return err
		}
		writeAltPart := func(ct, content string) {
			fmt.Fprintf(part, "--%s\r\n", altBoundary)
			fmt.Fprintf(part, "Content-Type: %s; charset=\"utf-8\"\r\n", ct)
			fmt.Fprintf(part, "Content-Transfer-Encoding: quoted-printable\r\n\r\n")
			fmt.Fprintf(part, "%s\r\n", quotedPrintable(content))
		}
		writeAltPart("text/plain", m.Text)
		writeAltPart("text/html", m.HTML)
		fmt.Fprintf(part, "--%s--\r\n", altBoundary)
		return nil
	}
	ct, body := "text/plain", m.Text
	if m.HTML != "" {
		ct, body = "text/html", m.HTML
	}
	part, err := mixed.CreatePart(textproto.MIMEHeader{
		"Content-Type":              []string{ct + "; charset=\"utf-8\""},
		"Content-Transfer-Encoding": []string{"quoted-printable"},
	})
	if err != nil {
		return err
	}
	_, err = part.Write([]byte(quotedPrintable(body)))
	return err
}

func writeAttachments(mixed *multipart.Writer, atts []Attachment) error {
	for _, a := range atts {
		part, err := mixed.CreatePart(textproto.MIMEHeader{
			"Content-Type":              []string{a.ContentType + "; name=\"" + a.Filename + "\""},
			"Content-Disposition":       []string{"attachment; filename=\"" + a.Filename + "\""},
			"Content-Transfer-Encoding": []string{"base64"},
		})
		if err != nil {
			return err
		}
		enc := base64.StdEncoding.EncodeToString(a.Data)
		for i := 0; i < len(enc); i += 76 {
			end := i + 76
			if end > len(enc) {
				end = len(enc)
			}
			_, _ = part.Write([]byte(enc[i:end] + "\r\n"))
		}
	}
	return nil
}

func writeHeader(w io.Writer, k, v string) {
	fmt.Fprintf(w, "%s: %s\r\n", k, v)
}

func writeTextPart(mw *multipart.Writer, text string) error {
	p, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              []string{"text/plain; charset=\"utf-8\""},
		"Content-Transfer-Encoding": []string{"quoted-printable"},
	})
	if err != nil {
		return err
	}
	_, err = p.Write([]byte(quotedPrintable(text)))
	return err
}

func writeHTMLPart(mw *multipart.Writer, html string) error {
	p, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              []string{"text/html; charset=\"utf-8\""},
		"Content-Transfer-Encoding": []string{"quoted-printable"},
	})
	if err != nil {
		return err
	}
	_, err = p.Write([]byte(quotedPrintable(html)))
	return err
}

func newBoundary() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("=_alt_%x", buf[:])
}

func generateMessageID(from string) string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	domain := "localhost"
	if i := strings.LastIndex(from, "@"); i > 0 {
		domain = from[i+1:]
		domain = strings.TrimRight(domain, ">")
	}
	return fmt.Sprintf("<%x@%s>", buf[:], domain)
}

// quotedPrintable encodes s for safe inclusion in an SMTP body.
// Lines longer than 76 chars are soft-wrapped per RFC 2045.
func quotedPrintable(s string) string {
	const safeMax = 75
	var buf bytes.Buffer
	lineLen := 0
	flush := func() {
		buf.WriteString("=\r\n")
		lineLen = 0
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		// Treat real CRLF/LF as line breaks
		if c == '\n' {
			buf.WriteString("\r\n")
			lineLen = 0
			continue
		}
		if c == '\r' {
			// swallow — \n follow-up will emit the line break
			continue
		}
		var token string
		if c == '=' || c < 32 || c > 126 || c == ' ' && (i+1 == len(s) || s[i+1] == '\n') {
			token = fmt.Sprintf("=%02X", c)
		} else {
			token = string(c)
		}
		if lineLen+len(token) > safeMax {
			flush()
		}
		buf.WriteString(token)
		lineLen += len(token)
	}
	return buf.String()
}

// Mailer is the transport. Send delivers msg synchronously.
type Mailer interface {
	Send(ctx context.Context, msg Message) error
}

// SMTPConfig configures an SMTP-backed Mailer.
type SMTPConfig struct {
	Host     string // e.g. "smtp.mailgun.org"
	Port     int    // 25 / 465 / 587
	Username string
	Password string
	// From is the default From address used when Message.From is empty.
	From string
	// Insecure disables STARTTLS. Use only with isolated dev servers
	// like MailHog. The default (false) uses STARTTLS when offered.
	Insecure bool
	// TLSImplicit forces an immediate TLS handshake (port 465 mode).
	TLSImplicit bool
}

// SMTPMailer talks plain-text SMTP with optional STARTTLS.
type SMTPMailer struct {
	cfg SMTPConfig
	// sendFn is overridable for tests.
	sendFn func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// NewSMTPMailer returns a Mailer that talks to an SMTP server.
func NewSMTPMailer(cfg SMTPConfig) *SMTPMailer {
	return &SMTPMailer{cfg: cfg, sendFn: smtp.SendMail}
}

// Send dispatches msg. If Message.From is empty, the config's default
// From is used. The wall-clock deadline is taken from ctx.
//
// Send returns as soon as ctx is done, but because net/smtp.SendMail is
// not context-aware the underlying delivery goroutine keeps running
// until the SMTP exchange finishes (or the dialler's own timeout fires)
// — it is not killed when ctx is cancelled. Set a Dialer/Host timeout
// at the network layer to bound that goroutine.
func (m *SMTPMailer) Send(ctx context.Context, msg Message) error {
	if msg.From == "" {
		msg.From = m.cfg.From
	}
	body, err := msg.Encode()
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("%s:%d", m.cfg.Host, m.cfg.Port)
	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}
	// Honour ctx by running on a goroutine and racing the deadline.
	errCh := make(chan error, 1)
	go func() {
		errCh <- m.sendFn(addr, auth, msg.From, msg.Recipients(), body)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LogMailer is an in-memory Mailer for tests/local dev. Sent messages
// accumulate in Sent.
type LogMailer struct {
	mu   sync.Mutex
	Sent []Message
}

// NewLogMailer returns an empty in-memory Mailer.
func NewLogMailer() *LogMailer { return &LogMailer{} }

// Send appends msg to Sent.
func (l *LogMailer) Send(_ context.Context, msg Message) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(msg.Recipients()) == 0 {
		return ErrEmptyRecipients
	}
	l.Sent = append(l.Sent, msg)
	return nil
}

// Last returns the most recent message, or zero if none.
func (l *LogMailer) Last() Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.Sent) == 0 {
		return Message{}
	}
	return l.Sent[len(l.Sent)-1]
}

// Count returns how many messages have been sent.
func (l *LogMailer) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.Sent)
}

// Reset clears the captured history.
func (l *LogMailer) Reset() {
	l.mu.Lock()
	l.Sent = nil
	l.mu.Unlock()
}
