package guard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/devituz/lagodev/session"
	"golang.org/x/crypto/bcrypt"
)

// ---- fakes -----------------------------------------------------------------

type fakeUser struct {
	id   string
	hash string
}

func (u fakeUser) AuthID() string           { return u.id }
func (u fakeUser) AuthPasswordHash() string { return u.hash }

// fakeProvider indexes users by id and by login identifier.
type fakeProvider struct {
	byID    map[string]User
	byCred  map[string]User
	failID  bool // make FindByID return an infra error
	failCre bool // make FindByCredentials return an infra error
}

func (p *fakeProvider) FindByID(_ context.Context, id string) (User, bool, error) {
	if p.failID {
		return nil, false, errors.New("boom")
	}
	u, ok := p.byID[id]
	return u, ok, nil
}

func (p *fakeProvider) FindByCredentials(_ context.Context, identifier string) (User, bool, error) {
	if p.failCre {
		return nil, false, errors.New("boom")
	}
	u, ok := p.byCred[identifier]
	return u, ok, nil
}

func mustHash(t *testing.T, plain string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return string(h)
}

// newGuard wires a guard with a MemoryStore session manager and a provider
// holding a single user (id="u1", login="alice@example.com", pw="s3cret").
func newGuard(t *testing.T, opts Options) (*Guard, *session.Manager, fakeUser) {
	t.Helper()
	store := session.NewMemoryStore(time.Hour)
	t.Cleanup(store.Close)
	mgr := session.NewManager(store, session.Options{Insecure: true})
	u := fakeUser{id: "u1", hash: mustHash(t, "s3cret")}
	prov := &fakeProvider{
		byID:   map[string]User{"u1": u},
		byCred: map[string]User{"alice@example.com": u},
	}
	return New(mgr, prov, opts), mgr, u
}

// runWithSession runs fn inside a request that has a started session attached
// and the session is saved to the store afterwards (mimicking the session
// middleware). It returns the resulting Set-Cookie session ID.
func runWithSession(t *testing.T, mgr *session.Manager, cookie *http.Cookie, fn func(w http.ResponseWriter, r *http.Request)) (*http.Cookie, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	var captured *http.Cookie
	h := mgr.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fn(w, r)
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	h.ServeHTTP(rec, r)
	for _, c := range rec.Result().Cookies() {
		if c.Name == "lagodev_session" {
			captured = c
		}
	}
	return captured, rec
}

// ---- Attempt ---------------------------------------------------------------

func TestAttempt_Success(t *testing.T) {
	g, mgr, u := newGuard(t, Options{})
	var got User
	var err error
	c, _ := runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		got, err = g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret")
	})
	if err != nil {
		t.Fatalf("Attempt: %v", err)
	}
	if got.AuthID() != u.id {
		t.Fatalf("returned user = %q, want %q", got.AuthID(), u.id)
	}
	if c == nil || c.Value == "" {
		t.Fatal("expected a session cookie after login")
	}
	// Follow-up request with the cookie must resolve the user.
	_, _ = runWithSession(t, mgr, c, func(w http.ResponseWriter, r *http.Request) {
		if !g.Check(r) {
			t.Error("Check=false after login")
		}
		if id := g.ID(r); id != u.id {
			t.Errorf("ID=%q, want %q", id, u.id)
		}
		ru, e := g.User(r.Context(), r)
		if e != nil {
			t.Errorf("User: %v", e)
		} else if ru.AuthID() != u.id {
			t.Errorf("User id=%q, want %q", ru.AuthID(), u.id)
		}
	})
}

func TestAttempt_WrongPassword(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		_, err := g.Attempt(r.Context(), w, r, "alice@example.com", "wrong")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v, want ErrInvalidCredentials", err)
		}
		if g.Check(r) {
			t.Error("Check=true after failed attempt")
		}
	})
}

func TestAttempt_UnknownUser(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		_, err := g.Attempt(r.Context(), w, r, "nobody@example.com", "s3cret")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v, want ErrInvalidCredentials", err)
		}
	})
}

func TestAttempt_EmptyHashUser(t *testing.T) {
	store := session.NewMemoryStore(time.Hour)
	t.Cleanup(store.Close)
	mgr := session.NewManager(store, session.Options{Insecure: true})
	prov := &fakeProvider{
		byCred: map[string]User{"x@e.com": fakeUser{id: "x", hash: ""}},
	}
	g := New(mgr, prov, Options{})
	runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		_, err := g.Attempt(r.Context(), w, r, "x@e.com", "anything")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v, want ErrInvalidCredentials", err)
		}
	})
}

