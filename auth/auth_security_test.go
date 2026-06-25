package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// secretManager builds a Manager with a fixed strong secret, issuer, and
// audience so the attacker tests exercise issuer/audience enforcement too.
func secretManager(t *testing.T) *Manager {
	t.Helper()
	m, err := New(Config{
		Secret:     "0123456789abcdef0123456789abcdef", // exactly 32 bytes
		Issuer:     "lagodev",
		Audience:   "api.lagodev",
		AccessTTL:  time.Minute,
		RefreshTTL: time.Hour,
		BcryptCost: 4,
		Leeway:     time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

// craft signs a Claims set with an arbitrary method/key, bypassing Manager so
// we can forge attacker-controlled tokens.
func craft(t *testing.T, method jwt.SigningMethod, key any, claims Claims) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, claims)
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("craft sign: %v", err)
	}
	return s
}

func baseClaims(m *Manager) Claims {
	now := time.Now()
	return Claims{
		UserID: 1,
		Role:   "admin",
		Type:   TokenAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.cfg.Issuer,
			Audience:  jwt.ClaimStrings{m.cfg.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		},
	}
}

// TestParse_RejectsAlgNone forges an unsigned (alg=none) token and asserts the
// keyfunc refuses it. This is the classic JWT downgrade attack.
func TestParse_RejectsAlgNone(t *testing.T) {
	m := secretManager(t)
	// jwt.UnsafeAllowNoneSignatureType is the sentinel key for alg=none.
	tok := craft(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, baseClaims(m))
	if _, err := m.Parse(tok); err == nil {
		t.Fatal("alg=none token was accepted")
	}
}

// TestParse_RejectsAlgConfusion signs a token with RS256 using an RSA private
// key whose PUBLIC key equals our HMAC secret bytes is not directly possible,
// so instead we assert that any RSA-signed token is rejected: the keyfunc only
// trusts HMAC, defeating the RS256->HS256 confusion where the attacker signs
// with the public key as an HMAC secret.
func TestParse_RejectsAlgConfusion(t *testing.T) {
	m := secretManager(t)
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	tok := craft(t, jwt.SigningMethodRS256, priv, baseClaims(m))
	if _, err := m.Parse(tok); err == nil {
		t.Fatal("RS256 token was accepted (alg confusion)")
	}
}

// TestParse_RejectsHMACSecretAsKey simulates the confusion variant where an
// attacker who learns the (public) issuer material signs an HS256 token with a
// DIFFERENT secret. A wrong HMAC secret must fail signature verification.
func TestParse_RejectsTamperedSignature(t *testing.T) {
	m := secretManager(t)
	good, _, err := m.IssueAccess(1, "admin")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	parts := strings.Split(good, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}
	// Flip a bit in the MIDDLE of the decoded signature. Flipping the last
	// base64url char can only toggle padding bits that decode to the same
	// 32 bytes (a 43-char HS256 sig's final char carries 2 meaningful bits),
	// which would not change the verified signature.
	sigRaw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	sigRaw[len(sigRaw)/2] ^= 0x01
	tampered := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(sigRaw)
	if _, err := m.Parse(tampered); err == nil {
		t.Fatal("tampered signature accepted")
	}
}

