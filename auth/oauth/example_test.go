package oauth_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/devituz/lagodev/auth/oauth"
)

// ExampleProvider runs the full authorization-code + PKCE flow against a fake
// OAuth2 provider served by httptest: it shows the consent URL carries the PKCE
// S256 challenge, then exchanges a code for a token and maps the userinfo
// response into a normalised User. Only stable fields are printed (never the
// random verifier/state).
func ExampleProvider() {
	// Fake provider: a token endpoint and an OIDC userinfo endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"at-123","token_type":"Bearer","expires_in":3600}`)
		case "/userinfo":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"sub":"909","name":"Ada Lovelace","email":"ada@example.com"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := oauth.Generic(
		oauth.Config{ClientID: "demo-client", RedirectURL: "https://app.test/callback"},
		srv.URL+"/auth", srv.URL+"/token", srv.URL+"/userinfo", nil,
	).WithHTTPClient(srv.Client())

	// Build the consent URL with a fixed verifier so the output is
	// deterministic (a real app uses oauth.NewVerifier()).
	verifier := "fixed-test-verifier-value-for-example"
	challenge := oauth.Challenge(verifier)
	authURL := p.AuthCodeURL("state-xyz", challenge)
	fmt.Println("url has S256:", strings.Contains(authURL, "code_challenge_method=S256"))
	fmt.Println("url has challenge:", strings.Contains(authURL, "code_challenge="+challenge))

	// Callback returns a code; exchange it (presenting the verifier).
	tok, err := p.Exchange(context.Background(), "auth-code-abc", verifier)
	if err != nil {
		panic(err)
	}
	fmt.Println("token type:", tok.TokenType)
	fmt.Println("token valid:", tok.Valid())

	// Map the provider identity into the common User shape.
	user, err := p.User(context.Background(), tok)
	if err != nil {
		panic(err)
	}
	fmt.Println("user id:", user.ID)
	fmt.Println("user email:", user.Email)

	// Output:
	// url has S256: true
	// url has challenge: true
	// token type: Bearer
	// token valid: true
	// user id: 909
	// user email: ada@example.com
}
