// Package oauth implements the OAuth2 / social-login flows that back a
// Laravel-Socialite-style "log in with Google/GitHub" experience, using only
// the standard library.
//
// Design rules (secure-by-default):
//
//   - The authorization-code flow always carries PKCE (RFC 7636, S256). The
//     verifier is a high-entropy random value; only its SHA-256 challenge
//     travels in the redirect, so an intercepted authorization code is useless
//     without the verifier.
//   - A CSRF state parameter binds the redirect to the originating session.
//     NewState mints an opaque random value; SignedState mints a stateless,
//     HMAC-signed value (with an embedded expiry) that the callback can verify
//     without server-side storage, in constant time.
//   - Token exchange and userinfo calls go through an injected *http.Client so
//     transports can be stubbed in tests; nil falls back to http.DefaultClient.
//   - Every outbound request honours the caller's context.Context.
//
// A Provider holds the endpoint URLs plus a mapper that normalises the
// provider's userinfo payload into a common User. Prebuilt constructors fill
// the well-known endpoints for Google and GitHub; Generic accepts any
// OIDC/OAuth2 endpoint set.
package oauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Errors returned by the flow.
var (
	// ErrTokenResponse is returned when the token endpoint replies with a
	// non-2xx status or an OAuth2 error object.
	ErrTokenResponse = errors.New("oauth: token endpoint error")
	// ErrUserInfoResponse is returned when the userinfo endpoint replies with
	// a non-2xx status.
	ErrUserInfoResponse = errors.New("oauth: userinfo endpoint error")
	// ErrInvalidState is returned when a signed state fails verification
	// (bad signature, malformed, or expired).
	ErrInvalidState = errors.New("oauth: invalid state")
)

// Config carries the per-application credentials and the redirect URL. It is
// embedded into a Provider by the prebuilt constructors.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// Scopes are space-joined into the "scope" parameter. The constructors
	// supply provider defaults when this is empty.
	Scopes []string
}

// UserMapper normalises a provider's raw userinfo JSON into a User. The bytes
// are the decoded HTTP body; the mapper decides which fields populate the
// common shape and stashes the lot in User.Raw.
type UserMapper func(raw []byte) (User, error)

// User is the normalised identity returned by User. Raw preserves the
// provider's full payload for fields outside the common shape.
type User struct {
	ID     string
	Name   string
	Email  string
	Avatar string
	Raw    map[string]any
}

// Token is the result of a successful code exchange. IDToken holds the raw
// OIDC id_token JWT when the provider returns one (empty otherwise); this
// package does not verify it.
type Token struct {
	AccessToken  string
	TokenType    string
	RefreshToken string
	IDToken      string
	Expiry       time.Time // zero when the provider omits expires_in
}

// Valid reports whether the token has an access token that has not expired.
// A zero Expiry is treated as non-expiring.
func (t *Token) Valid() bool {
	if t.AccessToken == "" {
		return false
	}
	return t.Expiry.IsZero() || time.Now().Before(t.Expiry)
}

// Provider is a configured OAuth2 endpoint set plus the userinfo mapper. Build
// one with Google, GitHub, or Generic; the zero value is not usable.
type Provider struct {
	Config
	// AuthURL is the authorization (consent) endpoint.
	AuthURL string
	// TokenURL is the token-exchange endpoint.
	TokenURL string
	// UserInfoURL is the endpoint that returns the authenticated identity.
	UserInfoURL string
	// mapUser normalises the userinfo response.
	mapUser UserMapper
	// client performs outbound HTTP; nil means http.DefaultClient.
	client *http.Client
}

// WithHTTPClient returns a shallow copy of p that uses c for outbound
// requests. Pass a stubbed client in tests. A nil c restores the default.
func (p Provider) WithHTTPClient(c *http.Client) Provider {
	p.client = c
	return p
}

// httpClient resolves the effective HTTP client.
func (p *Provider) httpClient() *http.Client {
	if p.client != nil {
		return p.client
	}
	return http.DefaultClient
}

// AuthCodeURL builds the consent URL the user is redirected to. state is the
// CSRF token (from NewState or SignedState); challenge is the PKCE S256
// challenge for the verifier kept by the caller. extra adds or overrides query
// parameters (e.g. "access_type=offline", "prompt=consent").
func (p *Provider) AuthCodeURL(state, challenge string, extra ...url.Values) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", p.RedirectURL)
	if len(p.Scopes) > 0 {
		q.Set("scope", strings.Join(p.Scopes, " "))
	}
	q.Set("state", state)
	if challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	for _, ev := range extra {
		for k, vs := range ev {
			q[k] = vs
		}
	}
	sep := "?"
	if strings.Contains(p.AuthURL, "?") {
		sep = "&"
	}
	return p.AuthURL + sep + q.Encode()
}

