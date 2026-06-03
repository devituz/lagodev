package mock

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"
)

// --- Clock --------------------------------------------------------------

func TestClock_AdvanceAndSet(t *testing.T) {
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	c := NewClock(start)
	if !c.Now().Equal(start) {
		t.Fatal("initial Now mismatch")
	}
	c.Advance(time.Hour)
	if c.Now().Sub(start) != time.Hour {
		t.Fatalf("after +1h: %s", c.Now())
	}
	c.Advance(-30 * time.Minute)
	if c.Now().Sub(start) != 30*time.Minute {
		t.Fatalf("after -30m: %s", c.Now())
	}
	jump := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Set(jump)
	if !c.Now().Equal(jump) {
		t.Fatalf("Set: %s", c.Now())
	}
}

// --- Calls --------------------------------------------------------------

type sendCall struct{ To, Subject string }

type fakeT struct {
	fatalfMsgs []string
}

func (f *fakeT) Helper() {}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.fatalfMsgs = append(f.fatalfMsgs, format)
}

func TestCalls_Recording(t *testing.T) {
	c := NewCalls[sendCall]()
	c.Record(sendCall{"a@x", "Hi"})
	c.Record(sendCall{"b@x", "Bye"})
	if c.Count() != 2 {
		t.Fatalf("Count = %d", c.Count())
	}
	if c.At(0).To != "a@x" || c.Last().Subject != "Bye" {
		t.Fatalf("ordering broken: %+v", c.All())
	}
	all := c.All()
	all[0].To = "MUTATED"
	if c.At(0).To == "MUTATED" {
		t.Fatal("All() must return a copy, not a live slice")
	}
}

func TestCalls_AssertCount_FailsOnMismatch(t *testing.T) {
	c := NewCalls[sendCall]()
	c.Record(sendCall{})
	ft := &fakeT{}
	c.AssertCount(ft, 5)
	if len(ft.fatalfMsgs) != 1 {
		t.Fatalf("expected Fatalf to fire once, got %d", len(ft.fatalfMsgs))
	}
}

func TestCalls_AssertCount_PassesOnMatch(t *testing.T) {
	c := NewCalls[sendCall]()
	c.Record(sendCall{})
	ft := &fakeT{}
	c.AssertCount(ft, 1)
	if len(ft.fatalfMsgs) != 0 {
		t.Fatalf("Fatalf should not fire, got %v", ft.fatalfMsgs)
	}
}

func TestCalls_AssertCalledAndNotCalled(t *testing.T) {
	c := NewCalls[sendCall]()
	ft := &fakeT{}
	c.AssertCalled(ft) // empty → fail
	if len(ft.fatalfMsgs) != 1 {
		t.Fatal("AssertCalled on empty must fail")
	}
	c.Record(sendCall{})
	ft2 := &fakeT{}
	c.AssertNotCalled(ft2) // non-empty → fail
	if len(ft2.fatalfMsgs) != 1 {
		t.Fatal("AssertNotCalled on non-empty must fail")
	}
}

func TestCalls_Reset(t *testing.T) {
	c := NewCalls[sendCall]()
	c.Record(sendCall{})
	c.Reset()
	if c.Count() != 0 {
		t.Fatal("Reset must clear")
	}
}

// --- HTTPServer ---------------------------------------------------------

func TestHTTPServer_RouteAndRecord(t *testing.T) {
	srv := NewHTTPServer()
	defer srv.Close()

	srv.On("GET", "/users/42").
		Header("X-Custom", "1").
		Reply(200, []byte(`{"id":42}`))

	resp, err := http.Get(srv.URL() + "/users/42")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Custom") != "1" {
		t.Fatal("custom header missing")
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"id":42}` {
		t.Fatalf("body = %q", body)
	}
	if srv.LastRequest().Path != "/users/42" {
		t.Fatalf("LastRequest = %+v", srv.LastRequest())
	}
}

func TestHTTPServer_UnknownRouteIs501(t *testing.T) {
	srv := NewHTTPServer()
	defer srv.Close()
	resp, err := http.Get(srv.URL() + "/missing")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}
}

func TestHTTPServer_OnJSONShorthand(t *testing.T) {
	srv := NewHTTPServer()
	defer srv.Close()
	if err := srv.OnJSON("POST", "/echo", 201, map[string]any{"ok": true}); err != nil {
		t.Fatalf("OnJSON: %v", err)
	}
	resp, err := http.Post(srv.URL()+"/echo", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}
}

func TestHTTPServer_RequestsRecorded(t *testing.T) {
	srv := NewHTTPServer()
	defer srv.Close()
	srv.On("POST", "/echo").Reply(204, nil)

	_, _ = http.Post(srv.URL()+"/echo?ref=mock", "application/json", bytes.NewReader([]byte(`{"x":1}`)))
	got := srv.Requests()
	if len(got) != 1 {
		t.Fatalf("Requests count = %d", len(got))
	}
	if got[0].Method != "POST" || got[0].Query != "ref=mock" {
		t.Fatalf("recorded = %+v", got[0])
	}
	if string(got[0].Body) != `{"x":1}` {
		t.Fatalf("body = %q", got[0].Body)
	}
}

func TestHTTPServer_Reset(t *testing.T) {
	srv := NewHTTPServer()
	defer srv.Close()
	srv.On("GET", "/x").Reply(200, nil)
	_, _ = http.Get(srv.URL() + "/x")
	srv.Reset()
	if len(srv.Requests()) != 0 {
		t.Fatal("Reset must clear recorded requests")
	}
	// Route survives Reset
	resp, err := http.Get(srv.URL() + "/x")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("route lost after Reset: err=%v status=%d", err, resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Body utilities -----------------------------------------------------

func TestBodyJSON_DecodesAndFlagsEmpty(t *testing.T) {
	var dst struct {
		X int `json:"x"`
	}
	if err := BodyJSON([]byte(`{"x":7}`), &dst); err != nil || dst.X != 7 {
		t.Fatalf("decode: dst=%+v err=%v", dst, err)
	}
	if err := BodyJSON(nil, &dst); err == nil {
		t.Fatal("empty body must error")
	}
}

func TestMustReadBody_NilReturnsEmpty(t *testing.T) {
	if got := MustReadBody(nil); got != nil {
		t.Fatalf("nil reader = %q", got)
	}
}
