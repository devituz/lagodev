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
// strong random value (>=32 bytes). Issuer is optional and embedded into
// the iss claim when non-empty.
type Config struct {
	Secret      string
	Issuer      string
	AccessTTL   time.Duration
	RefreshTTL  time.Duration
	BcryptCost  int
}

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

// New returns a Manager. The secret must be non-empty.
func New(cfg Config) (*Manager, error) {
	if cfg.Secret == "" {
		return nil, errors.New("auth: secret is required")
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
	claims := Claims{
		UserID: userID,
		Role:   role,
		Type:   tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.cfg.Issuer,
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(m.cfg.Secret))
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expires, nil
}

// Parse verifies the token's signature and expiry. It returns the claims
// on success, or ErrInvalidToken / ErrExpiredToken on failure.
func (m *Manager) Parse(token string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(m.cfg.Secret), nil
	})
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

// Config returns the manager's configuration (without copying the secret pointer).
func (m *Manager) AccessTTL() time.Duration  { return m.cfg.AccessTTL }
func (m *Manager) RefreshTTL() time.Duration { return m.cfg.RefreshTTL }
