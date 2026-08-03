# Authentication

lagodev ships two complementary authentication models and the
credential-lifecycle plumbing around them:

- **Stateless JWT** (`auth`) — bearer tokens for APIs and SPAs.
- **Stateful session guard** (`auth/guard` + `session`) — HttpOnly-cookie
  login for server-rendered apps, Laravel "web" guard style.
- **Personal access tokens** (`auth/token`) — Sanctum-style opaque,
  scoped API keys.
- **Social login** (`auth/oauth`) — OAuth2 / OIDC with mandatory PKCE.
- **Account flows** (`auth/account`) — password reset, email verification,
  signed URLs, and a login throttler.

Every package is framework-agnostic: it takes `context.Context`, depends on
nothing in `web`, and exposes a small `Store`/`Provider` seam so you can swap
the in-memory default for SQL or Redis without touching call sites. Secrets are
never stored in plaintext (bcrypt for passwords, SHA-256 for tokens), and the
secure choices are the defaults.

## Quick start

A JWT-protected API needs nothing more than a `Manager`:

```go
import "github.com/devituz/lagodev/auth"

mgr, err := auth.New(auth.Config{
    Secret:     os.Getenv("APP_KEY"),     // >= 32 bytes
    Issuer:     "myapp",
    AccessTTL:  15 * time.Minute,
    RefreshTTL: 30 * 24 * time.Hour,
})
if err != nil { log.Fatal(err) }

// On login: hash check, then issue a token pair.
if !mgr.VerifyPassword(user.PasswordHash, form.Password) {
    return c.Unauthorized()
}
access, exp, _ := mgr.IssueAccess(user.ID, user.Role)
refresh, _, _  := mgr.IssueRefresh(user.ID, user.Role)
```

On every protected request, parse the bearer token:

```go
claims, err := mgr.ParseAccess(bearer)
switch {
case errors.Is(err, auth.ErrExpiredToken):
    return c.Status(401)            // client should refresh
case errors.Is(err, auth.ErrInvalidToken):
    return c.Status(401)
}
userID, role := claims.UserID, claims.Role
```

`New` rejects a secret shorter than `auth.MinSecretLen` (32 bytes) unless
`Config.AllowWeakSecret` is set — keep that flag for tests only.

### The `auth.Manager` API

```go
func New(cfg Config) (*Manager, error)

func (m *Manager) Issue(userID uint64, role, tokenType string, ttl time.Duration) (string, time.Time, error)
func (m *Manager) IssueAccess(userID uint64, role string)  (string, time.Time, error)
func (m *Manager) IssueRefresh(userID uint64, role string) (string, time.Time, error)

func (m *Manager) Parse(token string)                       (*Claims, error)
func (m *Manager) ParseAccess(token string)                 (*Claims, error)
func (m *Manager) ParseRefresh(token string)                (*Claims, error)
func (m *Manager) ParseTyped(token, expectedType string)    (*Claims, error)

func (m *Manager) HashPassword(password string)  (string, error)
func (m *Manager) VerifyPassword(hash, password string) bool

func (m *Manager) AccessTTL()  time.Duration
func (m *Manager) RefreshTTL() time.Duration
```

`Claims` embeds `jwt.RegisteredClaims`, so `iss`/`sub`/`exp`/`iat`/`nbf`/`jti`
are populated and verified automatically; the lagodev-specific fields are:

```go
type Claims struct {
    UserID uint64 `json:"uid"`
    Role   string `json:"role,omitempty"`
    Type   string `json:"typ,omitempty"`  // "access" / "refresh" / custom
    jwt.RegisteredClaims
}
```

`ParseAccess`/`ParseRefresh` are sugar over `ParseTyped(token, TokenAccess)` /
`ParseTyped(token, TokenRefresh)` — they reject a refresh token presented where
an access token is expected (and vice versa). Define your own type string
(e.g. `"api"`) and call `Issue` / `ParseTyped` directly when you need a third
class.

## Guards & sessions

For server-rendered apps you want a cookie, not a bearer token. The `session`
package persists per-request data under an opaque ID carried in an
**HttpOnly + Secure** cookie; `auth/guard` layers the notion of an
*authenticated user* on top.

### Sessions

