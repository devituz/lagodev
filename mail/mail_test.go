package mail

import (
	"context"
	"errors"
	"net/smtp"
	"strings"
	"sync"
	"testing"
)

func TestBuilder_FieldsRouted(t *testing.T) {
	m := NewMessage().
		From("from@x").
		To("a@x", "b@x").
		Cc("c@x").
		Bcc("d@x").
		ReplyTo("rt@x").
		Subject("s").
		Text("hi").
		HTML("<b>hi</b>").
		Header("X-Custom", "1").
		Build()
	if m.From != "from@x" {
		t.Fatalf("from = %q", m.From)
	}
	if len(m.To) != 2 || len(m.Cc) != 1 || len(m.Bcc) != 1 {
		t.Fatalf("recipients off: To=%v Cc=%v Bcc=%v", m.To, m.Cc, m.Bcc)
	}
	if m.ReplyTo != "rt@x" || m.Subject != "s" || m.Text != "hi" || m.HTML != "<b>hi</b>" {
		t.Fatalf("body fields off: %+v", m)
	}
	if m.Headers["X-Custom"] != "1" {
		t.Fatalf("custom header missing")
	}
}

func TestBuilder_Recipients_AggregateAllLists(t *testing.T) {
	m := NewMessage().To("a@x").Cc("b@x").Bcc("c@x").Build()
	got := m.Recipients()
	if len(got) != 3 {
		t.Fatalf("Recipients() = %v", got)
	}
}

func TestEncode_RejectsNoRecipients(t *testing.T) {
	_, err := NewMessage().From("a@x").Subject("hi").Build().Encode()
	if !errors.Is(err, ErrEmptyRecipients) {
		t.Fatalf("want ErrEmptyRecipients, got %v", err)
	}
}

func TestEncode_TextOnly(t *testing.T) {
	body, err := NewMessage().From("a@x").To("b@x").Subject("hello").Text("hi").Build().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "Content-Type: text/plain") {
		t.Fatalf("text plain header missing:\n%s", s)
	}
	if !strings.Contains(s, "Subject: hello") {
		t.Fatalf("subject missing:\n%s", s)
	}
}

func TestEncode_HTMLOnly(t *testing.T) {
	body, _ := NewMessage().From("a@x").To("b@x").Subject("s").HTML("<h1>hi</h1>").Build().Encode()
	if !strings.Contains(string(body), "text/html") {
		t.Fatalf("html part missing:\n%s", body)
	}
}

func TestEncode_BothTextAndHTML_UsesAlternative(t *testing.T) {
	body, _ := NewMessage().From("a@x").To("b@x").Subject("s").
		Text("plain").HTML("<b>bold</b>").Build().Encode()
	s := string(body)
	if !strings.Contains(s, "multipart/alternative") {
		t.Fatalf("must use multipart/alternative:\n%s", s)
	}
	if !strings.Contains(s, "text/plain") || !strings.Contains(s, "text/html") {
		t.Fatalf("both subparts must be present:\n%s", s)
	}
}

func TestEncode_AttachmentsTriggersMixed(t *testing.T) {
	body, err := NewMessage().From("a@x").To("b@x").Subject("s").Text("hi").
		Attach("report.txt", []byte("DATA"), "text/plain").
		Attach("image.png", []byte{0x89, 0x50, 0x4e, 0x47}).
		Build().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "multipart/mixed") {
		t.Fatalf("multipart/mixed expected:\n%s", s)
	}
	if !strings.Contains(s, "Content-Disposition: attachment") {
		t.Fatalf("attachment disposition missing:\n%s", s)
	}
	if !strings.Contains(s, "report.txt") || !strings.Contains(s, "image.png") {
		t.Fatalf("attachment filenames missing:\n%s", s)
	}
	// image.png auto-detected MIME
	if !strings.Contains(s, "image/png") {
		t.Fatalf("auto-detected MIME for image.png missing:\n%s", s)
	}
}

