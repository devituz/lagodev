// Package guard implements a cookie-backed, stateful session auth guard
// modelled on Laravel's session guard ("web" guard). It sits on top of the
// session package: where session persists arbitrary per-request data, guard
// adds the notion of an *authenticated user* — login, logout, current-user
// resolution, and route protection middleware.
//
// It complements (does not replace) the stateless JWT in package auth. Use
// the JWT guard for APIs and SPAs that carry a bearer token; use this session
// guard for server-rendered apps that ride on an HttpOnly cookie.
//
// Design rules (secure-by-default):
//
//   - The session stores only the user's opaque id, never the user record.
//     The current user is re-resolved through the UserProvider per request and
//     cached on the request context, so a single request hits the provider at
//     most once.
//   - On a successful Attempt/Login the session ID is regenerated before the
//     authenticated id is written, defeating session fixation: a pre-login ID
//     an attacker may have planted is discarded.
//   - Logout regenerates and flushes the session so no authenticated remnant
//     survives, and (via session.Destroy semantics) the stale cookie is not
//     re-issued.
//   - Password verification goes through a pluggable Hasher (default: bcrypt).
//     The interface keeps app models free to store any hash format and keeps
//     the package free of a hard bcrypt dependency at the call site.
//
// UserProvider is the single integration seam: implement it over whatever
// store backs your users (SQL, an ORM, an in-memory map) and the guard works
// unchanged.
package guard

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/devituz/lagodev/session"
	"golang.org/x/crypto/bcrypt"
)

// sessionKey is the session map key under which the authenticated user id is
// stored. It is namespaced so it cannot collide with application data.
const sessionKey = "_guard_user_id"

// Errors returned by Attempt and related flows.
var (
	// ErrUnauthenticated is returned by User when there is no authenticated
	// user on the request (no session, no stored id, or the id no longer
	// resolves to a user).
	ErrUnauthenticated = errors.New("guard: unauthenticated")
	// ErrInvalidCredentials is returned by Attempt when the identifier is
	// unknown or the password does not match. The two cases are deliberately
	// indistinguishable to callers to avoid user enumeration.
	ErrInvalidCredentials = errors.New("guard: invalid credentials")
	// ErrNoSession is returned when guard is used on a request that has no
	// session attached (the session middleware was not applied). Without a
	// session there is nowhere to record the login.
	ErrNoSession = errors.New("guard: no session on request")
)

// User is the minimal contract an application's user model must satisfy. It is
// intentionally tiny so any model fits: implement two methods and the guard
// can authenticate it.
type User interface {
	// AuthID returns the opaque, stable identifier persisted in the session.
	AuthID() string
	// AuthPasswordHash returns the stored password hash for credential checks.
	// It may be "" for users that authenticate by other means; Attempt then
	// fails with ErrInvalidCredentials.
	AuthPasswordHash() string
}

// UserProvider resolves users for the guard. Implementations back onto the
// app's user store. Both methods return ok=false (not an error) for a clean
// "no such user" miss; a non-nil error signals an infrastructure failure
// (store unreachable, query error).
type UserProvider interface {
	// FindByID resolves a user by the identifier returned from AuthID. It is
	// called once per request to rehydrate the authenticated user.
	FindByID(ctx context.Context, id string) (User, bool, error)
	// FindByCredentials resolves a user by a login identifier (email,
	// username, ...). The password is NOT checked here — the guard compares
	// the hash via its Hasher — so implementations must not leak whether the
	// password matched.
	FindByCredentials(ctx context.Context, identifier string) (User, bool, error)
}

// Hasher verifies a plaintext password against a stored hash. The default is
// BcryptHasher; supply a custom one (argon2, scrypt, a legacy format) via
// Options.Hasher.
type Hasher interface {
	// Verify reports whether plain matches the stored hash. It must run in
	// time independent of where a mismatch occurs (bcrypt and friends already
	// do this).
	Verify(hash, plain string) bool
}

// BcryptHasher is the default Hasher, backed by golang.org/x/crypto/bcrypt
// (already a project dependency, used by package auth for hashing).
type BcryptHasher struct{}