```go
import "github.com/devituz/lagodev/session"

store := session.NewMemoryStore(2 * time.Hour)
defer store.Close()

mgr := session.NewManager(store, session.Options{
    CookieName: "myapp_session",
    TTL:        2 * time.Hour,
    SameSite:   http.SameSiteLaxMode,
    // Insecure: true,   // ONLY for local HTTP dev
})

// Wrap your handlers; the middleware loads/saves the session per request.
handler := mgr.Middleware()(mux)
```

Inside a handler, pull the session off the request:

```go
sess := session.FromRequest(r)        // nil if middleware not applied
sess.Put("locale", "uz")
loc := sess.GetString("locale")
v, ok := sess.Get("cart")
sess.Forget("flash")
_ = sess.Save(ctx, w)                 // middleware also saves on the way out
```

`Session` also exposes `All()`, `Flush()`, `ID()`, `IsNew()`,
`Regenerate(ctx)` (rotate the ID, defeating fixation) and
`Destroy(ctx, w)` (drop the record and clear the cookie). It is a per-request
handle and is **not** safe for concurrent use across goroutines.

`Options` defaults are production-correct without setup: cookie
`lagodev_session`, TTL 2h, `Secure=true`, `SameSite=Lax`. Note the inverted
`Insecure` flag — you opt *out* of Secure, never into it.

### The session guard

`Guard` ties a `*session.Manager` to a `UserProvider`. Implement two tiny
interfaces and the guard handles login, logout, and current-user resolution.

```go
import "github.com/devituz/lagodev/auth/guard"

// 1. Your user model satisfies guard.User.
type User struct{ ID, Hash string }
func (u User) AuthID() string           { return u.ID }
func (u User) AuthPasswordHash() string { return u.Hash }

// 2. A provider resolves users from your store.
type provider struct{ db *database.Connection }

func (p provider) FindByID(ctx context.Context, id string) (guard.User, bool, error) {
    u, err := orm.Query[User](p.db).Where("id", "=", id).First(ctx)
    if errors.Is(err, orm.ErrNotFound) { return nil, false, nil }
    return u, err == nil, err
}
func (p provider) FindByCredentials(ctx context.Context, login string) (guard.User, bool, error) {
    u, err := orm.Query[User](p.db).Where("email", "=", login).First(ctx)
    if errors.Is(err, orm.ErrNotFound) { return nil, false, nil }
    return u, err == nil, err
}

g := guard.New(mgr, provider{db}, guard.Options{
    LoginPath: "/login",   // Middleware redirects here instead of 401
})
```

Login, inspect, and log out:

```go
// POST /login — verifies the password via the guard's Hasher (bcrypt default).
user, err := g.Attempt(ctx, w, r, form.Email, form.Password)
if errors.Is(err, guard.ErrInvalidCredentials) {
    // unknown user AND wrong password are indistinguishable (no enumeration)
}

g.Check(r)              // bool: is the request authenticated?
g.ID(r)                 // the stored opaque user id ("" if none)
u, err := g.User(ctx, r)  // re-resolves via the provider, cached per request

_ = g.Logout(ctx, w, r) // flushes the session, drops the cookie
```

Two middlewares enforce access:

```go
mux.Handle("/dashboard", g.Middleware()(dashboard))  // 401/redirect if guest
mux.Handle("/login",     g.Guest()(loginPage))       // 403/redirect if logged in
```

Security properties are built in: `Attempt`/`Login` **regenerate the session ID
before** writing the authenticated id (session-fixation defence), the session
stores only the opaque id (the user is re-resolved per request), and password
verification runs through a pluggable `Hasher`:

```go
type Hasher interface { Verify(hash, plain string) bool }
```

