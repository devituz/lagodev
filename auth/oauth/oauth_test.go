package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAuthCodeURL(t *testing.T) {
	p := Google(Config{
		ClientID:    "cid",
		RedirectURL: "https://app.example/callback",
	})
	verifier, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	challenge := Challenge(verifier)
	state := "state-123"

	raw := p.AuthCodeURL(state, challenge, url.Values{"access_type": {"offline"}})
	if !strings.HasPrefix(raw, p.AuthURL+"?") {
		t.Fatalf("URL not prefixed with auth endpoint: %s", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()

	want := map[string]string{
		"response_type":         "code",
		"client_id":             "cid",
		"redirect_uri":          "https://app.example/callback",
		"scope":                 "openid email profile",
		"state":                 state,
		"code_challenge":        challenge,
		"code_challenge_method": "S256",
		"access_type":           "offline",
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("param %q = %q, want %q", k, got, v)
		}
	}
}

func TestAuthCodeURLExistingQuery(t *testing.T) {
	p := Generic(Config{ClientID: "cid"}, "https://idp.example/auth?tenant=acme", "https://idp.example/token", "https://idp.example/userinfo", nil)
	raw := p.AuthCodeURL("st", "ch")
	u, _ := url.Parse(raw)
	if u.Query().Get("tenant") != "acme" {
		t.Fatalf("existing query param lost: %s", raw)
	}
	if u.Query().Get("state") != "st" {
		t.Fatalf("state not appended: %s", raw)
	}
}

func TestAuthCodeURLNoPKCENoSecretLeak(t *testing.T) {
	// Without a challenge the PKCE params must be absent; the client secret
	// must never appear in the consent URL.
	p := Google(Config{ClientID: "cid", ClientSecret: "super-secret", RedirectURL: "https://app/cb"})
	raw := p.AuthCodeURL("st", "")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Has("code_challenge") || q.Has("code_challenge_method") {
		t.Fatalf("PKCE params present without a challenge: %s", raw)
	}
	if strings.Contains(raw, "super-secret") {
		t.Fatalf("client secret leaked into auth URL: %s", raw)
	}
	if q.Get("client_secret") != "" {
		t.Fatalf("client_secret must not be a consent-URL param")
	}
}

func TestAuthCodeURLExtraOverride(t *testing.T) {
	// extra is documented to add or override; confirm both directions so the
	// override semantics stay deliberate (a caller can replace state/prompt).
	p := Generic(Config{ClientID: "cid"}, "https://idp/auth", "https://idp/token", "https://idp/userinfo", nil)
	raw := p.AuthCodeURL("orig", "ch", url.Values{
		"state":  {"replaced"},
		"prompt": {"consent"},
	})
	q, _ := url.Parse(raw)
	if got := q.Query().Get("state"); got != "replaced" {
		t.Fatalf("extra did not override state: %q", got)
	}
	if got := q.Query().Get("prompt"); got != "consent" {
		t.Fatalf("extra did not add prompt: %q", got)
	}
	if got := q.Query().Get("code_challenge"); got != "ch" {
		t.Fatalf("challenge dropped: %q", got)
	}
}

func TestPKCERoundTrip(t *testing.T) {
	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if len(v) < 43 || len(v) > 128 {
		t.Fatalf("verifier length %d out of RFC range", len(v))
	}
	sum := sha256.Sum256([]byte(v))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if got := Challenge(v); got != want {
		t.Fatalf("Challenge = %q, want %q", got, want)
	}
	if strings.ContainsAny(Challenge(v), "=+/") {
		t.Fatalf("challenge not base64url-without-padding")
	}
}

func TestExchangeJSON(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-1","token_type":"Bearer","refresh_token":"rt-1","id_token":"idt","expires_in":3600}`))
	}))
	defer srv.Close()

	p := Generic(Config{ClientID: "cid", ClientSecret: "sec", RedirectURL: "https://app/cb"}, "", srv.URL, "", nil).WithHTTPClient(srv.Client())

	tok, err := p.Exchange(context.Background(), "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "at-1" || tok.TokenType != "Bearer" || tok.RefreshToken != "rt-1" || tok.IDToken != "idt" {
		t.Fatalf("token fields wrong: %+v", tok)
	}
	if !tok.Valid() {
		t.Fatalf("token should be valid")
	}
	if d := time.Until(tok.Expiry); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("expiry off: %v", d)
	}
	for k, want := range map[string]string{
		"grant_type":    "authorization_code",
		"code":          "the-code",
		"code_verifier": "the-verifier",
		"client_id":     "cid",
		"client_secret": "sec",
		"redirect_uri":  "https://app/cb",
	} {
		if got := gotForm.Get(k); got != want {
			t.Errorf("form %q = %q, want %q", k, got, want)
		}
	}
}

func TestExchangeFormEncoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		_, _ = w.Write([]byte("access_token=gh-at&token_type=bearer&scope=read:user"))
	}))
	defer srv.Close()

	p := GitHub(Config{ClientID: "cid"}).WithHTTPClient(srv.Client())
	p.TokenURL = srv.URL

	tok, err := p.Exchange(context.Background(), "code", "")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "gh-at" || tok.TokenType != "bearer" {
		t.Fatalf("form token wrong: %+v", tok)
	}
	if !tok.Expiry.IsZero() {
		t.Fatalf("expiry should be zero when expires_in absent")
	}
}

func TestExchangeErrorObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"bad code"}`))
	}))
	defer srv.Close()

	p := Generic(Config{ClientID: "cid"}, "", srv.URL, "", nil).WithHTTPClient(srv.Client())
	_, err := p.Exchange(context.Background(), "code", "v")
	if !errors.Is(err, ErrTokenResponse) {
		t.Fatalf("want ErrTokenResponse, got %v", err)
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error should carry provider message: %v", err)
	}
}

func TestExchangeFormEncodedError(t *testing.T) {
	// GitHub returns OAuth errors as form-encoded bodies, often with a 200
	// status. The error must still surface as ErrTokenResponse.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		_, _ = w.Write([]byte("error=bad_verification_code&error_description=The+code+is+incorrect"))
	}))
	defer srv.Close()

	p := GitHub(Config{ClientID: "cid"}).WithHTTPClient(srv.Client())
	p.TokenURL = srv.URL

	_, err := p.Exchange(context.Background(), "code", "")
	if !errors.Is(err, ErrTokenResponse) {
		t.Fatalf("want ErrTokenResponse, got %v", err)
	}
	if !strings.Contains(err.Error(), "bad_verification_code") {
		t.Fatalf("error should carry provider message: %v", err)
	}
}

func TestExchangeGarbageBody(t *testing.T) {
	// A non-JSON, non-form body with a success status must not silently yield
	// an empty token; it falls through to the empty-access_token guard.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not a token at all"))
	}))
	defer srv.Close()

	p := Generic(Config{ClientID: "cid"}, "", srv.URL, "", nil).WithHTTPClient(srv.Client())
	if _, err := p.Exchange(context.Background(), "code", "v"); !errors.Is(err, ErrTokenResponse) {
		t.Fatalf("want ErrTokenResponse, got %v", err)
	}
}

func TestExchangeEmptyAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token_type":"Bearer"}`))
	}))
	defer srv.Close()

	p := Generic(Config{ClientID: "cid"}, "", srv.URL, "", nil).WithHTTPClient(srv.Client())
	if _, err := p.Exchange(context.Background(), "code", "v"); !errors.Is(err, ErrTokenResponse) {
		t.Fatalf("want ErrTokenResponse, got %v", err)
	}
}

func TestUserGoogle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer at-1" {
			t.Errorf("auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"123","name":"Ada","email":"ada@example.com","picture":"https://pic/a.png","locale":"en"}`))
	}))
	defer srv.Close()

	p := Google(Config{ClientID: "cid"}).WithHTTPClient(srv.Client())
	p.UserInfoURL = srv.URL

	u, err := p.User(context.Background(), &Token{AccessToken: "at-1"})
	if err != nil {
		t.Fatalf("User: %v", err)
	}
	if u.ID != "123" || u.Name != "Ada" || u.Email != "ada@example.com" || u.Avatar != "https://pic/a.png" {
		t.Fatalf("google user wrong: %+v", u)
	}
	if u.Raw["locale"] != "en" {
		t.Fatalf("raw not preserved: %+v", u.Raw)
	}
}

func TestUserGitHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"login":"octocat","name":"","email":"oct@example.com","avatar_url":"https://gh/a.png"}`))
	}))
	defer srv.Close()

	p := GitHub(Config{ClientID: "cid"}).WithHTTPClient(srv.Client())
	p.UserInfoURL = srv.URL

	u, err := p.User(context.Background(), &Token{AccessToken: "at-1"})
	if err != nil {
		t.Fatalf("User: %v", err)
	}
	if u.ID != "42" {
		t.Fatalf("id = %q, want 42", u.ID)
	}
	if u.Name != "octocat" { // falls back to login when name empty
		t.Fatalf("name = %q, want octocat", u.Name)
	}
	if u.Avatar != "https://gh/a.png" {
		t.Fatalf("avatar wrong: %q", u.Avatar)
	}
}

func TestUserGenericOIDC(t *testing.T) {
	// Generic with a nil mapper must use the default OIDC mapper, including the
	// preferred_username fallback when name is absent.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer at-1" {
			t.Errorf("auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"u-9","preferred_username":"neo","email":"neo@matrix","picture":"https://p/n.png","dept":"ops"}`))
	}))
	defer srv.Close()

	p := Generic(Config{}, "", "", srv.URL, nil).WithHTTPClient(srv.Client())
	u, err := p.User(context.Background(), &Token{AccessToken: "at-1"})
	if err != nil {
		t.Fatalf("User: %v", err)
	}
	if u.ID != "u-9" || u.Name != "neo" || u.Email != "neo@matrix" || u.Avatar != "https://p/n.png" {
		t.Fatalf("oidc user wrong: %+v", u)
	}
	if u.Raw["dept"] != "ops" {
		t.Fatalf("raw not preserved: %+v", u.Raw)
	}
}

