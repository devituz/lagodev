package guard_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/devituz/lagodev/auth/guard"
	"github.com/devituz/lagodev/session"
	"golang.org/x/crypto/bcrypt"
)

// exampleUser is a minimal user model satisfying guard.User.
type exampleUser struct {
	id   string
	hash string
}

func (u exampleUser) AuthID() string           { return u.id }
func (u exampleUser) AuthPasswordHash() string { return u.hash }

// exampleProvider is a tiny in-memory guard.UserProvider.
type exampleProvider struct {
	byID   map[string]guard.User
	byCred map[string]guard.User
}

func (p *exampleProvider) FindByID(_ context.Context, id string) (guard.User, bool, error) {
	u, ok := p.byID[id]
	return u, ok, nil
}

func (p *exampleProvider) FindByCredentials(_ context.Context, identifier string) (guard.User, bool, error) {
	u, ok := p.byCred[identifier]
	return u, ok, nil
}

// Example shows the session guard end to end: Attempt with correct credentials
// logs the user in (Check()==true, User() returns the model), and a subsequent
// Logout clears the session (Check()==false). Requests are driven through the
// session middleware so a real session cookie carries the login across calls.
func Example() {
	hash, _ := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	user := exampleUser{id: "u1", hash: string(hash)}
	prov := &exampleProvider{
		byID:   map[string]guard.User{"u1": user},
		byCred: map[string]guard.User{"alice@example.com": user},
	}

	store := session.NewMemoryStore(time.Hour)
	defer store.Close()
	mgr := session.NewManager(store, session.Options{Insecure: true})
	g := guard.New(mgr, prov, guard.Options{})

	// run executes fn inside a request that has a session attached (via the
	// session middleware) and returns the session cookie set on the response.
	run := func(in *http.Cookie, fn func(w http.ResponseWriter, r *http.Request)) *http.Cookie {
		rec := httptest.NewRecorder()
		h := mgr.Middleware()(http.HandlerFunc(fn))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if in != nil {
			req.AddCookie(in)
		}
		h.ServeHTTP(rec, req)
		for _, c := range rec.Result().Cookies() {
			if c.Name == "lagodev_session" {
				return c
			}
		}
		return in
	}

	// 1. Log in with correct credentials.
	cookie := run(nil, func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret"); err != nil {
			panic(err)
		}
	})

	// 2. Next request (carrying the cookie): authenticated.
	cookie = run(cookie, func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("check after login:", g.Check(r))
		u, err := g.User(r.Context(), r)
		if err != nil {
			panic(err)
		}
		fmt.Println("user id:", u.AuthID())
	})

	// 3. Log out.
	cookie = run(cookie, func(w http.ResponseWriter, r *http.Request) {
		if err := g.Logout(r.Context(), w, r); err != nil {
			panic(err)
		}
	})

	// 4. Next request: no longer authenticated.
	run(cookie, func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("check after logout:", g.Check(r))
	})

	// Output:
	// check after login: true
	// user id: u1
	// check after logout: false
}