// TestParse_RejectsTamperedPayload re-encodes the claims with an escalated
// role but keeps the original signature; verification must fail.
func TestParse_RejectsTamperedPayload(t *testing.T) {
	m := secretManager(t)
	good, _, err := m.IssueAccess(1, "user")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	parts := strings.Split(good, ".")
	var payload map[string]any
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	payload["role"] = "admin"
	payload["uid"] = 999
	nb, _ := json.Marshal(payload)
	parts[1] = base64.RawURLEncoding.EncodeToString(nb)
	forged := strings.Join(parts, ".")
	if _, err := m.Parse(forged); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

// TestParse_RejectsExpired forges a token whose exp is well past the leeway.
func TestParse_RejectsExpired(t *testing.T) {
	m := secretManager(t)
	c := baseClaims(m)
	c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	c.IssuedAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
	c.NotBefore = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
	tok := craft(t, jwt.SigningMethodHS256, []byte(m.cfg.Secret), c)
	_, err := m.Parse(tok)
	if err != ErrExpiredToken {
		t.Fatalf("Parse expired = %v, want ErrExpiredToken", err)
	}
}

// TestParse_RejectsNotYetValid forges a token with nbf in the future.
func TestParse_RejectsNotYetValid(t *testing.T) {
	m := secretManager(t)
	c := baseClaims(m)
	c.NotBefore = jwt.NewNumericDate(time.Now().Add(time.Hour))
	tok := craft(t, jwt.SigningMethodHS256, []byte(m.cfg.Secret), c)
	if _, err := m.Parse(tok); err != ErrInvalidToken {
		t.Fatalf("Parse nbf-future = %v, want ErrInvalidToken", err)
	}
}

// TestParse_RejectsWrongIssuer forges a properly signed token with a foreign
// issuer.
func TestParse_RejectsWrongIssuer(t *testing.T) {
	m := secretManager(t)
	c := baseClaims(m)
	c.Issuer = "evil"
	tok := craft(t, jwt.SigningMethodHS256, []byte(m.cfg.Secret), c)
	if _, err := m.Parse(tok); err != ErrInvalidToken {
		t.Fatalf("Parse wrong issuer = %v, want ErrInvalidToken", err)
	}
}

// TestParse_RejectsWrongAudience forges a token aimed at another audience.
func TestParse_RejectsWrongAudience(t *testing.T) {
	m := secretManager(t)
	c := baseClaims(m)
	c.Audience = jwt.ClaimStrings{"other.api"}
	tok := craft(t, jwt.SigningMethodHS256, []byte(m.cfg.Secret), c)
	if _, err := m.Parse(tok); err != ErrInvalidToken {
		t.Fatalf("Parse wrong audience = %v, want ErrInvalidToken", err)
	}
}

// TestParse_RejectsCrossSecret signs with a different but equally-strong
// secret; signature verification must fail.
func TestParse_RejectsCrossSecret(t *testing.T) {
	m := secretManager(t)
	other := []byte("ffffffffffffffffffffffffffffffff")
	tok := craft(t, jwt.SigningMethodHS256, other, baseClaims(m))
	if _, err := m.Parse(tok); err != ErrInvalidToken {
		t.Fatalf("Parse cross-secret = %v, want ErrInvalidToken", err)
	}
}

// TestParse_RejectsGarbageAndOversized feeds malformed and oversized inputs;
// Parse must error, never panic.
func TestParse_RejectsGarbageAndOversized(t *testing.T) {
	m := secretManager(t)
	cases := []string{
		"",
		"not-a-jwt",
		"a.b",
		"a.b.c.d",
		strings.Repeat("A", 1<<20), // 1 MiB of junk
		"eyJ." + strings.Repeat("A", 1<<20) + ".sig", // oversized payload
		strings.Repeat("a.", 100000) + "z",           // many segments
	}
	for _, in := range cases {
		if _, err := m.Parse(in); err == nil {
			t.Fatalf("oversized/garbage input accepted: %.20q", in)
		}
	}
}

// TestParseTyped_RejectsTypeConfusion proves an access token cannot stand in
// for a refresh token (and vice versa), defeating token-type replay.
func TestParseTyped_RejectsTypeConfusion(t *testing.T) {
	m := secretManager(t)
	access, _, err := m.IssueAccess(1, "user")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := m.ParseRefresh(access); err != ErrInvalidToken {
		t.Fatalf("access replayed as refresh = %v, want ErrInvalidToken", err)
	}
	refresh, _, err := m.IssueRefresh(1, "user")
	if err != nil {
		t.Fatalf("issue refresh: %v", err)
	}
	if _, err := m.ParseAccess(refresh); err != ErrInvalidToken {
		t.Fatalf("refresh replayed as access = %v, want ErrInvalidToken", err)
	}
}

// TestNew_EnforcesMinSecretLen asserts the constructor rejects weak secrets
// unless explicitly allowed.
func TestNew_EnforcesMinSecretLen(t *testing.T) {
	if _, err := New(Config{Secret: strings.Repeat("x", MinSecretLen-1)}); err == nil {
		t.Fatal("short secret accepted without AllowWeakSecret")
	}
	if _, err := New(Config{Secret: ""}); err == nil {
		t.Fatal("empty secret accepted")
	}
	if _, err := New(Config{Secret: "short", AllowWeakSecret: true}); err != nil {
		t.Fatalf("AllowWeakSecret should permit short secret: %v", err)
	}
	if _, err := New(Config{Secret: strings.Repeat("x", MinSecretLen)}); err != nil {
		t.Fatalf("exactly MinSecretLen rejected: %v", err)
	}
}
