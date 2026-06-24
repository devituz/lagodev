package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	m, err := New(Config{
		Secret:     "test-secret-do-not-use-in-prod-32bytes!!",
		Issuer:     "test",
		AccessTTL:  time.Minute,
		RefreshTTL: time.Hour,
		BcryptCost: 4, // Lowest cost so tests run fast.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func TestIssueAndParseAccess(t *testing.T) {
	m := newManager(t)
	token, exp, err := m.IssueAccess(42, "admin")
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	if exp.Before(time.Now()) {
		t.Fatalf("expiry already past: %v", exp)
	}

	claims, err := m.Parse(token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.UserID != 42 {
		t.Fatalf("UserID = %d, want 42", claims.UserID)
	}
	if claims.Role != "admin" {
		t.Fatalf("Role = %q, want admin", claims.Role)
	}
	if claims.Type != TokenAccess {
		t.Fatalf("Type = %q, want access", claims.Type)
	}
}

// TestTokensAreUnique locks issue #4: two tokens issued in immediate
// succession for the same (user, role, type) must differ — a random jti
// guarantees a distinct token (and distinct hash) every time.
func TestTokensAreUnique(t *testing.T) {
	m := newManager(t)

	t1, _, err := m.IssueRefresh(7, "admin")
	if err != nil {
		t.Fatalf("IssueRefresh: %v", err)
	}
	t2, _, err := m.IssueRefresh(7, "admin")
	if err != nil {
		t.Fatalf("IssueRefresh: %v", err)
	}
	if t1 == t2 {
		t.Fatalf("two refresh tokens for same (user, role) are identical — jti missing")
	}

	c1, err := m.Parse(t1)
	if err != nil {
		t.Fatalf("Parse t1: %v", err)
	}
	c2, err := m.Parse(t2)
	if err != nil {
		t.Fatalf("Parse t2: %v", err)
	}
	if c1.ID == "" || c2.ID == "" {
		t.Fatalf("jti (claims.ID) is empty: %q / %q", c1.ID, c2.ID)
	}
	if c1.ID == c2.ID {
		t.Fatalf("jti collision: %q", c1.ID)
	}
}

func TestParseRejectsBadSignature(t *testing.T) {
	m1 := newManager(t)
	m2, _ := New(Config{Secret: "another-secret-totally-different-key!!", AccessTTL: time.Minute})

	token, _, _ := m1.IssueAccess(1, "user")
	if _, err := m2.Parse(token); err != ErrInvalidToken {
		t.Fatalf("Parse with wrong secret = %v, want ErrInvalidToken", err)
	}
}

func TestParseRejectsExpired(t *testing.T) {
	m := newManager(t)
	// Issue() lets us pass a negative TTL directly so we don't need to sleep.
	// Expire well beyond the default leeway (30s) so the leeway window does
	// not keep the token valid.
	token, _, err := m.Issue(1, "user", TokenAccess, -2*DefaultLeeway)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := m.Parse(token); err != ErrExpiredToken {
		t.Fatalf("Parse expired = %v, want ErrExpiredToken", err)
	}
}

func TestPasswordHashing(t *testing.T) {
	m := newManager(t)
	hash, err := m.HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !m.VerifyPassword(hash, "hunter2") {
		t.Fatalf("VerifyPassword: correct password rejected")
	}
	if m.VerifyPassword(hash, "wrong") {
		t.Fatalf("VerifyPassword: wrong password accepted")
	}
}

func TestNewRequiresSecret(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatalf("New with empty secret should fail")
	}
}

// TestNewRejectsWeakSecret locks the secret-length check: a secret shorter
// than MinSecretLen is refused unless AllowWeakSecret is set.
func TestNewRejectsWeakSecret(t *testing.T) {
	if _, err := New(Config{Secret: "short"}); err == nil {
		t.Fatalf("New with <%d byte secret should fail", MinSecretLen)
	}
	if _, err := New(Config{Secret: "short", AllowWeakSecret: true}); err != nil {
		t.Fatalf("AllowWeakSecret should relax the length check: %v", err)
	}
}

// TestParseTyped_RejectsWrongType locks issue #4: a refresh token must not
// pass ParseAccess (and vice versa).
func TestParseTyped_RejectsWrongType(t *testing.T) {
	m := newManager(t)

	refresh, _, err := m.IssueRefresh(1, "user")
	if err != nil {
		t.Fatalf("IssueRefresh: %v", err)
	}
	if _, err := m.ParseAccess(refresh); err != ErrInvalidToken {
		t.Fatalf("ParseAccess(refresh) = %v, want ErrInvalidToken", err)
	}
	if _, err := m.ParseRefresh(refresh); err != nil {
		t.Fatalf("ParseRefresh(refresh) = %v, want nil", err)
	}

	access, _, err := m.IssueAccess(1, "user")
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	if _, err := m.ParseRefresh(access); err != ErrInvalidToken {
		t.Fatalf("ParseRefresh(access) = %v, want ErrInvalidToken", err)
	}
	if _, err := m.ParseAccess(access); err != nil {
		t.Fatalf("ParseAccess(access) = %v, want nil", err)
	}
}

// TestParse_VerifiesIssuer locks issue #5: a token signed by a manager with
// a different issuer (but the same secret) must be rejected.
func TestParse_VerifiesIssuer(t *testing.T) {
	const secret = "test-secret-do-not-use-in-prod-32bytes!!"
	other, err := New(Config{Secret: secret, Issuer: "other-issuer", AccessTTL: time.Minute})
	if err != nil {
		t.Fatalf("New other: %v", err)
	}
	token, _, err := other.IssueAccess(1, "user")
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	// newManager uses Issuer "test", same secret — issuer mismatch must fail.
	m := newManager(t)
	if _, err := m.Parse(token); err != ErrInvalidToken {
		t.Fatalf("Parse cross-issuer = %v, want ErrInvalidToken", err)
	}
}

// TestParse_VerifiesAudience locks the audience validator.
func TestParse_VerifiesAudience(t *testing.T) {
	const secret = "test-secret-do-not-use-in-prod-32bytes!!"
	issuer, err := New(Config{Secret: secret, Audience: "aud-a", AccessTTL: time.Minute})
	if err != nil {
		t.Fatalf("New issuer: %v", err)
	}
	token, _, err := issuer.IssueAccess(1, "user")
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	// Same secret, expecting a different audience — must be rejected.
	verifier, err := New(Config{Secret: secret, Audience: "aud-b", AccessTTL: time.Minute})
	if err != nil {
		t.Fatalf("New verifier: %v", err)
	}
	if _, err := verifier.Parse(token); err != ErrInvalidToken {
		t.Fatalf("Parse cross-audience = %v, want ErrInvalidToken", err)
	}
	// Matching audience parses fine.
	if _, err := issuer.Parse(token); err != nil {
		t.Fatalf("Parse matching audience = %v, want nil", err)
	}
}

// TestParse_LeewayAllowsClockSkew locks issue #6: a token whose nbf is a few
// seconds in the future still parses within the configured leeway.
func TestParse_LeewayAllowsClockSkew(t *testing.T) {
	m := newManager(t) // default leeway 30s

	// Build a token with nbf 5s in the future (within leeway).
	now := time.Now()
	claims := Claims{
		UserID: 1,
		Type:   TokenAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "test",
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(5 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte("test-secret-do-not-use-in-prod-32bytes!!"))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := m.Parse(signed); err != nil {
		t.Fatalf("Parse within leeway = %v, want nil", err)
	}
}
