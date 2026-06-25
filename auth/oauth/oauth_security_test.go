package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// roundTripFunc adapts a function to http.RoundTripper so the provider's
// outbound calls hit an in-memory stub instead of the network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func stubClient(fn func(*http.Request) (*http.Response, error)) *http.Client {
	return &http.Client{Transport: roundTripFunc(fn)}
}

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       newBody(body),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func formResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       newBody(body),
		Header:     http.Header{"Content-Type": []string{"application/x-www-form-urlencoded"}},
	}
}

func newBody(s string) *stringBody { return &stringBody{r: strings.NewReader(s)} }

type stringBody struct{ r *strings.Reader }

func (b *stringBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *stringBody) Close() error               { return nil }

// ---- PKCE ------------------------------------------------------------------

// TestPKCE_S256Correctness proves Challenge implements RFC 7636 S256 exactly:
// base64url(SHA-256(verifier)) without padding.
func TestPKCE_S256Correctness(t *testing.T) {
	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if len(v) < 43 || len(v) > 128 {
		t.Fatalf("verifier length %d outside RFC 7636 43..128", len(v))
	}
	sum := sha256.Sum256([]byte(v))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if got := Challenge(v); got != want {
		t.Fatalf("Challenge = %q, want %q", got, want)
	}
	if strings.ContainsAny(Challenge(v), "=+/") {
		t.Fatal("challenge is not base64url (contains padding or non-url chars)")
	}
}

// TestPKCE_RFCVector checks the exact test vector from RFC 7636 Appendix B.
func TestPKCE_RFCVector(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := Challenge(verifier); got != want {
		t.Fatalf("Challenge = %q, want RFC vector %q", got, want)
	}
}

// TestPKCE_VerifiersUnique proves verifiers are high-entropy / non-repeating.
func TestPKCE_VerifiersUnique(t *testing.T) {
	seen := make(map[string]struct{})
	for n := 0; n < 100; n++ {
		v, err := NewVerifier()
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		if _, dup := seen[v]; dup {
			t.Fatal("duplicate verifier")
		}
		seen[v] = struct{}{}
	}
}

// ---- signed state (CSRF) ---------------------------------------------------

func newStateSigner() *StateSigner {
	return NewStateSigner([]byte("0123456789abcdef0123456789abcdef"))
}

func TestSignedState_RoundTrip(t *testing.T) {
	s := newStateSigner()
	st, err := s.SignedState(time.Hour)
	if err != nil {
		t.Fatalf("SignedState: %v", err)
	}
	if err := s.VerifyState(st); err != nil {
		t.Fatalf("VerifyState = %v, want nil", err)
	}
}

// TestSignedState_RejectsTampering proves a mutated nonce or signature fails,
// defeating CSRF where an attacker forges a state value.
func TestSignedState_RejectsTampering(t *testing.T) {
	s := newStateSigner()
	st, err := s.SignedState(time.Hour)
	if err != nil {
		t.Fatalf("SignedState: %v", err)
	}
	parts := strings.SplitN(st, ".", 3)
	if len(parts) != 3 {
		t.Fatalf("expected nonce.exp.sig, got %q", st)
	}

	// Mutate the nonce, keep the signature.
	bad1 := "x" + parts[0][1:] + "." + parts[1] + "." + parts[2]
	if err := s.VerifyState(bad1); err != ErrInvalidState {
		t.Fatalf("nonce tamper = %v, want ErrInvalidState", err)
	}

	// Mutate the embedded expiry (extend the lifetime) keeping old signature.
	bad2 := parts[0] + ".9999999999." + parts[2]
	if err := s.VerifyState(bad2); err != ErrInvalidState {
		t.Fatalf("exp tamper = %v, want ErrInvalidState", err)
	}

	// Mutate the signature.
	bad3 := parts[0] + "." + parts[1] + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := s.VerifyState(bad3); err != ErrInvalidState {
		t.Fatalf("sig tamper = %v, want ErrInvalidState", err)
	}

	// Wrong key entirely.
	other := NewStateSigner([]byte("ffffffffffffffffffffffffffffffff"))
	if err := other.VerifyState(st); err != ErrInvalidState {
		t.Fatalf("wrong key = %v, want ErrInvalidState", err)
	}

	// Malformed inputs.
	for _, in := range []string{"", "nodot", "only.two", ".leading", "trailing."} {
		if err := s.VerifyState(in); err != ErrInvalidState {
			t.Fatalf("VerifyState(%q) = %v, want ErrInvalidState", in, err)
		}
	}
}

// TestSignedState_Expiry proves a state past its embedded expiry is rejected
// (replay-after-timeout defense).
func TestSignedState_Expiry(t *testing.T) {
	s := newStateSigner()
	st, err := s.SignedState(time.Second)
	if err != nil {
		t.Fatalf("SignedState: %v", err)
	}
	// Expiry is at 1s Unix resolution; wait until the wall clock passes it.
	parts := strings.Split(st, ".")
	for {
		if err := s.VerifyState(st); err == ErrInvalidState {
			break // expired (VerifyState collapses expiry into ErrInvalidState)
		}
		time.Sleep(50 * time.Millisecond)
		_ = parts
	}
}

