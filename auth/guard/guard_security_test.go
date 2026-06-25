package guard

import (
	"errors"
	"net/http"
	"testing"
)

// TestAttempt_NoEnumeration proves an unknown identifier and a wrong password
// collapse to the SAME ErrInvalidCredentials, so an attacker cannot enumerate
// which accounts exist.
func TestAttempt_NoEnumeration(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	var unknownErr, wrongPwErr error
	runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		_, unknownErr = g.Attempt(r.Context(), w, r, "ghost@example.com", "whatever")
	})
	runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		_, wrongPwErr = g.Attempt(r.Context(), w, r, "alice@example.com", "wrong-password")
	})
	if !errors.Is(unknownErr, ErrInvalidCredentials) {
		t.Fatalf("unknown user err = %v, want ErrInvalidCredentials", unknownErr)
	}
	if !errors.Is(wrongPwErr, ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v, want ErrInvalidCredentials", wrongPwErr)
	}
	if unknownErr.Error() != wrongPwErr.Error() {
		t.Fatalf("errors distinguishable: %q vs %q", unknownErr, wrongPwErr)
	}
}

// TestAttempt_EmptyHashRejected proves a user whose stored hash is empty (an
// SSO-only account) cannot be logged in with any password — no empty-hash
// bypass.
func TestAttempt_EmptyHashRejected(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	prov := g.provider.(*fakeProvider)
	noPw := fakeUser{id: "u2", hash: ""}
	prov.byCred["sso@example.com"] = noPw
	prov.byID["u2"] = noPw
	var err error
	runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		_, err = g.Attempt(r.Context(), w, r, "sso@example.com", "")
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("empty-hash login err = %v, want ErrInvalidCredentials", err)
	}
}

// TestLogin_ClearsStaleCache proves that logging in a NEW identity within a
// request invalidates a previously cached user (no identity bleed).
func TestLogin_ClearsStaleCache(t *testing.T) {
	g, mgr, u := newGuard(t, Options{})
	prov := g.provider.(*fakeProvider)
	bob := fakeUser{id: "u9", hash: mustHash(t, "bobpw")}
	prov.byCred["bob@example.com"] = bob
	prov.byID["u9"] = bob

	runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		r2 := withCachedUser(r)
		// Log in alice, resolve and cache her.
		if _, err := g.Attempt(r2.Context(), w, r2, "alice@example.com", "s3cret"); err != nil {
			t.Fatalf("Attempt alice: %v", err)
		}
		got, err := g.User(r2.Context(), r2)
		if err != nil || got.AuthID() != u.id {
			t.Fatalf("alice resolve = (%v,%v)", got, err)
		}
		// Now log in bob on the same request: the cache must drop alice.
		if _, err := g.Attempt(r2.Context(), w, r2, "bob@example.com", "bobpw"); err != nil {
			t.Fatalf("Attempt bob: %v", err)
		}
		got2, err := g.User(r2.Context(), r2)
		if err != nil {
			t.Fatalf("bob resolve: %v", err)
		}
		if got2.AuthID() != "u9" {
			t.Fatalf("after re-login User=%q, want u9 (stale cache bled through)", got2.AuthID())
		}
	})
}

// TestLogout_FlushesEverything proves Logout clears the guard id AND flushes
// other session data, so no authenticated remnant survives.
func TestLogout_FlushesEverything(t *testing.T) {
	g, mgr, _ := newGuard(t, Options{})
	loginCookie, _ := runWithSession(t, mgr, nil, func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Attempt(r.Context(), w, r, "alice@example.com", "s3cret"); err != nil {
			t.Fatalf("Attempt: %v", err)
		}
	})
	postLogout, _ := runWithSession(t, mgr, loginCookie, func(w http.ResponseWriter, r *http.Request) {
		if err := g.Logout(r.Context(), w, r); err != nil {
			t.Fatalf("Logout: %v", err)
		}
		if g.Check(r) {
			t.Error("Check=true right after Logout")
		}
		if g.ID(r) != "" {
			t.Error("ID non-empty right after Logout")
		}
	})
	// Old cookie must no longer authenticate (regenerated + flushed).
	runWithSession(t, mgr, loginCookie, func(w http.ResponseWriter, r *http.Request) {
		if g.Check(r) {
			t.Error("old cookie still authenticated after logout")
		}
	})
	if postLogout != nil && postLogout.Value == loginCookie.Value {
		t.Error("session id not regenerated on logout")
	}
}