func TestUserMapError(t *testing.T) {
	// A 2xx body that is not valid JSON must propagate the mapper failure
	// rather than returning a half-populated User.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<<not json>>"))
	}))
	defer srv.Close()

	p := Google(Config{}).WithHTTPClient(srv.Client())
	p.UserInfoURL = srv.URL
	if _, err := p.User(context.Background(), &Token{AccessToken: "x"}); err == nil {
		t.Fatalf("want mapper error on invalid JSON body")
	}
}

func TestUserError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("bad token"))
	}))
	defer srv.Close()

	p := Google(Config{}).WithHTTPClient(srv.Client())
	p.UserInfoURL = srv.URL
	if _, err := p.User(context.Background(), &Token{AccessToken: "x"}); !errors.Is(err, ErrUserInfoResponse) {
		t.Fatalf("want ErrUserInfoResponse, got %v", err)
	}

	if _, err := p.User(context.Background(), &Token{}); !errors.Is(err, ErrUserInfoResponse) {
		t.Fatalf("missing token: want ErrUserInfoResponse, got %v", err)
	}
}

func TestNewStateUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		s, err := NewState()
		if err != nil {
			t.Fatalf("NewState: %v", err)
		}
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate state: %s", s)
		}
		seen[s] = struct{}{}
	}
}

func TestSignedStateVerify(t *testing.T) {
	s := NewStateSigner([]byte("0123456789abcdef0123456789abcdef"))
	state, err := s.SignedState(time.Minute)
	if err != nil {
		t.Fatalf("SignedState: %v", err)
	}
	if err := s.VerifyState(state); err != nil {
		t.Fatalf("VerifyState valid: %v", err)
	}

	// Tampered payload.
	if err := s.VerifyState("nonce.0." + strings.SplitN(state, ".", 3)[2]); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("tamper: want ErrInvalidState, got %v", err)
	}

	// Wrong key.
	other := NewStateSigner([]byte("different-key-different-key-1234"))
	if err := other.VerifyState(state); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("wrong key: want ErrInvalidState, got %v", err)
	}

	// Malformed.
	for _, bad := range []string{"", "noseparator", "a.b", ".", "a."} {
		if err := s.VerifyState(bad); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("malformed %q: want ErrInvalidState, got %v", bad, err)
		}
	}
}

func TestVerifyStateWrongLengthSig(t *testing.T) {
	// A correctly structured value whose signature has a different length must
	// still be rejected (ConstantTimeCompare returns 0 on length mismatch).
	s := NewStateSigner([]byte("0123456789abcdef0123456789abcdef"))
	state, err := s.SignedState(time.Minute)
	if err != nil {
		t.Fatalf("SignedState: %v", err)
	}
	payload := state[:strings.LastIndexByte(state, '.')]
	if err := s.VerifyState(payload + ".short"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("short sig: want ErrInvalidState, got %v", err)
	}
	if err := s.VerifyState(payload + "." + strings.Repeat("A", 200)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("long sig: want ErrInvalidState, got %v", err)
	}
}

func TestVerifyStateTamperedExpiry(t *testing.T) {
	// Flipping the expiry while keeping the structure intact must fail the
	// signature check (the expiry is inside the signed payload).
	s := NewStateSigner([]byte("0123456789abcdef0123456789abcdef"))
	state, err := s.SignedState(time.Minute)
	if err != nil {
		t.Fatalf("SignedState: %v", err)
	}
	parts := strings.SplitN(state, ".", 3) // nonce, expiry, sig
	forged := parts[0] + ".9999999999." + parts[2]
	if err := s.VerifyState(forged); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("tampered expiry: want ErrInvalidState, got %v", err)
	}
}

func TestSignedStateExpiry(t *testing.T) {
	s := NewStateSigner([]byte("k"))
	expired, err := s.SignedState(-time.Second)
	if err != nil {
		t.Fatalf("SignedState: %v", err)
	}
	// ttl<=0 means non-expiring (expiry=0), should verify.
	if err := s.VerifyState(expired); err != nil {
		t.Fatalf("non-expiring should verify: %v", err)
	}

	// Build a state whose expiry is already in the past by re-signing manually
	// is internal; instead verify a 1ns ttl elapses.
	short, _ := s.SignedState(time.Nanosecond)
	time.Sleep(2 * time.Second) // cross the unix-second boundary deterministically
	if err := s.VerifyState(short); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expired state: want ErrInvalidState, got %v", err)
	}
}