// Verify reports whether plain matches the bcrypt hash.
func (BcryptHasher) Verify(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// RememberStore is an optional hook for "remember me": long-lived tokens that
// re-authenticate a user after the session cookie expires. It is intentionally
// minimal — the guard does not wire it into the cookie lifecycle automatically;
// applications that want remember-me drive Issue/Lookup/Forget themselves and
// call Login on a successful token lookup. Leave Options.Remember nil to skip.
type RememberStore interface {
	// Issue persists a new remember token for userID and returns the opaque
	// plaintext to set in a client cookie.
	Issue(ctx context.Context, userID string) (token string, err error)
	// Lookup resolves a remember token to a user id. ok=false on miss/expiry.
	Lookup(ctx context.Context, token string) (userID string, ok bool, err error)
	// Forget invalidates a remember token (called on logout).
	Forget(ctx context.Context, token string) error
}

// Options configures a Guard. The zero value is usable once a Manager and a
// UserProvider are supplied: Hasher defaults to BcryptHasher and the redirect
// is empty (Middleware then answers 401 rather than redirecting).
type Options struct {
	// Hasher verifies passwords. Defaults to BcryptHasher when nil.
	Hasher Hasher
	// Remember is the optional remember-me backend. Nil disables the hook.
	Remember RememberStore
	// LoginPath, when non-empty, makes Middleware redirect unauthenticated
	// requests there (302) instead of returning 401. Guest does the inverse
	// with HomePath.
	LoginPath string
	// HomePath, when non-empty, makes Guest redirect already-authenticated
	// requests there instead of returning 403.
	HomePath string
}

// Guard ties a session.Manager to a UserProvider, providing login/logout and
// current-user resolution. A single Guard is shared across requests; per-request
// state (the resolved user) lives on the request context, so Guard itself holds
// no mutable request state and is safe for concurrent use.
type Guard struct {
	mgr      *session.Manager
	provider UserProvider
	hasher   Hasher
	remember RememberStore
	login    string
	home     string
}

// New returns a Guard. mgr and provider are required; opts is optional.
func New(mgr *session.Manager, provider UserProvider, opts Options) *Guard {
	h := opts.Hasher
	if h == nil {
		h = BcryptHasher{}
	}
	return &Guard{
		mgr:      mgr,
		provider: provider,
		hasher:   h,
		remember: opts.Remember,
		login:    opts.LoginPath,
		home:     opts.HomePath,
	}
}

// Remember exposes the configured RememberStore (nil if unset), so callers that
// opt into remember-me can drive Issue/Lookup/Forget without re-plumbing it.
func (g *Guard) Remember() RememberStore { return g.remember }

// Attempt verifies the credentials and, on success, logs the user in: it
// regenerates the session (fixation defense) and records the user id. It
// returns the authenticated user.
//
// Errors:
//   - ErrInvalidCredentials when the identifier is unknown OR the password is
//     wrong (the two are indistinguishable on purpose).
//   - ErrNoSession when no session is attached to the request.
//   - a wrapped provider/store error on infrastructure failure.
func (g *Guard) Attempt(ctx context.Context, w http.ResponseWriter, r *http.Request, identifier, password string) (User, error) {
	u, ok, err := g.provider.FindByCredentials(ctx, identifier)
	if err != nil {
		return nil, fmt.Errorf("guard: find by credentials: %w", err)
	}
	if !ok {
		// Unknown identifier and wrong password collapse to the same error so
		// callers cannot enumerate which accounts exist.
		return nil, ErrInvalidCredentials
	}
	if hash := u.AuthPasswordHash(); hash == "" || !g.hasher.Verify(hash, password) {
		return nil, ErrInvalidCredentials
	}
	if err := g.Login(ctx, w, r, u); err != nil {
		return nil, err
	}
	return u, nil
}

// Login marks user as authenticated on the request's session. It regenerates
// the session ID first (so any pre-login fixation token is discarded) and then
// stores the user id. The session is persisted by the session middleware on
// the response write, or by an explicit Session.Save.
func (g *Guard) Login(ctx context.Context, w http.ResponseWriter, r *http.Request, user User) error {
	s := session.FromRequest(r)
	if s == nil {
		return ErrNoSession
	}
	if err := s.Regenerate(ctx); err != nil {
		return fmt.Errorf("guard: regenerate session: %w", err)
	}
	s.Put(sessionKey, user.AuthID())
	// Drop any cached user from a prior identity on this request.
	clearCachedUser(r)
	return nil
}

// Logout clears the authenticated id and invalidates the session. It flushes
// the session contents and regenerates the ID so no authenticated remnant
// survives; the stale cookie is handled by the session layer. Remember tokens,
// if any, are the caller's responsibility (call Remember().Forget).
func (g *Guard) Logout(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	s := session.FromRequest(r)
	if s == nil {
		return ErrNoSession
	}
	s.Forget(sessionKey)
	s.Flush()
	if err := s.Regenerate(ctx); err != nil {
		return fmt.Errorf("guard: regenerate session: %w", err)
	}
	clearCachedUser(r)
	return nil
}

// ID returns the authenticated user's id from the session, or "" when no user
// is logged in. It does not touch the provider.
func (g *Guard) ID(r *http.Request) string {
	s := session.FromRequest(r)
	if s == nil {
		return ""
	}
	return s.GetString(sessionKey)
}

// Check reports whether a user id is present in the session. It is the cheap
// "is anyone logged in" test and, like ID, never calls the provider.
func (g *Guard) Check(r *http.Request) bool {
	return g.ID(r) != ""
}

// User resolves and returns the authenticated user, hitting the provider at
// most once per request (the result is cached on the request context by
// WithCachedUser, installed by Middleware). It returns ErrUnauthenticated when
// no id is stored or the stored id no longer resolves to a user (e.g. the
// account was deleted).
func (g *Guard) User(ctx context.Context, r *http.Request) (User, error) {
	if u, ok := cachedUser(r); ok {
		if u == nil {
			return nil, ErrUnauthenticated
		}
		return u, nil
	}
	id := g.ID(r)
	if id == "" {
		return nil, ErrUnauthenticated
	}
	u, ok, err := g.provider.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("guard: find by id: %w", err)
	}
	if !ok {
		// Stored id points at a vanished user: treat as unauthenticated and
		// cache the negative so we don't re-query within the request.
		setCachedUser(r, nil)
		return nil, ErrUnauthenticated
	}
	setCachedUser(r, u)
	return u, nil
}