func TestEncode_QuotedPrintableForUTF8(t *testing.T) {
	body, _ := NewMessage().From("a@x").To("b@x").Subject("salom").
		Text("Salom — bu test").Build().Encode()
	s := string(body)
	// "—" (em dash) is non-ASCII, must be encoded.
	if strings.Contains(s, "—") {
		t.Fatalf("unicode dash must be quoted-printable encoded:\n%s", s)
	}
	if !strings.Contains(s, "Content-Transfer-Encoding: quoted-printable") {
		t.Fatalf("QP transfer encoding header missing:\n%s", s)
	}
}

func TestEncode_SubjectIsQEncoded(t *testing.T) {
	body, _ := NewMessage().From("a@x").To("b@x").Subject("ünıcödé").Text("hi").Build().Encode()
	s := string(body)
	if strings.Contains(s, "Subject: ünıcödé") {
		t.Fatalf("subject must be Q-encoded:\n%s", s)
	}
	if !strings.Contains(s, "Subject: =?utf-8?q?") {
		t.Fatalf("Q-encoded subject prefix missing:\n%s", s)
	}
}

func TestLogMailer_CapturesAndCounts(t *testing.T) {
	l := NewLogMailer()
	for i := 0; i < 3; i++ {
		_ = l.Send(context.Background(), NewMessage().From("a@x").To("b@x").Subject("hi").Text("x").Build())
	}
	if l.Count() != 3 {
		t.Fatalf("Count = %d", l.Count())
	}
	if l.Last().Subject != "hi" {
		t.Fatalf("Last.Subject = %q", l.Last().Subject)
	}
	l.Reset()
	if l.Count() != 0 {
		t.Fatal("Reset must zero the count")
	}
}

func TestLogMailer_RejectsEmptyRecipients(t *testing.T) {
	l := NewLogMailer()
	err := l.Send(context.Background(), NewMessage().Subject("s").Build())
	if !errors.Is(err, ErrEmptyRecipients) {
		t.Fatalf("want ErrEmptyRecipients, got %v", err)
	}
}

func TestLogMailer_ConcurrentSafe(t *testing.T) {
	l := NewLogMailer()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.Send(context.Background(), NewMessage().From("a@x").To("b@x").Subject("s").Text("x").Build())
		}()
	}
	wg.Wait()
	if l.Count() != 50 {
		t.Fatalf("Count = %d", l.Count())
	}
}

func TestSMTPMailer_FallsBackToConfigFrom(t *testing.T) {
	var seenFrom string
	m := NewSMTPMailer(SMTPConfig{
		Host: "localhost", Port: 25, From: "default@x",
	})
	m.sendFn = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		seenFrom = from
		return nil
	}
	_ = m.Send(context.Background(),
		NewMessage().To("b@x").Subject("s").Text("x").Build())
	if seenFrom != "default@x" {
		t.Fatalf("expected default From, got %q", seenFrom)
	}
}

func TestSMTPMailer_PassesRecipientsToTransport(t *testing.T) {
	var seenTo []string
	m := NewSMTPMailer(SMTPConfig{Host: "x", Port: 25, From: "from@x"})
	m.sendFn = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		seenTo = to
		return nil
	}
	_ = m.Send(context.Background(),
		NewMessage().To("a@x").Cc("b@x").Bcc("c@x").Subject("s").Text("x").Build())
	if len(seenTo) != 3 {
		t.Fatalf("envelope recipients = %v", seenTo)
	}
}

func TestSMTPMailer_HonoursContextCancellation(t *testing.T) {
	m := NewSMTPMailer(SMTPConfig{Host: "x", Port: 25, From: "from@x"})
	// Hold the send forever so ctx wins.
	block := make(chan struct{})
	defer close(block)
	m.sendFn = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		<-block
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled
	err := m.Send(ctx, NewMessage().To("a@x").Subject("s").Text("x").Build())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