func TestAttempt_ProviderError(t *testing.T) {
	store := session.NewMemoryStore(time.Hour)
	t.Cleanup(store.Close)
	mgr := session.NewManager(store, session.Options{Insecure: true})
	g := New(mgr, &fakeProvider{failCre: true}, Options{})
	runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		_, err := g.Attempt(r.Context(), w, r, "a", "b")
		if err == nil || errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v, want wrapped infra error", err)
		}
	})
}

func TestAttempt_NoSession(t *testing.T) {
	g, _, _ := newGuard(t, Options{})
	// Request without the session middleware: no session attached.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	_, err := g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret")
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
}

// ---- Login / Logout --------------------------------------------------------

func TestLogin_RegeneratesSessionID(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	// First request: establish a session WITHOUT logging in, capture its ID.
	preCookie, _ := runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		session.FromRequest(r).Put("seed", "1")
	})
	if preCookie == nil {
		t.Fatal("no pre-login cookie")
	}
	// Second request carries that cookie and logs in: ID must change (fixation).
	postCookie, _ := runWithSession(t, mgr, preCookie, func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret"); err != nil {
			t.Fatalf("Attempt: %v", err)
		}
	})
	if postCookie == nil {
		t.Fatal("no post-login cookie")
	}
	if postCookie.Value == preCookie.Value {
		t.Fatalf("session ID unchanged on login: %q (fixation defense missing)", postCookie.Value)
	}
}

func TestLogout(t *testing.T) {
	g, mgr, u := newGuard(t, Options{})
	loginCookie, _ := runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret"); err != nil {
			t.Fatalf("Attempt: %v", err)
		}
	})
	// Logout on the authenticated session.
	postLogout, _ := runWithSession(t, mgr, loginCookie, func(w http.ResponseWriter, r *http.Request) {
		if id := g.ID(r); id != u.id {
			t.Fatalf("pre-logout ID=%q, want %q", id, u.id)
		}
		if err := g.Logout(r.Context(), w, r); err != nil {
			t.Fatalf("Logout: %v", err)
		}
		if g.Check(r) {
			t.Error("Check=true immediately after Logout")
		}
	})
	// Logout regenerates the ID, so the new cookie must differ.
	if postLogout != nil && postLogout.Value == loginCookie.Value {
		t.Error("session ID unchanged on logout")
	}
	// A fresh request with the (new) cookie must be unauthenticated.
	runWithSession(t, mgr, postLogout, func(w http.ResponseWriter, r *http.Request) {
		if g.Check(r) {
			t.Error("still authenticated after logout")
		}
		if _, err := g.User(r.Context(), r); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("User err = %v, want ErrUnauthenticated", err)
		}
	})
}

// ---- User / Check / ID resolution -----------------------------------------

func TestUser_Unauthenticated(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		if g.Check(r) {
			t.Error("Check=true on fresh session")
		}
		if g.ID(r) != "" {
			t.Error("ID non-empty on fresh session")
		}
		if _, err := g.User(r.Context(), r); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("User err = %v, want ErrUnauthenticated", err)
		}
	})
}

func TestUser_VanishedAccount(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	loginCookie, _ := runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret"); err != nil {
			t.Fatalf("Attempt: %v", err)
		}
	})
	// Delete the user from the provider, then resolve: id present but no user.
	prov := g.provider.(*fakeProvider)
	delete(prov.byID, "u1")
	runWithSession(t, mgr, loginCookie, func(w http.ResponseWriter, r *http.Request) {
		// ID/Check still see the stored id (cheap path, no provider hit)...
		if g.ID(r) != "u1" {
			t.Error("ID lost the stored id")
		}
		// ...but User must report unauthenticated.
		if _, err := g.User(r.Context(), r); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("User err = %v, want ErrUnauthenticated", err)
		}
	})
}

func TestUser_CachedPerRequest(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	loginCookie, _ := runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret"); err != nil {
			t.Fatalf("Attempt: %v", err)
		}
	})
	rec := httptest.NewRecorder()
	// Use the guard Middleware so the cache box is installed.
	var calls int
	prov := g.provider.(*fakeProvider)
	wrapped := &countingProvider{inner: prov, calls: &calls}
	g.provider = wrapped
	h := mgr.Middleware()(g.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 3; i++ {
			if _, err := g.User(r.Context(), r); err != nil {
				t.Errorf("User: %v", err)
			}
		}
	})))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(loginCookie)
	h.ServeHTTP(rec, r)
	// Middleware resolves once; the 3 handler calls hit the cache.
	if calls != 1 {
		t.Fatalf("FindByID calls = %d, want 1 (cached per request)", calls)
	}
}