// Middleware returns a net/http middleware that requires an authenticated
// user. Unauthenticated requests get a 302 to LoginPath when configured, else
// a 401. It also installs the per-request user cache, so downstream handlers
// calling User do not re-query the provider.
//
// It assumes the session middleware ran earlier in the chain (so a session is
// attached). Order: session.Middleware -> guard.Middleware -> handler.
func (g *Guard) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = withCachedUser(r)
			if _, err := g.User(r.Context(), r); err != nil {
				if g.login != "" {
					http.Redirect(w, r, g.login, http.StatusFound)
					return
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Guest returns the inverse middleware: it admits only unauthenticated
// requests (login/register pages). Authenticated requests get a 302 to
// HomePath when configured, else a 403.
func (g *Guard) Guest() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = withCachedUser(r)
			if g.Check(r) {
				if g.home != "" {
					http.Redirect(w, r, g.home, http.StatusFound)
					return
				}
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---- per-request user cache ------------------------------------------------

// cacheKey is the context key under which the resolved user is memoised.
type cacheKey struct{}

// userCache is a tiny mutable box so the cache can be populated after the
// request context is built (context values are immutable, the box is not).
type userCache struct {
	loaded bool
	user   User // nil + loaded==true means "resolved to no user"
}

// withCachedUser attaches an empty cache box to the request if absent and
// returns the (possibly new) request. Idempotent: nested middleware reuse the
// same box.
func withCachedUser(r *http.Request) *http.Request {
	if r.Context().Value(cacheKey{}) != nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), cacheKey{}, &userCache{}))
}

func cachedUser(r *http.Request) (User, bool) {
	box, ok := r.Context().Value(cacheKey{}).(*userCache)
	if !ok || !box.loaded {
		return nil, false
	}
	return box.user, true
}

func setCachedUser(r *http.Request, u User) {
	if box, ok := r.Context().Value(cacheKey{}).(*userCache); ok {
		box.user = u
		box.loaded = true
	}
}

// clearCachedUser invalidates the cache after a login/logout changes identity
// within the same request.
func clearCachedUser(r *http.Request) {
	if box, ok := r.Context().Value(cacheKey{}).(*userCache); ok {
		box.user = nil
		box.loaded = false
	}
}
