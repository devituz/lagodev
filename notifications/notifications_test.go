package notifications

import (
	"context"
	"errors"
	"testing"

	"github.com/devituz/lagodev/mail"
)

// --- Test fixtures ------------------------------------------------------

type User struct {
	Email string
	Phone string
}

func (u User) RouteFor(channel string) string {
	switch channel {
	case "mail":
		return u.Email
	case "sms":
		return u.Phone
	}
	return ""
}

type WelcomeNotification struct{}

func (WelcomeNotification) Channels() []string { return []string{"mail"} }
func (WelcomeNotification) ToMail(to string) mail.Message {
	return mail.NewMessage().
		From("noreply@x").
		To(to).
		Subject("Welcome").
		Text("Hi there").
		Build()
}

type AlertNotification struct{}

func (AlertNotification) Channels() []string { return []string{"mail", "sms"} }
func (AlertNotification) ToMail(to string) mail.Message {
	return mail.NewMessage().From("noreply@x").To(to).Subject("Alert").Text("!").Build()
}

// recordingChannel is a Channel that just records what it received.
type recordingChannel struct {
	name string
	got  []string
	fail error
}

func (r *recordingChannel) Name() string { return r.name }
func (r *recordingChannel) Send(_ context.Context, recipient string, _ Notification) error {
	r.got = append(r.got, recipient)
	return r.fail
}

// --- Tests --------------------------------------------------------------

func TestSend_MailChannel_DeliversThroughMailer(t *testing.T) {
	m := mail.NewLogMailer()
	mgr := NewManager()
	mgr.Register(NewMailChannel(m))

	user := User{Email: "a@x", Phone: "+1"}
	if err := mgr.Send(context.Background(), user, WelcomeNotification{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if m.Count() != 1 {
		t.Fatalf("mailer Count = %d", m.Count())
	}
	if m.Last().To[0] != "a@x" || m.Last().Subject != "Welcome" {
		t.Fatalf("last message off: %+v", m.Last())
	}
}

func TestSend_SkipsUnregisteredChannel(t *testing.T) {
	mgr := NewManager() // no channels at all
	if err := mgr.Send(context.Background(), User{Email: "a@x"}, WelcomeNotification{}); err != nil {
		t.Fatalf("unregistered channels must be silent, got %v", err)
	}
}

func TestSend_SkipsEmptyRoute(t *testing.T) {
	m := mail.NewLogMailer()
	mgr := NewManager()
	mgr.Register(NewMailChannel(m))
	// User with no email → RouteFor("mail") returns ""
	if err := mgr.Send(context.Background(), User{}, WelcomeNotification{}); err != nil {
		t.Fatalf("empty route must be silent, got %v", err)
	}
	if m.Count() != 0 {
		t.Fatalf("nothing should have been sent")
	}
}

func TestSend_AggregatesPerChannelErrors(t *testing.T) {
	smsErr := errors.New("sms gateway down")
	sms := &recordingChannel{name: "sms", fail: smsErr}
	m := mail.NewLogMailer()
	mgr := NewManager()
	mgr.RegisterMany(NewMailChannel(m), sms)

	user := User{Email: "a@x", Phone: "+1"}
	err := mgr.Send(context.Background(), user, AlertNotification{})
	if err == nil {
		t.Fatal("must surface sms error")
	}
	if !errors.Is(err, smsErr) {
		t.Fatalf("joined err must wrap sms err, got %v", err)
	}
	// Mail must still have been sent despite sms failing.
	if m.Count() != 1 {
		t.Fatal("mail must be sent independently of sms")
	}
	if len(sms.got) != 1 || sms.got[0] != "+1" {
		t.Fatalf("sms recipient = %v", sms.got)
	}
}

func TestRegister_ReplacesByName(t *testing.T) {
	mgr := NewManager()
	a := &recordingChannel{name: "x"}
	b := &recordingChannel{name: "x"}
	mgr.Register(a)
	mgr.Register(b)
	if mgr.Channel("x") != b {
		t.Fatal("re-registration must replace")
	}
}

type noMailNotification struct{}

func (noMailNotification) Channels() []string { return []string{"mail"} }

// no ToMail method → should error from MailChannel.

func TestMailChannel_RejectsNonMailNotification(t *testing.T) {
	mgr := NewManager()
	mgr.Register(NewMailChannel(mail.NewLogMailer()))
	err := mgr.Send(context.Background(), User{Email: "a@x"}, noMailNotification{})
	if err == nil {
		t.Fatal("must reject notification missing ToMail")
	}
}

func TestSendNow_AliasOfSend(t *testing.T) {
	m := mail.NewLogMailer()
	mgr := NewManager()
	mgr.Register(NewMailChannel(m))
	_ = mgr.SendNow(context.Background(), User{Email: "a@x"}, WelcomeNotification{})
	if m.Count() != 1 {
		t.Fatal("SendNow must deliver")
	}
}