type countingProvider struct {
	inner UserProvider
	calls *int
}

func (c *countingProvider) FindByID(ctx context.Context, id string) (User, bool, error) {
	*c.calls++
	return c.inner.FindByID(ctx, id)
}

func (c *countingProvider) FindByCredentials(ctx context.Context, identifier string) (User, bool, error) {
	return c.inner.FindByCredentials(ctx, identifier)
}

// ---- Middleware ------------------------------------------------------------

func TestMiddleware_Allow(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	loginCookie, _ := runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret"); err != nil {
			t.Fatalf("Attempt: %v", err)
		}
	})
	var reached bool
	h := mgr.Middleware()(g.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	})))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(loginCookie)
	h.ServeHTTP(rec, r)
	if !reached {
		t.Fatal("handler not reached for authenticated request")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204", rec.Code)
	}
}

func TestMiddleware_Deny401(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	h := mgr.Middleware()(g.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler reached for unauthenticated request")
	})))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestMiddleware_RedirectWhenLoginPath(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{LoginPath: "/login"})
	h := mgr.Middleware()(g.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler reached")
	})))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Fatalf("Location = %q, want /login", loc)
	}
}

// ---- Guest -----------------------------------------------------------------

func TestGuest_AllowsAnonymous(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	var reached bool
	h := mgr.Middleware()(g.Guest()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	})))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, r)
	if !reached {
		t.Fatal("guest handler not reached for anonymous request")
	}
}

func TestGuest_BlocksAuthenticated(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	loginCookie, _ := runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret"); err != nil {
			t.Fatalf("Attempt: %v", err)
		}
	})
	h := mgr.Middleware()(g.Guest()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("guest handler reached for authenticated request")
	})))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(loginCookie)
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}

func TestGuest_RedirectWhenHomePath(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{HomePath: "/dashboard"})
	loginCookie, _ := runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret"); err != nil {
			t.Fatalf("Attempt: %v", err)
		}
	})
	h := mgr.Middleware()(g.Guest()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("guest handler reached")
	})))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(loginCookie)
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard" {
		t.Fatalf("Location = %q, want /dashboard", loc)
	}
}

// ---- custom Hasher ---------------------------------------------------------

type plainHasher struct{}

func (plainHasher) Verify(hash, plain string) bool { return hash == plain }

func TestCustomHasher(t *testing.T) {
	store := session.NewMemoryStore(time.Hour)
	t.Cleanup(store.Close)
	mgr := session.NewManager(store, session.Options{Insecure: true})
	prov := &fakeProvider{
		byCred: map[string]User{"a@e.com": fakeUser{id: "a", hash: "plaintextpw"}},
		byID:   map[string]User{"a": fakeUser{id: "a", hash: "plaintextpw"}},
	}
	g := New(mgr, prov, Options{Hasher: plainHasher{}})
	runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Attempt(r.Context(), w, r, "a@e.com", "plaintextpw"); err != nil {
			t.Fatalf("Attempt with custom hasher: %v", err)
		}
	})
}

// ---- Login / Logout without a session -------------------------------------

func TestLogin_NoSession(t *testing.T) {
	g, _, u := newGuard(t, Options{})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	if err := g.Login(r.Context(), w, r, u); !errors.Is(err, ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
}

func TestLogout_NoSession(t *testing.T) {
	g, _, _ := newGuard(t, Options{})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	if err := g.Logout(r.Context(), w, r); !errors.Is(err, ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
}

// ---- ID / Check without a session ------------------------------------------

func TestIDAndCheck_NoSession(t *testing.T) {
	g, _, _ := newGuard(t, Options{})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if id := g.ID(r); id != "" {
		t.Errorf("ID = %q, want empty without a session", id)
	}
	if g.Check(r) {
		t.Error("Check = true without a session")
	}
}

// ---- User: infra error must surface and must NOT be negatively cached ------

func TestUser_InfraError(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	loginCookie, _ := runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret"); err != nil {
			t.Fatalf("Attempt: %v", err)
		}
	})
	prov := g.provider.(*fakeProvider)
	prov.failID = true
	calls := 0
	counting := &countingProvider{inner: prov, calls: &calls}
	g.provider = counting
	// Drive through the guard Middleware so the cache box is installed; a
	// transient infra error must NOT be memoised, so a second User call
	// re-queries the provider (no poisoned negative cache).
	h := mgr.Middleware()(g.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler reached despite infra error")
	})))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(loginCookie)
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401 on infra error (fail-closed)", rec.Code)
	}

	// Now confirm the error is NOT cached: within a single request (with a
	// cache box installed) two User calls both re-hit the provider, because
	// an infra error is never memoised.
	calls = 0
	rec2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.AddCookie(loginCookie)
	mgr.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, rr *http.Request) {
		rr = withCachedUser(rr)
		if _, err := g.User(rr.Context(), rr); err == nil || errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("first User err = %v, want wrapped infra error", err)
		}
		if _, err := g.User(rr.Context(), rr); err == nil {
			t.Fatal("second User unexpectedly succeeded")
		}
	})).ServeHTTP(rec2, r2)
	if calls < 2 {
		t.Fatalf("FindByID calls = %d, want >=2 (infra error must not be cached)", calls)
	}
}

