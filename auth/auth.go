// Package auth provides framework-agnostic JWT signing/parsing and
// password hashing for lagodev-based applications.
//
// The Manager holds a signing secret plus TTL configuration and produces
// signed JWTs from Claims. Parse verifies the signature and expiry. The
// package returns ErrInvalidToken / ErrExpiredToken so callers can map them
// to HTTP responses without inspecting jwt-library error strings.
//
// Password helpers wrap golang.org/x/crypto/bcrypt with the project's
// preferred cost so storage layers don't need to import bcrypt directly.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidToken is returned when the token is malformed, has a bad
// signature, or fails any of the registered validators.
var ErrInvalidToken = errors.New("auth: invalid token")

// ErrExpiredToken is returned when the token's exp claim is in the past.
var ErrExpiredToken = errors.New("auth: token expired")

// Default token types — applications may define their own (e.g. "api").
const (
	TokenAccess  = "access"
	TokenRefresh = "refresh"
)

// Config holds the signing secret and token lifetimes. Secret must be a
// strong random value (>=32 bytes by default; relax with AllowWeakSecret).
// Issuer is optional and embedded into the iss claim when non-empty; when
// set it is also verified on Parse. Audience is optional; when set it is
// embedded into the aud claim and verified on Parse.
type Config struct {
	Secret     string
	Issuer     string
	Audience   string
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	BcryptCost int
	// Leeway is the clock-skew tolerance applied to exp/nbf/iat validation.
	// Defaults to 30s when zero.
	Leeway time.Duration
	// AllowWeakSecret skips the >=32 byte secret length check in New. Only
	// set this in tests or when the secret strength is guaranteed elsewhere.
	AllowWeakSecret bool
}

// MinSecretLen is the minimum byte length enforced for Config.Secret unless
// AllowWeakSecret is set.
const MinSecretLen = 32

// DefaultLeeway is the clock-skew tolerance applied when Config.Leeway is zero.
const DefaultLeeway = 30 * time.Second

// Claims is the payload carried inside every JWT issued by Manager.
// Embed jwt.RegisteredClaims for iss/sub/exp/iat/nbf/jti.
type Claims struct {
	UserID uint64 `json:"uid"`
	Role   string `json:"role,omitempty"`
	Type   string `json:"typ,omitempty"`
	jwt.RegisteredClaims
}

// Manager issues and verifies tokens.
type Manager struct {
	cfg Config
}

// New returns a Manager. The secret must be non-empty and at least
// MinSecretLen bytes (unless cfg.AllowWeakSecret is set).
func New(cfg Config) (*Manager, error) {
	if cfg.Secret == "" {
		return nil, errors.New("auth: secret is required")
	}
	if !cfg.AllowWeakSecret && len(cfg.Secret) < MinSecretLen {
		return nil, fmt.Errorf("auth: secret must be at least %d bytes (set AllowWeakSecret to relax)", MinSecretLen)
	}
	if cfg.AccessTTL <= 0 {
		cfg.AccessTTL = 15 * time.Minute
	}
	if cfg.RefreshTTL <= 0 {
		cfg.RefreshTTL = 30 * 24 * time.Hour
	}
	if cfg.BcryptCost <= 0 {
		cfg.BcryptCost = bcrypt.DefaultCost
	}
	if cfg.Leeway <= 0 {
		cfg.Leeway = DefaultLeeway
	}
	return &Manager{cfg: cfg}, nil
}

// IssueAccess signs an access token (short-lived) for the given user.
func (m *Manager) IssueAccess(userID uint64, role string) (string, time.Time, error) {
	return m.issue(userID, role, TokenAccess, m.cfg.AccessTTL)
}

// IssueRefresh signs a refresh token (long-lived) for the given user.
func (m *Manager) IssueRefresh(userID uint64, role string) (string, time.Time, error) {
	return m.issue(userID, role, TokenRefresh, m.cfg.RefreshTTL)
}

// Issue signs a custom token type with a caller-specified TTL.
func (m *Manager) Issue(userID uint64, role, tokenType string, ttl time.Duration) (string, time.Time, error) {
	return m.issue(userID, role, tokenType, ttl)
}

func (m *Manager) issue(userID uint64, role, tokenType string, ttl time.Duration) (string, time.Time, error) {
	now := time.Now()
	expires := now.Add(ttl)
	jti, err := newJTI()
	if err != nil {
		return "", time.Time{}, err
	}
	claims := Claims{
		UserID: userID,
		Role:   role,
		Type:   tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Issuer:    m.cfg.Issuer,
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	}
	if m.cfg.Audience != "" {
		claims.Audience = jwt.ClaimStrings{m.cfg.Audience}
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(m.cfg.Secret))
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expires, nil
}

// newJTI returns a 128-bit random token identifier (URL-safe base64). It makes
// every issued token unique even when (user, role, type, iat) are identical,
// so callers can safely store a UNIQUE hash of the token without spurious
// "UNIQUE constraint failed" collisions on rapid re-issue.
func newJTI() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("auth: jti generation: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Parse verifies the token's signature, expiry, issuer/audience (when
// configured), and applies the configured clock-skew leeway. It returns the
// claims on success, or ErrInvalidToken / ErrExpiredToken on failure.
func (m *Manager) Parse(token string) (*Claims, error) {
	claims := &Claims{}
	opts := []jwt.ParserOption{jwt.WithLeeway(m.cfg.Leeway)}
	if m.cfg.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(m.cfg.Issuer))
	}
	if m.cfg.Audience != "" {
		opts = append(opts, jwt.WithAudience(m.cfg.Audience))
	}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(m.cfg.Secret), nil
	}, opts...)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}
	if !parsed.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// ParseTyped is Parse plus an assertion that the token's Type claim equals
// expected. It returns ErrInvalidToken when the type does not match, so an
// access token cannot be replayed where a refresh token is required (and
// vice versa).
func (m *Manager) ParseTyped(token, expectedType string) (*Claims, error) {
	claims, err := m.Parse(token)
	if err != nil {
		return nil, err
	}
	if claims.Type != expectedType {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// ParseAccess parses token and asserts it is an access token (Type == TokenAccess).
func (m *Manager) ParseAccess(token string) (*Claims, error) {
	return m.ParseTyped(token, TokenAccess)
}

// ParseRefresh parses token and asserts it is a refresh token (Type == TokenRefresh).
func (m *Manager) ParseRefresh(token string) (*Claims, error) {
	return m.ParseTyped(token, TokenRefresh)
}

// HashPassword returns a bcrypt hash of the password using the configured cost.
func (m *Manager) HashPassword(password string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), m.cfg.BcryptCost)
	if err != nil {
		return "", err
	}
	return string(hashed), nil
}

// VerifyPassword reports whether password matches the stored bcrypt hash.
func (m *Manager) VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// AccessTTL returns the configured access-token lifetime.
func (m *Manager) AccessTTL() time.Duration { return m.cfg.AccessTTL }

// RefreshTTL returns the configured refresh-token lifetime.
func (m *Manager) RefreshTTL() time.Duration { return m.cfg.RefreshTTL }
