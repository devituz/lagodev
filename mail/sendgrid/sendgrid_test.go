package sendgrid

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devituz/lagodev/mail"
)

func newTestServer(t *testing.T, status int, body string) (*httptest.Server, func() *http.Request, func() sendgridPayload) {
	t.Helper()
	var lastReq *http.Request
	var lastPayload sendgridPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(buf, &lastPayload)
		lastReq = r.Clone(context.Background())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, func() *http.Request { return lastReq }, func() sendgridPayload { return lastPayload }
}

func newMailer(srv *httptest.Server) *Mailer {
	return New(Config{
		APIKey:   "SG.test",
		From:     "noreply@example.com",
		Endpoint: srv.URL,
	})
}

func TestSend_BasicTextMessage(t *testing.T) {
	srv, lastReq, lastPayload := newTestServer(t, 202, "")
	m := newMailer(srv)
	err := m.Send(context.Background(), mail.NewMessage().
		To("a@x").Subject("Hi").Text("Hello").Build())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if lastReq().Header.Get("Authorization") != "Bearer SG.test" {
		t.Fatalf("auth header = %q", lastReq().Header.Get("Authorization"))
	}
	if lastReq().Header.Get("Content-Type") != "application/json" {
		t.Fatalf("ct = %q", lastReq().Header.Get("Content-Type"))
	}
	p := lastPayload()
	if p.From.Email != "noreply@example.com" {
		t.Fatalf("from = %+v", p.From)
	}
	if len(p.Personalizations) != 1 || p.Personalizations[0].To[0].Email != "a@x" {
		t.Fatalf("personalisations = %+v", p.Personalizations)
	}
	if p.Subject != "Hi" {
		t.Fatalf("subject = %q", p.Subject)
	}
	if len(p.Content) != 1 || p.Content[0].Type != "text/plain" || p.Content[0].Value != "Hello" {
		t.Fatalf("content = %+v", p.Content)
	}
}

func TestSend_BothTextAndHTMLBecomeTwoContentBlocks(t *testing.T) {
	srv, _, lastPayload := newTestServer(t, 202, "")
	m := newMailer(srv)
	_ = m.Send(context.Background(), mail.NewMessage().
		To("a@x").Subject("s").Text("plain").HTML("<b>html</b>").Build())
	p := lastPayload()
	if len(p.Content) != 2 {
		t.Fatalf("want 2 content blocks, got %d", len(p.Content))
	}
	if p.Content[0].Type != "text/plain" || p.Content[1].Type != "text/html" {
		t.Fatalf("content types = %s, %s", p.Content[0].Type, p.Content[1].Type)
	}
}

func TestSend_CcBccEncoded(t *testing.T) {
	srv, _, lastPayload := newTestServer(t, 202, "")
	m := newMailer(srv)
	_ = m.Send(context.Background(), mail.NewMessage().
		To("a@x").Cc("c@x").Bcc("d@x").Subject("s").Text("t").Build())
	p := lastPayload().Personalizations[0]
	if p.Cc[0].Email != "c@x" || p.Bcc[0].Email != "d@x" {
		t.Fatalf("cc/bcc = %+v / %+v", p.Cc, p.Bcc)
	}
}

func TestSend_Attachment_Base64Encoded(t *testing.T) {
	srv, _, lastPayload := newTestServer(t, 202, "")
	m := newMailer(srv)
	_ = m.Send(context.Background(), mail.NewMessage().
		To("a@x").Subject("s").Text("t").
		Attach("report.txt", []byte("PAYLOAD"), "text/plain").Build())

	atts := lastPayload().Attachments
	if len(atts) != 1 {
		t.Fatalf("want 1 attachment, got %d", len(atts))
	}
	if atts[0].Filename != "report.txt" || atts[0].Type != "text/plain" {
		t.Fatalf("att meta = %+v", atts[0])
	}
	decoded, err := base64.StdEncoding.DecodeString(atts[0].Content)
	if err != nil || string(decoded) != "PAYLOAD" {
		t.Fatalf("base64 decode = (%q, %v)", decoded, err)
	}
}

func TestSend_ReplyToAndHeaders(t *testing.T) {
	srv, _, lastPayload := newTestServer(t, 202, "")
	m := newMailer(srv)
	_ = m.Send(context.Background(), mail.NewMessage().
		To("a@x").ReplyTo("rt@x").Header("X-Trace", "abc").
		Subject("s").Text("t").Build())
	p := lastPayload()
	if p.ReplyTo == nil || p.ReplyTo.Email != "rt@x" {
		t.Fatalf("reply-to = %+v", p.ReplyTo)
	}
	if p.Headers["X-Trace"] != "abc" {
		t.Fatalf("headers = %+v", p.Headers)
	}
}

func TestSend_PropagatesAPIError(t *testing.T) {
	srv, _, _ := newTestServer(t, 401, `{"errors":[{"message":"invalid key"}]}`)
	m := newMailer(srv)
	err := m.Send(context.Background(), mail.NewMessage().
		To("a@x").Subject("s").Text("t").Build())
	if err == nil || !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_MissingConfigRejected(t *testing.T) {
	// Missing API key
	m := New(Config{From: "f@x"})
	if err := m.Send(context.Background(), mail.NewMessage().To("a@x").Subject("s").Text("t").Build()); err == nil {
		t.Fatal("missing APIKey must error")
	}
	// Missing From and msg.From empty
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(202)
	}))
	defer srv.Close()
	m2 := New(Config{APIKey: "k", Endpoint: srv.URL})
	if err := m2.Send(context.Background(), mail.NewMessage().To("a@x").Subject("s").Text("t").Build()); err == nil {
		t.Fatal("missing From must error")
	}
}

func TestSend_EmptyRecipients(t *testing.T) {
	srv, _, _ := newTestServer(t, 202, "")
	m := newMailer(srv)
	err := m.Send(context.Background(), mail.NewMessage().Subject("s").Build())
	if !errors.Is(err, mail.ErrEmptyRecipients) {
		t.Fatalf("want ErrEmptyRecipients, got %v", err)
	}
}
