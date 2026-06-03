package mailgun

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devituz/lagodev/mail"
)

// newTestServer captures the inbound request and replies with a
// configurable status + body.
func newTestServer(t *testing.T, status int, body string) (*httptest.Server, func() *http.Request, func() map[string][]string) {
	t.Helper()
	var lastReq *http.Request
	var lastFields map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture request and parsed form fields.
		lastReq = r.Clone(context.Background())
		fields, err := parseMultipart(r)
		if err == nil {
			lastFields = fields
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, func() *http.Request { return lastReq }, func() map[string][]string { return lastFields }
}

func parseMultipart(r *http.Request) (map[string][]string, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for {
		p, err := mr.NextPart()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		data, _ := io.ReadAll(p)
		out[p.FormName()] = append(out[p.FormName()], string(data))
	}
	return out, nil
}

func newMailer(srv *httptest.Server) *Mailer {
	return New(Config{
		Domain: "mg.example.com",
		APIKey: "key-test",
		From:   "noreply@example.com",
		// Inject the test server as the API region.
		Region: Region(srv.URL),
	})
}

func TestSend_BasicTextMessage(t *testing.T) {
	srv, lastReq, lastFields := newTestServer(t, 200, `{"id":"x"}`)
	m := newMailer(srv)
	err := m.Send(context.Background(), mail.NewMessage().
		To("a@x").
		Subject("Hello").
		Text("Body").
		Build())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := lastReq()
	if req.Method != "POST" {
		t.Fatalf("method = %s", req.Method)
	}
	if !strings.HasSuffix(req.URL.Path, "/mg.example.com/messages") {
		t.Fatalf("URL = %s", req.URL.Path)
	}
	user, pass, ok := req.BasicAuth()
	if !ok || user != "api" || pass != "key-test" {
		t.Fatalf("auth = (%s, %s, %v)", user, pass, ok)
	}
	f := lastFields()
	if f["from"][0] != "noreply@example.com" {
		t.Fatalf("from = %v", f["from"])
	}
	if f["to"][0] != "a@x" {
		t.Fatalf("to = %v", f["to"])
	}
	if f["subject"][0] != "Hello" || f["text"][0] != "Body" {
		t.Fatalf("subject/text fields wrong: %+v", f)
	}
}

func TestSend_MultipleRecipientsAndCcBcc(t *testing.T) {
	srv, _, lastFields := newTestServer(t, 200, "{}")
	m := newMailer(srv)
	_ = m.Send(context.Background(), mail.NewMessage().
		To("a@x", "b@x").Cc("c@x").Bcc("d@x").
		Subject("s").Text("t").Build())

	f := lastFields()
	if len(f["to"]) != 2 || f["cc"][0] != "c@x" || f["bcc"][0] != "d@x" {
		t.Fatalf("recipients off: %+v", f)
	}
}

func TestSend_HTMLAndHeadersEmitted(t *testing.T) {
	srv, _, lastFields := newTestServer(t, 200, "{}")
	m := newMailer(srv)
	_ = m.Send(context.Background(), mail.NewMessage().
		To("a@x").Subject("s").HTML("<b>hi</b>").
		Header("X-Trace", "abc").Build())
	f := lastFields()
	if f["html"][0] != "<b>hi</b>" {
		t.Fatalf("html missing: %v", f["html"])
	}
	if v, ok := f["h:X-Trace"]; !ok || v[0] != "abc" {
		t.Fatalf("custom header missing: %v", f)
	}
}

func TestSend_Attachment(t *testing.T) {
	var att []byte
	var attName, attCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mr, _ := r.MultipartReader()
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "attachment" {
				att, _ = io.ReadAll(p)
				attName = p.FileName()
				attCT = p.Header.Get("Content-Type")
			}
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	m := New(Config{Domain: "d", APIKey: "k", From: "f@x", Region: Region(srv.URL)})
	_ = m.Send(context.Background(), mail.NewMessage().
		To("a@x").Subject("s").Text("t").
		Attach("file.txt", []byte("PAYLOAD"), "text/plain").
		Build())
	if string(att) != "PAYLOAD" || attName != "file.txt" || attCT != "text/plain" {
		t.Fatalf("attachment = (%q, %q, %q)", att, attName, attCT)
	}
}

func TestSend_PropagatesAPIError(t *testing.T) {
	srv, _, _ := newTestServer(t, 400, `{"message":"missing param"}`)
	m := newMailer(srv)
	err := m.Send(context.Background(), mail.NewMessage().
		To("a@x").Subject("s").Text("t").Build())
	if err == nil {
		t.Fatal("expected error from 4xx response")
	}
	if !strings.Contains(err.Error(), "missing param") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_MissingConfigRejected(t *testing.T) {
	for _, c := range []Config{
		{APIKey: "k", From: "f@x"},
		{Domain: "d", From: "f@x"},
	} {
		m := New(c)
		err := m.Send(context.Background(), mail.NewMessage().To("a@x").Subject("s").Text("t").Build())
		if err == nil {
			t.Fatalf("config %+v must error", c)
		}
	}
}

func TestSend_MissingFromRejected(t *testing.T) {
	srv, _, _ := newTestServer(t, 200, "{}")
	m := New(Config{Domain: "d", APIKey: "k", Region: Region(srv.URL)})
	err := m.Send(context.Background(), mail.NewMessage().To("a@x").Subject("s").Text("t").Build())
	if err == nil {
		t.Fatal("missing From should error")
	}
}

func TestSend_EmptyRecipientsRejected(t *testing.T) {
	srv, _, _ := newTestServer(t, 200, "{}")
	m := newMailer(srv)
	err := m.Send(context.Background(), mail.NewMessage().Subject("s").Build())
	if !errors.Is(err, mail.ErrEmptyRecipients) {
		t.Fatalf("want ErrEmptyRecipients, got %v", err)
	}
}

// Ensure imports stay relevant.
var _ multipart.File