// tokenResponse is the wire shape of a token-endpoint reply, covering both the
// success and RFC 6749 §5.2 error forms.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	RefreshToken     string `json:"refresh_token"`
	IDToken          string `json:"id_token"`
	ExpiresIn        int64  `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Exchange swaps an authorization code for a Token, presenting the PKCE
// verifier the challenge was derived from. The request is form-encoded per
// RFC 6749 with the client secret in the body; providers that require basic
// auth still accept this form.
//
// Errors: ErrTokenResponse wraps any non-2xx status or OAuth2 error object.
func (p *Provider) Exchange(ctx context.Context, code, verifier string) (*Token, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", p.RedirectURL)
	form.Set("client_id", p.ClientID)
	if p.ClientSecret != "" {
		form.Set("client_secret", p.ClientSecret)
	}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("oauth: read token response: %w", err)
	}

	// Some providers (notably GitHub's default) reply with form-encoded
	// bodies; detect JSON and fall back to form parsing otherwise.
	var tr tokenResponse
	if looksJSON(body) {
		if err := json.Unmarshal(body, &tr); err != nil {
			return nil, fmt.Errorf("oauth: decode token response: %w", err)
		}
	} else {
		vals, perr := url.ParseQuery(string(body))
		if perr != nil {
			return nil, fmt.Errorf("oauth: decode token response: %w", perr)
		}
		tr.AccessToken = vals.Get("access_token")
		tr.TokenType = vals.Get("token_type")
		tr.RefreshToken = vals.Get("refresh_token")
		tr.IDToken = vals.Get("id_token")
		tr.Error = vals.Get("error")
		tr.ErrorDescription = vals.Get("error_description")
		if ei := vals.Get("expires_in"); ei != "" {
			tr.ExpiresIn, _ = strconv.ParseInt(ei, 10, 64)
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status %d: %s", ErrTokenResponse, resp.StatusCode, tokenErrText(&tr, body))
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("%w: %s: %s", ErrTokenResponse, tr.Error, tr.ErrorDescription)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("%w: empty access_token", ErrTokenResponse)
	}

	tok := &Token{
		AccessToken:  tr.AccessToken,
		TokenType:    tr.TokenType,
		RefreshToken: tr.RefreshToken,
		IDToken:      tr.IDToken,
	}
	if tr.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC()
	}
	return tok, nil
}

// User fetches the authenticated identity from the userinfo endpoint using the
// access token as a bearer credential, then maps it through the provider's
// UserMapper.
//
// Errors: ErrUserInfoResponse wraps any non-2xx status.
func (p *Provider) User(ctx context.Context, tok *Token) (User, error) {
	if tok == nil || tok.AccessToken == "" {
		return User{}, fmt.Errorf("%w: missing access token", ErrUserInfoResponse)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.UserInfoURL, nil)
	if err != nil {
		return User{}, fmt.Errorf("oauth: build userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return User{}, fmt.Errorf("oauth: userinfo request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return User{}, fmt.Errorf("oauth: read userinfo response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return User{}, fmt.Errorf("%w: status %d: %s", ErrUserInfoResponse, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	u, err := p.mapUser(body)
	if err != nil {
		return User{}, fmt.Errorf("oauth: map userinfo: %w", err)
	}
	return u, nil
}

// ---- PKCE ------------------------------------------------------------------

// verifierBytes is the entropy of a PKCE verifier (256-bit). Base64url-encoded
// this yields 43 characters, within RFC 7636's 43..128 range.
const verifierBytes = 32

// NewVerifier returns a fresh PKCE code verifier: a high-entropy,
// base64url-without-padding string. Keep it in the session and pass it to
// Exchange after the callback.
func NewVerifier() (string, error) {
	return randString(verifierBytes)
}

// Challenge derives the S256 code challenge for a verifier:
// base64url(SHA-256(verifier)), without padding.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ---- CSRF state ------------------------------------------------------------

// NewState returns an opaque, single-use CSRF state value. Store it in the
// session and compare it against the callback's state parameter.
func NewState() (string, error) {
	return randString(16)
}

// StateSigner mints and verifies stateless, HMAC-signed state values. Use it
// when you do not want to persist per-request state server-side: the signed
// value carries its own expiry and is rejected on tamper or timeout.
type StateSigner struct {
	key []byte
}

// NewStateSigner returns a StateSigner keyed by key (reuse APP_KEY bytes; any
// length, 32 bytes recommended).
func NewStateSigner(key []byte) *StateSigner {
	cp := append([]byte(nil), key...)
	return &StateSigner{key: cp}
}

// SignedState returns a stateless state value of the form
// "<nonce>.<expiry>.<signature>", where signature is base64url HMAC-SHA256 over
// "<nonce>.<expiry>". ttl<=0 produces a non-expiring value (expiry=0).
func (s *StateSigner) SignedState(ttl time.Duration) (string, error) {
	nonce, err := randString(12)
	if err != nil {
		return "", err
	}
	var expUnix int64
	if ttl > 0 {
		expUnix = time.Now().Add(ttl).UTC().Unix()
	}
	payload := nonce + "." + strconv.FormatInt(expUnix, 10)
	sig := base64.RawURLEncoding.EncodeToString(hmacSHA256(s.key, []byte(payload)))
	return payload + "." + sig, nil
}

// VerifyState checks a value produced by SignedState. It returns ErrInvalidState
// on a bad signature, malformed input, or a passed expiry. The signature is
// compared in constant time.
func (s *StateSigner) VerifyState(state string) error {
	i := strings.LastIndexByte(state, '.')
	if i <= 0 || i == len(state)-1 {
		return ErrInvalidState
	}
	payload, gotSig := state[:i], state[i+1:]
	wantSig := base64.RawURLEncoding.EncodeToString(hmacSHA256(s.key, []byte(payload)))
	if subtle.ConstantTimeCompare([]byte(gotSig), []byte(wantSig)) != 1 {
		return ErrInvalidState
	}
	j := strings.LastIndexByte(payload, '.')
	if j <= 0 || j == len(payload)-1 {
		return ErrInvalidState
	}
	expUnix, err := strconv.ParseInt(payload[j+1:], 10, 64)
	if err != nil {
		return ErrInvalidState
	}
	if expUnix != 0 && time.Now().UTC().Unix() > expUnix {
		return ErrInvalidState
	}
	return nil
}

// ---- Prebuilt providers ----------------------------------------------------

// Google returns a Provider for Google's OIDC endpoints. When cfg.Scopes is
// empty it defaults to "openid email profile". The userinfo response is the
// OIDC standard-claims shape.
func Google(cfg Config) Provider {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "email", "profile"}
	}
	return Provider{
		Config:      cfg,
		AuthURL:     "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:    "https://oauth2.googleapis.com/token",
		UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo",
		mapUser:     mapGoogleUser,
	}
}

// GitHub returns a Provider for GitHub's OAuth2 endpoints. When cfg.Scopes is
// empty it defaults to "read:user user:email". The token endpoint is asked for
// JSON via the Accept header set in Exchange.
func GitHub(cfg Config) Provider {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"read:user", "user:email"}
	}
	return Provider{
		Config:      cfg,
		AuthURL:     "https://github.com/login/oauth/authorize",
		TokenURL:    "https://github.com/login/oauth/access_token",
		UserInfoURL: "https://api.github.com/user",
		mapUser:     mapGitHubUser,
	}
}

// Generic returns a Provider for an arbitrary OIDC/OAuth2 endpoint set. auth,
// token, and userinfo are the three endpoint URLs; mapper normalises the
// userinfo payload. A nil mapper falls back to a best-effort OIDC mapper that
// reads the standard claims (sub, name, email, picture).
func Generic(cfg Config, auth, token, userInfo string, mapper UserMapper) Provider {
	if mapper == nil {
		mapper = mapOIDCUser
	}
	return Provider{
		Config:      cfg,
		AuthURL:     auth,
		TokenURL:    token,
		UserInfoURL: userInfo,
		mapUser:     mapper,
	}
}

// mapGoogleUser maps the OIDC userinfo standard claims Google returns.
func mapGoogleUser(raw []byte) (User, error) {
	var m struct {
		Sub     string `json:"sub"`
		Name    string `json:"name"`
		Email   string `json:"email"`
		Picture string `json:"picture"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return User{}, err
	}
	return User{
		ID:     m.Sub,
		Name:   m.Name,
		Email:  m.Email,
		Avatar: m.Picture,
		Raw:    rawMap(raw),
	}, nil
}