The default is `guard.BcryptHasher{}`. Supply your own (argon2, scrypt, or a
legacy format you're migrating off) via `Options.Hasher` — it must compare in
constant time.

> **JWT vs. session.** Use the JWT `Manager` for stateless APIs that carry a
> bearer token. Use the session `Guard` for browser apps that ride on a cookie.
> They are independent; nothing forces you to pick only one.

## Personal access tokens

`auth/token` implements Sanctum-style API keys: opaque, scoped, hashed at rest.
The plaintext is shown to the client exactly once at creation; the server stores
only `hex(SHA-256(secret))`. The wire format is `"<id>|<secret>"`, so lookup is
O(1) by id and the secret is hash-compared in constant time — no row scans.

```go
import "github.com/devituz/lagodev/auth/token"

issuer := token.NewIssuer(token.NewMemoryStore())

// Issue a token for user 7, scoped to two abilities, never-expiring (ttl 0).
rec, plain, err := issuer.Issue(ctx, 7, "CI deploy key",
    []string{"posts:read", "posts:write"}, 0)
// → hand `plain` (e.g. "abc123|xYz...") to the client ONCE; store nothing else.

// On an incoming request: resolve and authorize.
pat, err := issuer.Find(ctx, bearer)
switch {
case errors.Is(err, token.ErrNotFound), errors.Is(err, token.ErrMalformed):
    return c.Status(401)
case errors.Is(err, token.ErrExpired), errors.Is(err, token.ErrRevoked):
    return c.Status(401)
}
if pat.Cant("posts:write") {
    return c.Status(403)
}
```

`Find` also stamps `LastUsedAt`. Management and revocation:

```go
list, _ := issuer.List(ctx, 7)          // all tokens for a user (incl. revoked)
_ = issuer.Revoke(ctx, pat.ID)          // by id
_ = issuer.RevokePlain(ctx, bearer)     // by the presented plaintext
```

Abilities are arbitrary scope strings; the wildcard `token.AbilityAll` (`"*"`)
makes `Can(x)` true for any `x`. The `PersonalAccessToken` record exposes
`Can`, `Cant`, `Expired`, and `Revoked` helpers.

Swap `MemoryStore` for a DB-backed `Store` in production — the interface is four
methods (`Save`, `Get`, `Delete`, `ListByUser`).

## OAuth / social login

`auth/oauth` runs the authorization-code flow with **mandatory PKCE (S256)**
using only the standard library. Prebuilt constructors fill the well-known
endpoints for Google and GitHub; `Generic` accepts any OIDC/OAuth2 endpoint set.

```go
import "github.com/devituz/lagodev/auth/oauth"

p := oauth.Google(oauth.Config{
    ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
    ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
    RedirectURL:  "https://app.test/auth/google/callback",
    // Scopes optional — Google defaults to "openid email profile".
})
```

### Step 1 — redirect to consent

Mint a PKCE verifier and a CSRF state, stash both in the session, then redirect:

```go
func redirect(w http.ResponseWriter, r *http.Request) {
    verifier, _ := oauth.NewVerifier()
    state, _    := oauth.NewState()

    sess := session.FromRequest(r)
    sess.Put("oauth_verifier", verifier)
    sess.Put("oauth_state", state)
    _ = sess.Save(r.Context(), w)

    url := p.AuthCodeURL(state, oauth.Challenge(verifier))
    http.Redirect(w, r, url, http.StatusFound)
}
```

Only the SHA-256 challenge travels in the redirect; an intercepted
authorization code is useless without the verifier.

### Step 2 — handle the callback

Verify state, then exchange the code (presenting the verifier) and fetch the
identity:

```go
func callback(w http.ResponseWriter, r *http.Request) {
    sess := session.FromRequest(r)
    if r.URL.Query().Get("state") != sess.GetString("oauth_state") {
        http.Error(w, "bad state", http.StatusBadRequest)
        return
    }
    verifier := sess.GetString("oauth_verifier")

    tok, err := p.Exchange(r.Context(), r.URL.Query().Get("code"), verifier)
    if err != nil { /* oauth.ErrTokenResponse, ... */ }

    user, err := p.User(r.Context(), tok)   // normalised identity
    // user.ID, user.Name, user.Email, user.Avatar, user.Raw (full payload)
    _ = upsertAndLogin(user)
}
```

`Token` carries `AccessToken`, `TokenType`, `RefreshToken`, `IDToken` (raw OIDC
JWT when present — this package does **not** verify it), and `Expiry`. `User`
is the normalised shape; `Raw` preserves the provider's full payload for fields
outside the common set.

### Stateless state (no session storage)

If you cannot persist per-request state, use an HMAC-signed, self-expiring state
value instead of `NewState`:

```go
signer := oauth.NewStateSigner([]byte(os.Getenv("APP_KEY")))

state, _ := signer.SignedState(5 * time.Minute)   // step 1
// ...
err := signer.VerifyState(r.URL.Query().Get("state"))  // step 2, constant-time
```

### Other providers

```go
p := oauth.GitHub(oauth.Config{ClientID: id, ClientSecret: sec, RedirectURL: cb})

// Any OIDC/OAuth2 provider; nil mapper falls back to standard OIDC claims.
p := oauth.Generic(cfg, authURL, tokenURL, userInfoURL, func(raw []byte) (oauth.User, error) {
    var v struct{ Sub, Name, Mail string }
    _ = json.Unmarshal(raw, &v)
    return oauth.User{ID: v.Sub, Name: v.Name, Email: v.Mail}, nil
})

// Inject an *http.Client (timeouts, test transports); nil → http.DefaultClient.
p = p.WithHTTPClient(&http.Client{Timeout: 10 * time.Second})
```

## Account flows

`auth/account` covers the credential lifecycle around login: password-reset and
email-verification tokens, signed URLs, and a login throttler.

### Single-use, expiring tokens

Reset and verification tokens are high-entropy random values; the store keeps
only a SHA-256 hash, and `Consume` succeeds at most once (defeating replay).
`Purpose` namespaces the tokens so a reset token can never be replayed as a
verification token.

```go
import "github.com/devituz/lagodev/auth/account"

tokens := account.NewTokens(account.NewMemoryTokenStore(), time.Hour)

// "Forgot password" → mail this plaintext in a reset link.
plain, _ := tokens.Issue(ctx, account.PurposeReset, "user-42")

// On the reset form submit:
subject, err := tokens.Consume(ctx, account.PurposeReset, plain)
switch {
case errors.Is(err, account.ErrTokenNotFound): // wrong/used token
case errors.Is(err, account.ErrTokenExpired):  // past TTL
default:
    resetPasswordFor(subject)   // "user-42"
}
```

Use `Verify` (read-only, does not burn the token) when you want to validate
before showing the form, and `Consume` on submit. `account.PurposeVerify` is the
email-verification namespace; the pattern is identical.

### Signed URLs

`Signer` produces tamper-proof, self-expiring links with an HMAC over the path,
a canonical sorted encoding of the query, and the expiry — so reordering or
adding parameters invalidates the signature.

```go
signer := account.NewSigner([]byte(os.Getenv("APP_KEY")))

link, _ := signer.Sign("https://app.test/invite?team=acme", time.Hour)
// → original URL plus signature & expiry query params.

err := signer.Verify(link)   // constant-time
// account.ErrInvalidSignature | account.ErrSignatureExpired | nil
```

### Login throttling

`Throttle` is a fixed-window failure counter keyed by an arbitrary string. After
`maxAttempts` failures within the window the key is locked; a successful login
must `Clear` it.

```go
th := account.NewThrottle(5, time.Minute)   // 5 tries / minute
key := "login:" + email

if err := th.Check(key); errors.Is(err, account.ErrThrottled) {
    return c.Status(429)
}
if !passwordOK {
    _ = th.Hit(key)                          // record a failure (also returns ErrThrottled when it trips)
    return c.Unauthorized()
}
th.Clear(key)                                // success: reset the counter
```

`Attempts(key)` returns the current count if you want to surface "N tries left".

## Production notes & security gotchas

- **Secret length.** `auth.New` requires a >= 32-byte `Secret`; reuse the same
  `APP_KEY` bytes for `oauth.NewStateSigner` and `account.NewSigner`. Never set
  `AllowWeakSecret` outside tests.
- **Swap the in-memory stores.** `session.MemoryStore`, `token.MemoryStore`, and
  `account.MemoryTokenStore` are single-replica and lose state on restart.
  Behind a load balancer or for durability, implement the `Store` /
  `TokenStore` interface over SQL or Redis — call sites don't change.
- **Cookies are Secure by default.** `session.Options.Insecure=true` disables
  the `Secure` flag; flip it only on local HTTP. Keep `SameSite=Lax` (or
  `Strict`) and never widen it without reason.
- **Session fixation is handled** by the guard (ID regenerated on login). If you
  elevate privileges mid-session by other means, call `sess.Regenerate(ctx)`
  yourself.
- **No user enumeration.** `guard.ErrInvalidCredentials` is returned for both an
  unknown identifier and a wrong password — keep them indistinguishable in your
  HTTP responses too, and run the throttler on the identifier.
- **Tokens at rest are hashes.** A leak exposes no usable credential (bcrypt
  for passwords, SHA-256 for PATs and reset/verify tokens). Show a PAT or reset
  token plaintext exactly once.
- **OAuth: verify the `IDToken` yourself.** This package mints/checks PKCE and
  (optionally) signs state, but does **not** validate the OIDC `id_token`
  signature — verify it against the provider's JWKS before trusting its claims.
- **Throttler is in-memory and per-process.** For multi-replica deployments,
  back the rate limit with a shared store (Redis `INCR`/`EXPIRE`).
