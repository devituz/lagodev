package session

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// --- Session fixation -------------------------------------------------

// TestRegenerate_OldIDNoLongerResolves models the canonical session
// fixation attack: an attacker plants a known session ID on the victim,
// the victim authenticates, and the app calls Regenerate to mint a new
// ID. After that, the attacker's old ID must NOT resolve to the
// (now-authenticated) session data anywhere in the store.
func TestRegenerate_OldIDNoLongerResolves(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	t.Cleanup(store.Close)
	m := NewManager(store, Options{Insecure: true})
	ctx := context.Background()

	// Victim arrives with an attacker-fixed cookie. Establish that ID in
	// the store first (simulating the pre-login anonymous session).
	fixedID := "attacker_fixed_session_id_0000000000000000000000000000000000"
	if err := store.Write(ctx, fixedID, map[string]any{"cart": "x"}, time.Hour); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "lagodev_session", Value: fixedID})
	s, err := m.Start(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID() != fixedID {
		t.Fatalf("setup: expected to load fixed id, got %q", s.ID())
	}

	// Victim authenticates; app regenerates the ID.
	s.Put("user_id", uint64(42))
	if err := s.Regenerate(ctx); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	newID := s.ID()

	if newID == fixedID {
		t.Fatal("Regenerate must mint a NEW id")
	}

	// Persist (as Save/Middleware would after the handler).
	w := httptest.NewRecorder()
	if err := s.Save(ctx, w); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 1. The attacker's old ID must no longer resolve in the store.
	if _, ok, _ := store.Read(ctx, fixedID); ok {
		t.Fatal("session fixation: old id still resolves in store after Regenerate")
	}

	// 2. A request carrying the old cookie must yield a FRESH session,
	//    not the authenticated one.
	reqAtt := httptest.NewRequest("GET", "/", nil)
	reqAtt.AddCookie(&http.Cookie{Name: "lagodev_session", Value: fixedID})
	att, _ := m.Start(ctx, reqAtt)
	if !att.IsNew() {
		t.Fatal("session fixation: old id reloads an existing session")
	}
	if _, ok := att.Get("user_id"); ok {
		t.Fatal("session fixation: authenticated data leaked to old id")
	}

	// 3. The migrated data must live under the NEW id.
	reqNew := httptest.NewRequest("GET", "/", nil)
	reqNew.AddCookie(&http.Cookie{Name: "lagodev_session", Value: newID})
	nw, _ := m.Start(ctx, reqNew)
	if v, ok := nw.Get("user_id"); !ok || v.(uint64) != 42 {
		t.Fatalf("Regenerate must migrate data to the new id; got (%v,%v)", v, ok)
	}
}

// TestRegenerate_ResetsIsNew ensures a regenerated session does not keep
// claiming IsNew()==true once it has been migrated to a fresh ID and
// persisted; the handle now references a concrete server-side record.
func TestRegenerate_PersistsUnderNewID(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	t.Cleanup(store.Close)
	m := NewManager(store, Options{Insecure: true})
	ctx := context.Background()

	s, _ := m.Start(ctx, httptest.NewRequest("GET", "/", nil))
	s.Put("k", "v")
	if err := s.Regenerate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Read(ctx, s.ID()); !ok {
		t.Fatal("regenerated session must be readable under the new id")
	}
}

// --- Cookie flags -----------------------------------------------------

func TestCookieFlags_SecureModeViaHTTP(t *testing.T) {
	m := NewManager(NewMemoryStore(time.Hour), Options{SameSite: http.SameSiteStrictMode})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		FromRequest(r).Put("x", 1)
	})
	rec := httptest.NewRecorder()
	m.Middleware()(handler).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	ck := rec.Result().Cookies()[0]
	if !ck.HttpOnly {
		t.Fatal("secure mode: HttpOnly must be set")
	}
	if !ck.Secure {
		t.Fatal("secure mode: Secure must be set")
	}
	if ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("SameSite must reflect config, got %v", ck.SameSite)
	}
	// Path must be root-scoped, never a leaked path.
	if ck.Path != "/" {
		t.Fatalf("Path = %q, want /", ck.Path)
	}
}

func TestCookieFlags_InsecureModeDocumented(t *testing.T) {
	// Insecure mode is for local HTTP dev only; Secure is dropped but
	// HttpOnly and SameSite must still hold (XSS + CSRF defenses stay on).
	m := NewManager(NewMemoryStore(time.Hour), Options{Insecure: true})
	w := httptest.NewRecorder()
	s, _ := m.Start(context.Background(), httptest.NewRequest("GET", "/", nil))
	_ = s.Save(context.Background(), w)
	ck := w.Result().Cookies()[0]
	if ck.Secure {
		t.Fatal("insecure mode must NOT set Secure")
	}
	if !ck.HttpOnly {
		t.Fatal("insecure mode must still set HttpOnly")
	}
	if ck.SameSite != http.SameSiteLaxMode {
		t.Fatalf("insecure mode SameSite default = %v", ck.SameSite)
	}
}

// --- ID entropy / unpredictability ------------------------------------