// ---- User: vanished account negative cache avoids re-query -----------------

func TestUser_VanishedAccountCached(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	loginCookie, _ := runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret"); err != nil {
			t.Fatalf("Attempt: %v", err)
		}
	})
	prov := g.provider.(*fakeProvider)
	delete(prov.byID, "u1")
	calls := 0
	g.provider = &countingProvider{inner: prov, calls: &calls}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(loginCookie)
	r = withCachedUser(r)
	// Attach a session so ID resolves.
	rec := httptest.NewRecorder()
	mgr.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, rr *http.Request) {
		rr = withCachedUser(rr)
		for i := 0; i < 3; i++ {
			if _, err := g.User(rr.Context(), rr); !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("User err = %v, want ErrUnauthenticated", err)
			}
		}
	})).ServeHTTP(rec, r)
	if calls != 1 {
		t.Fatalf("FindByID calls = %d, want 1 (negative result cached per request)", calls)
	}
}

// ---- remember-me round-trip ------------------------------------------------

// memRemember is an in-memory RememberStore: token -> userID.
type memRemember struct {
	mu     sync.Mutex
	tokens map[string]string
	n      int
}

func (m *memRemember) Issue(_ context.Context, userID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tokens == nil {
		m.tokens = map[string]string{}
	}
	m.n++
	tok := "tok-" + userID + "-" + itoa(m.n)
	m.tokens[tok] = userID
	return tok, nil
}

func (m *memRemember) Lookup(_ context.Context, token string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.tokens[token]
	return id, ok, nil
}

func (m *memRemember) Forget(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, token)
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestRememberStore_RoundTrip(t *testing.T) {
	rem := &memRemember{}
	g, mgr, u := newGuard(t, Options{Remember: rem})

	if g.Remember() == nil {
		t.Fatal("Remember() = nil, want the configured store")
	}

	ctx := context.Background()
	// Issue a token for the user (as an app would on "remember me" login).
	tok, err := g.Remember().Issue(ctx, u.id)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Later, after the session cookie expired: look the token up and Login.
	id, ok, err := g.Remember().Lookup(ctx, tok)
	if err != nil || !ok {
		t.Fatalf("Lookup ok=%v err=%v", ok, err)
	}
	if id != u.id {
		t.Fatalf("Lookup id = %q, want %q", id, u.id)
	}
	loginCookie, _ := runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		ru, _, _ := g.provider.FindByID(r.Context(), id)
		if err := g.Login(r.Context(), w, r, ru); err != nil {
			t.Fatalf("Login from remember token: %v", err)
		}
	})
	// The re-established session must authenticate the user.
	runWithSession(t, mgr, loginCookie, func(w http.ResponseWriter, r *http.Request) {
		if !g.Check(r) {
			t.Error("Check=false after remember-me login")
		}
	})
	// Forget invalidates the token.
	if err := g.Remember().Forget(ctx, tok); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, ok, _ := g.Remember().Lookup(ctx, tok); ok {
		t.Error("token still resolves after Forget")
	}
}

func TestRemember_NilByDefault(t *testing.T) {
	g, _, _ := newGuard(t, Options{})
	if g.Remember() != nil {
		t.Fatal("Remember() should be nil when unset")
	}
}

// ---- default hasher is bcrypt ----------------------------------------------

func TestNew_DefaultsToBcrypt(t *testing.T) {
	g, _, _ := newGuard(t, Options{})
	if _, ok := g.hasher.(BcryptHasher); !ok {
		t.Fatalf("default hasher = %T, want BcryptHasher", g.hasher)
	}
	// BcryptHasher.Verify sanity: correct vs wrong.
	hash := mustHash(t, "pw")
	if !(BcryptHasher{}).Verify(hash, "pw") {
		t.Error("BcryptHasher.Verify rejected the correct password")
	}
	if (BcryptHasher{}).Verify(hash, "nope") {
		t.Error("BcryptHasher.Verify accepted a wrong password")
	}
}
