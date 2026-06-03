package session

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMiddleware_PopulatesSessionAndCookie(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	defer store.Close()
	m := NewManager(store, Options{Insecure: true})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := FromRequest(r)
		if s == nil {
			t.Fatal("FromRequest must return a *Session")
		}
		s.Put("user_id", uint64(42))
		w.WriteHeader(http.StatusOK)
	})
	wrapped := m.Middleware()(handler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "lagodev_session" {
		t.Fatalf("expected session cookie, got %+v", cookies)
	}
	if cookies[0].Value == "" {
		t.Fatal("cookie value empty")
	}
}

func TestMiddleware_SessionDataPersists(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	defer store.Close()
	m := NewManager(store, Options{Insecure: true})

	setHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		FromRequest(r).Put("name", "Ada")
	})
	getHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := FromRequest(r).GetString("name"); v != "Ada" {
			t.Fatalf("second request got %q, want Ada", v)
		}
	})

	mw := m.Middleware()

	// First request — write
	rec1 := httptest.NewRecorder()
	mw(setHandler).ServeHTTP(rec1, httptest.NewRequest("GET", "/", nil))
	ck := rec1.Result().Cookies()[0]

	// Second request — read with cookie
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.AddCookie(ck)
	rec2 := httptest.NewRecorder()
	mw(getHandler).ServeHTTP(rec2, req2)
}

func TestMiddleware_CookieSentBeforeBody(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	defer store.Close()
	m := NewManager(store, Options{Insecure: true})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		FromRequest(r).Put("hit", true)
		// Write body — Set-Cookie must have been emitted already.
		_, _ = w.Write([]byte("hello"))
	})
	rec := httptest.NewRecorder()
	m.Middleware()(handler).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if !strings.Contains(strings.Join(rec.Result().Header["Set-Cookie"], ";"), "lagodev_session=") {
		t.Fatalf("Set-Cookie missing; headers=%v", rec.Header())
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestFromRequest_NilWhenMiddlewareAbsent(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if FromRequest(req) != nil {
		t.Fatal("must be nil without middleware")
	}
}