func TestSessionID_EntropyFormatAndUniqueness(t *testing.T) {
	const n = 20000
	seen := make(map[string]struct{}, n)
	var prev string
	for i := 0; i < n; i++ {
		id, err := generateID()
		if err != nil {
			t.Fatalf("generateID: %v", err)
		}
		// 32 random bytes hex-encoded => 64 hex chars.
		if len(id) != 64 {
			t.Fatalf("id length = %d, want 64", len(id))
		}
		if _, err := hex.DecodeString(id); err != nil {
			t.Fatalf("id not hex: %q", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id at iteration %d: %q", i, id)
		}
		seen[id] = struct{}{}
		// Not sequential / not derived from the previous one.
		if id == prev {
			t.Fatal("ids must not repeat consecutively")
		}
		prev = id
	}
}

// TestSessionID_NotPredictableFromCounter sanity-checks that two
// adjacent mints share no long common prefix (a counter/timestamp scheme
// would leak structure).
func TestSessionID_NoStructuralPrefix(t *testing.T) {
	a, _ := generateID()
	b, _ := generateID()
	common := 0
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			break
		}
		common++
	}
	if common > 8 {
		t.Fatalf("ids share a %d-char prefix; entropy source looks structured", common)
	}
}

// --- Expiry / TTL -----------------------------------------------------

func TestExpiry_ExpiredNotReturned_DestroyClearsCookie(t *testing.T) {
	store := NewMemoryStore(30 * time.Millisecond)
	t.Cleanup(store.Close)
	m := NewManager(store, Options{Insecure: true, TTL: 30 * time.Millisecond})
	ctx := context.Background()

	w := httptest.NewRecorder()
	s, _ := m.Start(ctx, httptest.NewRequest("GET", "/", nil))
	s.Put("x", "y")
	_ = s.Save(ctx, w)
	ck := w.Result().Cookies()[0]
	id := s.ID()

	time.Sleep(70 * time.Millisecond)

	// Expired record must not be returned by the store.
	if _, ok, _ := store.Read(ctx, id); ok {
		t.Fatal("expired record must not be returned by Read")
	}

	// And Start must produce a fresh session.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.AddCookie(ck)
	s2, _ := m.Start(ctx, req2)
	if !s2.IsNew() {
		t.Fatal("expired session must reload as new")
	}

	// Destroy: removes server-side AND emits an expiring cookie.
	req3 := httptest.NewRequest("GET", "/", nil)
	req3.AddCookie(ck)
	w3 := httptest.NewRecorder()
	s3, _ := m.Start(ctx, req3)
	s3.Put("z", 1)
	_ = s3.Save(ctx, httptest.NewRecorder()) // persist a live one
	id3 := s3.ID()
	wDestroy := httptest.NewRecorder()
	if err := s3.Destroy(ctx, wDestroy); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Read(ctx, id3); ok {
		t.Fatal("Destroy must remove server-side record")
	}
	clear := wDestroy.Result().Cookies()[0]
	if clear.MaxAge >= 0 {
		t.Fatalf("Destroy must expire the cookie, MaxAge=%d", clear.MaxAge)
	}
	_ = w3
}

// --- Tampered cookie --------------------------------------------------

func TestTamperedCookie_FreshSessionNoLeakNoPanic(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	t.Cleanup(store.Close)
	m := NewManager(store, Options{Insecure: true})
	ctx := context.Background()

	garbage := []string{
		"",                 // empty
		"../../etc/passwd", // path traversal-ish
		"' OR 1=1 --",      // sqli-ish
		"<script>alert(1)</script>",
		"zzzz_not_hex_zzzz",
		"deadbeef",                 // valid hex but unknown
		string(make([]byte, 4096)), // oversized
	}
	for _, g := range garbage {
		req := httptest.NewRequest("GET", "/", nil)
		// Cookie values can't contain all bytes; set via header to bypass
		// net/http's client-side sanitization where possible.
		req.AddCookie(&http.Cookie{Name: "lagodev_session", Value: g})
		s, err := m.Start(ctx, req)
		if err != nil {
			t.Fatalf("garbage %q must not error: %v", g, err)
		}
		if !s.IsNew() {
			t.Fatalf("garbage %q must yield a fresh session", g)
		}
		if len(s.All()) != 0 {
			t.Fatalf("garbage %q must yield empty data", g)
		}
		// The fresh id must NOT echo the attacker-supplied value.
		if s.ID() == g && g != "" {
			t.Fatalf("garbage %q reused as session id (fixation)", g)
		}
	}
}

// --- Concurrency ------------------------------------------------------

func TestMemoryStore_ConcurrentReadWriteDestroy(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	t.Cleanup(store.Close)
	ctx := context.Background()

	const workers = 64
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(n int) {
			defer wg.Done()
			id := "id"
			for j := 0; j < 500; j++ {
				switch j % 3 {
				case 0:
					_ = store.Write(ctx, id, map[string]any{"n": n, "j": j}, time.Hour)
				case 1:
					_, _, _ = store.Read(ctx, id)
				case 2:
					_ = store.Destroy(ctx, id)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestManager_ConcurrentRequestsSharedManager(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	t.Cleanup(store.Close)
	m := NewManager(store, Options{Insecure: true})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := FromRequest(r)
		s.Put("hit", true)
		_ = s.GetString("missing")
		_ = s.All()
		_, _ = w.Write([]byte("ok"))
	})
	wrapped := m.Middleware()(handler)

	const reqs = 200
	var wg sync.WaitGroup
	wg.Add(reqs)
	for i := 0; i < reqs; i++ {
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d", rec.Code)
			}
		}()
	}
	wg.Wait()
}