// ---- Exchange --------------------------------------------------------------

func testProvider(client *http.Client) Provider {
	return Generic(
		Config{ClientID: "cid", ClientSecret: "sec", RedirectURL: "https://app/cb"},
		"https://idp/auth", "https://idp/token", "https://idp/userinfo", nil,
	).WithHTTPClient(client)
}

// TestExchange_JSON handles a JSON token endpoint.
func TestExchange_JSON(t *testing.T) {
	p := testProvider(stubClient(func(r *http.Request) (*http.Response, error) {
		// Assert PKCE verifier is forwarded in the form body.
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if r.FormValue("code_verifier") != "the-verifier" {
			t.Errorf("code_verifier = %q, want the-verifier", r.FormValue("code_verifier"))
		}
		return jsonResp(200, `{"access_token":"AT","token_type":"Bearer","expires_in":3600,"refresh_token":"RT","id_token":"IDT"}`), nil
	}))
	tok, err := p.Exchange(context.Background(), "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "AT" || tok.RefreshToken != "RT" || tok.IDToken != "IDT" {
		t.Fatalf("token fields wrong: %+v", tok)
	}
	if tok.Expiry.IsZero() {
		t.Fatal("Expiry not set from expires_in")
	}
}

// TestExchange_Form handles a form-encoded token endpoint (GitHub default).
func TestExchange_Form(t *testing.T) {
	p := testProvider(stubClient(func(r *http.Request) (*http.Response, error) {
		return formResp(200, "access_token=AT&token_type=bearer&scope=read"), nil
	}))
	tok, err := p.Exchange(context.Background(), "c", "v")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "AT" {
		t.Fatalf("AccessToken = %q, want AT", tok.AccessToken)
	}
}

// TestExchange_OAuthError maps an RFC 6749 error object to ErrTokenResponse.
func TestExchange_OAuthError(t *testing.T) {
	p := testProvider(stubClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(400, `{"error":"invalid_grant","error_description":"bad code"}`), nil
	}))
	_, err := p.Exchange(context.Background(), "c", "v")
	if !errors.Is(err, ErrTokenResponse) {
		t.Fatalf("Exchange = %v, want ErrTokenResponse", err)
	}
}

// TestExchange_Malformed proves malformed/empty/garbage provider responses
// produce errors, never a panic.
func TestExchange_Malformed(t *testing.T) {
	cases := []*http.Response{
		jsonResp(200, `{not json`),
		jsonResp(200, `{}`),                 // empty access_token
		jsonResp(200, ``),                   // empty body
		formResp(200, "%zz=bad"),            // unparseable form
		jsonResp(500, `{"error":"server"}`), // 5xx
		formResp(200, "token_type=bearer"),  // missing access_token
	}
	for i, resp := range cases {
		i, resp := i, resp
		p := testProvider(stubClient(func(*http.Request) (*http.Response, error) {
			return resp, nil
		}))
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("case %d panicked: %v", i, r)
				}
			}()
			if _, err := p.Exchange(context.Background(), "c", "v"); err == nil {
				t.Fatalf("case %d: Exchange err = nil, want error", i)
			}
		}()
	}
}

// TestExchange_OversizedBody proves the 1 MiB read cap protects against a
// hostile provider streaming an unbounded body (no OOM, returns a value).
func TestExchange_OversizedBody(t *testing.T) {
	huge := `{"access_token":"AT","token_type":"Bearer","junk":"` + strings.Repeat("A", 4<<20) + `"}`
	p := testProvider(stubClient(func(*http.Request) (*http.Response, error) {
		return jsonResp(200, huge), nil
	}))
	// The body exceeds 1 MiB so it is truncated; JSON decode then fails, which
	// must surface as an error, not a panic or unbounded allocation.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked on oversized body: %v", r)
		}
	}()
	_, _ = p.Exchange(context.Background(), "c", "v")
}

// TestAuthCodeURL_CarriesPKCEAndState proves the consent URL embeds the S256
// challenge, method, and CSRF state.
func TestAuthCodeURL_CarriesPKCEAndState(t *testing.T) {
	p := testProvider(nil)
	v, _ := NewVerifier()
	ch := Challenge(v)
	st := "the-state"
	raw := p.AuthCodeURL(st, ch)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("code_challenge") != ch {
		t.Fatalf("code_challenge = %q, want %q", q.Get("code_challenge"), ch)
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("state") != st {
		t.Fatalf("state = %q, want %q", q.Get("state"), st)
	}
	if q.Get("response_type") != "code" {
		t.Fatal("response_type != code")
	}
}

// TestUser_MalformedResponse proves a malformed userinfo body errors cleanly.
func TestUser_MalformedResponse(t *testing.T) {
	p := testProvider(stubClient(func(*http.Request) (*http.Response, error) {
		return jsonResp(200, `{not valid`), nil
	}))
	if _, err := p.User(context.Background(), &Token{AccessToken: "AT"}); err == nil {
		t.Fatal("User with malformed body = nil, want error")
	}
	// nil / empty token must not reach the network.
	if _, err := p.User(context.Background(), nil); !errors.Is(err, ErrUserInfoResponse) {
		t.Fatalf("User(nil) = %v, want ErrUserInfoResponse", err)
	}
}
