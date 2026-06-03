package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newMgr(t *testing.T) *Manager {
	t.Helper()
	store := NewMemoryStore(time.Hour)
	t.Cleanup(store.Close)
	return NewManager(store, Options{Insecure: true})
}

func TestStart_NewSessionWhenNoCookie(t *testing.T) {
	m := newMgr(t)
	req := httptest.NewRequest("GET", "/", nil)
	s, err := m.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !s.IsNew() {
		t.Fatal("must be new without cookie")
	}
	if s.ID() == "" {
		t.Fatal("ID must be assigned")
	}
}

func TestSaveAndReload_DataPersists(t *testing.T) {
	m := newMgr(t)
	ctx := context.Background()

	// First request: set a value and save.
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s, _ := m.Start(ctx, req)
	s.Put("user_id", uint64(42))
	if err := s.Save(ctx, w); err != nil {
		t.Fatalf("Save: %v", err)
	}
	ck := w.Result().Cookies()[0]
	if ck.Name != "lagodev_session" || ck.Value == "" {
		t.Fatalf("cookie = %+v", ck)
	}

	// Second request: cookie carries the ID; data must come back.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.AddCookie(ck)
	s2, _ := m.Start(ctx, req2)
	if s2.IsNew() {
		t.Fatal("reload must not be new")
	}
	v, ok := s2.Get("user_id")
	if !ok || v.(uint64) != 42 {
		t.Fatalf("reload value = (%v, %v)", v, ok)
	}
}

func TestCookieAttributes_SafeDefaults(t *testing.T) {
	m := NewManager(NewMemoryStore(time.Hour), Options{}) // Secure=true default
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s, _ := m.Start(context.Background(), req)
	_ = s.Save(context.Background(), w)
	ck := w.Result().Cookies()[0]
	if !ck.HttpOnly {
		t.Fatal("HttpOnly default")
	}
	if !ck.Secure {
		t.Fatal("Secure default")
	}
	if ck.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v", ck.SameSite)
	}
}

func TestForget_RemovesKey(t *testing.T) {
	m := newMgr(t)
	ctx := context.Background()
	req := httptest.NewRequest("GET", "/", nil)
	s, _ := m.Start(ctx, req)
	s.Put("a", 1)
	s.Forget("a")
	if _, ok := s.Get("a"); ok {
		t.Fatal("Forget must remove")
	}
}

func TestFlush_ClearsData(t *testing.T) {
	m := newMgr(t)
	ctx := context.Background()
	req := httptest.NewRequest("GET", "/", nil)
	s, _ := m.Start(ctx, req)
	s.Put("a", 1)
	s.Put("b", 2)
	s.Flush()
	if len(s.All()) != 0 {
		t.Fatal("Flush must clear")
	}
}

func TestRegenerate_ChangesIDPreservesData(t *testing.T) {
	m := newMgr(t)
	ctx := context.Background()
	req := httptest.NewRequest("GET", "/", nil)
	s, _ := m.Start(ctx, req)
	s.Put("user_id", uint64(7))
	oldID := s.ID()
	if err := s.Regenerate(ctx); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if s.ID() == oldID {
		t.Fatal("ID must change after Regenerate")
	}
	if v, _ := s.Get("user_id"); v.(uint64) != 7 {
		t.Fatal("data must survive Regenerate")
	}
}

func TestDestroy_RemovesAndExpiresCookie(t *testing.T) {
	m := newMgr(t)
	ctx := context.Background()

	// Establish a session
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s, _ := m.Start(ctx, req)
	s.Put("x", "y")
	_ = s.Save(ctx, w)
	ck := w.Result().Cookies()[0]

	// Now destroy on the next request
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.AddCookie(ck)
	w2 := httptest.NewRecorder()
	s2, _ := m.Start(ctx, req2)
	if err := s2.Destroy(ctx, w2); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	clearCk := w2.Result().Cookies()[0]
	if clearCk.MaxAge >= 0 {
		t.Fatalf("destroy cookie must expire, got MaxAge=%d", clearCk.MaxAge)
	}

	// And the store must no longer hold it
	req3 := httptest.NewRequest("GET", "/", nil)
	req3.AddCookie(ck)
	s3, _ := m.Start(ctx, req3)
	if !s3.IsNew() {
		t.Fatal("after Destroy, reload should be a new session")
	}
}

func TestStaleCookie_CreatesFreshSession(t *testing.T) {
	m := newMgr(t)
	ctx := context.Background()
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "lagodev_session", Value: "stale-id-with-no-data"})
	s, _ := m.Start(ctx, req)
	if !s.IsNew() {
		t.Fatal("stale cookie must produce a fresh session")
	}
}

func TestGetString_ConvenienceTyping(t *testing.T) {
	m := newMgr(t)
	ctx := context.Background()
	req := httptest.NewRequest("GET", "/", nil)
	s, _ := m.Start(ctx, req)
	s.Put("name", "Ada")
	s.Put("age", 30) // wrong type for GetString
	if s.GetString("name") != "Ada" {
		t.Fatalf("name = %q", s.GetString("name"))
	}
	if s.GetString("age") != "" {
		t.Fatal("wrong-type GetString must return empty")
	}
	if s.GetString("missing") != "" {
		t.Fatal("missing must return empty")
	}
}

func TestStore_ExpiresEntries(t *testing.T) {
	store := NewMemoryStore(40 * time.Millisecond)
	defer store.Close()
	m := NewManager(store, Options{Insecure: true, TTL: 40 * time.Millisecond})
	ctx := context.Background()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s, _ := m.Start(ctx, req)
	s.Put("x", "y")
	_ = s.Save(ctx, w)
	ck := w.Result().Cookies()[0]

	time.Sleep(80 * time.Millisecond)

	req2 := httptest.NewRequest("GET", "/", nil)
	req2.AddCookie(ck)
	s2, _ := m.Start(ctx, req2)
	if !s2.IsNew() {
		t.Fatal("expired session must reload as new")
	}
}