// mapGitHubUser maps the GitHub /user payload. GitHub numbers the id, so it is
// stringified for the common shape.
func mapGitHubUser(raw []byte) (User, error) {
	var m struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return User{}, err
	}
	name := m.Name
	if name == "" {
		name = m.Login
	}
	return User{
		ID:     strconv.FormatInt(m.ID, 10),
		Name:   name,
		Email:  m.Email,
		Avatar: m.AvatarURL,
		Raw:    rawMap(raw),
	}, nil
}

// mapOIDCUser is the default mapper for Generic: OIDC standard claims.
func mapOIDCUser(raw []byte) (User, error) {
	var m struct {
		Sub               string `json:"sub"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
		Picture           string `json:"picture"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return User{}, err
	}
	name := m.Name
	if name == "" {
		name = m.PreferredUsername
	}
	return User{
		ID:     m.Sub,
		Name:   name,
		Email:  m.Email,
		Avatar: m.Picture,
		Raw:    rawMap(raw),
	}, nil
}

// ---- helpers ---------------------------------------------------------------

// rawMap decodes raw JSON into a generic map, returning nil on failure so a
// mapper never fails solely because the Raw stash could not be built.
func rawMap(raw []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// looksJSON reports whether body's first non-space byte starts a JSON object
// or array, used to pick the token-response decoder.
func looksJSON(body []byte) bool {
	for _, b := range body {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}

// tokenErrText picks the most informative message from a failed token reply.
func tokenErrText(tr *tokenResponse, body []byte) string {
	if tr.Error != "" {
		if tr.ErrorDescription != "" {
			return tr.Error + ": " + tr.ErrorDescription
		}
		return tr.Error
	}
	return strings.TrimSpace(string(body))
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// randString returns n random bytes base64url-encoded without padding.
func randString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oauth: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
