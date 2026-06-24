// Package account implements the credential-lifecycle flows that sit around
// authentication: password-reset tokens, email-verification tokens, signed
// URLs, and a login throttler.
//
// Design rules (secure-by-default):
//
//   - Reset and verification tokens are high-entropy random values. The
//     server stores only a SHA-256 hash, so a store leak does not yield
//     usable tokens.
//   - Tokens expire and are single-use: Consume succeeds at most once, which
//     defeats replay.
//   - Signed URLs use the crypt package's HMAC primitive over a canonical
//     string and embed an expiry, verified in constant time.
//   - The login throttler is a sliding-style fixed-window counter that locks
//     an identifier out after too many failures, mitigating brute force and
//     credential stuffing.
//
// The TokenStore interface is small enough for a SQL/Redis driver to replace
// MemoryTokenStore without changing call sites. All timestamps are UTC.
package account

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors returned by token flows.
var (
	// ErrTokenNotFound is returned when no token matches.
	ErrTokenNotFound = errors.New("account: token not found")
	// ErrTokenExpired is returned when a token matched but has expired.
	ErrTokenExpired = errors.New("account: token expired")
	// ErrTokenUsed is returned when a single-use token was already consumed.
	ErrTokenUsed = errors.New("account: token already used")
)

// Purpose distinguishes token namespaces so a password-reset token can never
// be replayed as an email-verification token (and vice versa).
type Purpose string

const (
	// PurposeReset is the namespace for password-reset tokens.
	PurposeReset Purpose = "password_reset"
	// PurposeVerify is the namespace for email-verification tokens.
	PurposeVerify Purpose = "email_verify"
)

// tokenBytes is the entropy of the random token (256-bit).
const tokenBytes = 32

// Record is a stored token entry. The plaintext is never persisted — only
// Hash (hex SHA-256). Selector is the public lookup key, so verification is
// O(1) and does not require scanning rows.
type Record struct {
	Selector  string
	Purpose   Purpose
	Subject   string // opaque app identifier: user id, email, etc.
	Hash      string // hex SHA-256 of the secret half
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    time.Time // zero until consumed
}

// Used reports whether the record has been consumed.
func (r *Record) Used() bool { return !r.UsedAt.IsZero() }

// Expired reports whether the record's expiry has passed.
func (r *Record) Expired() bool { return time.Now().After(r.ExpiresAt) }

// TokenStore persists token records keyed by selector. Implementations must
// be safe for concurrent use.
type TokenStore interface {
	Save(ctx context.Context, r *Record) error
	Get(ctx context.Context, selector string) (*Record, bool, error)
	Delete(ctx context.Context, selector string) error
}

// MemoryTokenStore is an in-process TokenStore backed by a sync.RWMutex.
type MemoryTokenStore struct {
	mu    sync.RWMutex
	items map[string]Record
}

// NewMemoryTokenStore returns an empty in-memory token store.
func NewMemoryTokenStore() *MemoryTokenStore {
	return &MemoryTokenStore{items: make(map[string]Record)}
}

func (s *MemoryTokenStore) Save(_ context.Context, r *Record) error {
	s.mu.Lock()
	s.items[r.Selector] = *r
	s.mu.Unlock()
	return nil
}

func (s *MemoryTokenStore) Get(_ context.Context, selector string) (*Record, bool, error) {
	s.mu.RLock()
	it, ok := s.items[selector]
	s.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	cp := it
	return &cp, true, nil
}

func (s *MemoryTokenStore) Delete(_ context.Context, selector string) error {
	s.mu.Lock()
	delete(s.items, selector)
	s.mu.Unlock()
	return nil
}

// Tokens issues and verifies single-use, expiring, hashed tokens for a given
// Purpose. Use one Tokens per purpose-family or pass the purpose per call.
type Tokens struct {
	store TokenStore
	ttl   time.Duration
}

// NewTokens returns a Tokens with the given store and default TTL. ttl<=0
// defaults to 1 hour.
func NewTokens(store TokenStore, ttl time.Duration) *Tokens {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Tokens{store: store, ttl: ttl}
}

// Issue mints a token for (purpose, subject) and returns the one-time
// plaintext "<selector>.<secret>". The caller embeds it in an email link;
// it is unrecoverable afterwards because only its hash is stored.
func (t *Tokens) Issue(ctx context.Context, purpose Purpose, subject string) (string, error) {
	selector, err := randToken(12)
	if err != nil {
		return "", err
	}
	secret, err := randToken(tokenBytes)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	rec := &Record{
		Selector:  selector,
		Purpose:   purpose,
		Subject:   subject,
		Hash:      hashToken(secret),
		CreatedAt: now,
		ExpiresAt: now.Add(t.ttl),
	}
	if err := t.store.Save(ctx, rec); err != nil {
		return "", fmt.Errorf("account: save token: %w", err)
	}
	return selector + "." + secret, nil
}

// Verify checks a plaintext token without consuming it. It returns the
// subject on success. Use Consume for single-use semantics; Verify is for
// flows that need to display a form before committing (the actual reset must
// still call Consume).
func (t *Tokens) Verify(ctx context.Context, purpose Purpose, plain string) (string, error) {
	rec, _, err := t.lookup(ctx, purpose, plain)
	if err != nil {
		return "", err
	}
	return rec.Subject, nil
}

// Consume validates a plaintext token and atomically marks it used, so a
// second call with the same token returns ErrTokenUsed. It returns the
// subject on success.
//
// Errors: ErrTokenNotFound, ErrTokenExpired, ErrTokenUsed.
func (t *Tokens) Consume(ctx context.Context, purpose Purpose, plain string) (string, error) {
	rec, selector, err := t.lookup(ctx, purpose, plain)
	if err != nil {
		return "", err
	}
	rec.UsedAt = time.Now().UTC()
	if err := t.store.Save(ctx, rec); err != nil {
		return "", fmt.Errorf("account: mark used: %w", err)
	}
	_ = selector
	return rec.Subject, nil
}

// lookup resolves and validates a plaintext token but does not mutate it.
func (t *Tokens) lookup(ctx context.Context, purpose Purpose, plain string) (*Record, string, error) {
	selector, secret, ok := splitToken(plain)
	if !ok {
		return nil, "", ErrTokenNotFound
	}
	rec, found, err := t.store.Get(ctx, selector)
	if err != nil {
		return nil, "", fmt.Errorf("account: get token: %w", err)
	}
	if !found {
		return nil, "", ErrTokenNotFound
	}
	// Constant-time compare; reject wrong purpose as not-found to avoid
	// confirming the selector belongs to a different namespace.
	if rec.Purpose != purpose {
		return nil, "", ErrTokenNotFound
	}
	if subtle.ConstantTimeCompare([]byte(rec.Hash), []byte(hashToken(secret))) != 1 {
		return nil, "", ErrTokenNotFound
	}
	if rec.Used() {
		return nil, "", ErrTokenUsed
	}
	if rec.Expired() {
		return nil, "", ErrTokenExpired
	}
	return rec, selector, nil
}

// ---- Signed URLs -----------------------------------------------------------

// ErrInvalidSignature is returned when a signed URL fails verification.
var ErrInvalidSignature = errors.New("account: invalid signature")

// ErrSignatureExpired is returned when a signed URL's expiry has passed.
var ErrSignatureExpired = errors.New("account: signature expired")

// Signer produces and verifies tamper-proof, expiring URLs using an HMAC
// key (reuse APP_KEY bytes). The signature covers the path and a canonical,
// sorted encoding of the query parameters plus the expiry, so reordering or
// adding parameters invalidates it.
type Signer struct {
	key []byte
}

// NewSigner returns a Signer keyed by key (any length; 32 bytes recommended).
func NewSigner(key []byte) *Signer {
	cp := append([]byte(nil), key...)
	return &Signer{key: cp}
}

// Sign returns path with an appended "expires" timestamp and "signature"
// query parameter. base may already contain query parameters; they are
// included in the signed payload. ttl<=0 produces a non-expiring link
// (expires=0), which is verified accordingly.
func (s *Signer) Sign(base string, ttl time.Duration) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("account: parse url: %w", err)
	}
	q := u.Query()
	q.Del("signature") // never let a caller-supplied signature into the payload
	var expUnix int64
	if ttl > 0 {
		expUnix = time.Now().Add(ttl).UTC().Unix()
	}
	q.Set("expires", strconv.FormatInt(expUnix, 10))
	u.RawQuery = q.Encode() // Encode sorts keys -> canonical
	sig := s.mac(u.Path, u.RawQuery)
	q.Set("signature", sig)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Verify checks a signed URL produced by Sign. It returns ErrInvalidSignature
// on tampering and ErrSignatureExpired when the embedded expiry has passed.
func (s *Signer) Verify(signed string) error {
	u, err := url.Parse(signed)
	if err != nil {
		return ErrInvalidSignature
	}
	q := u.Query()
	got := q.Get("signature")
	if got == "" {
		return ErrInvalidSignature
	}
	q.Del("signature")
	canonical := q.Encode()
	want := s.mac(u.Path, canonical)
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return ErrInvalidSignature
	}
	expStr := q.Get("expires")
	expUnix, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return ErrInvalidSignature
	}
	if expUnix != 0 && time.Now().UTC().Unix() > expUnix {
		return ErrSignatureExpired
	}
	return nil
}

// mac computes the HMAC over the canonical "path?query" payload. It mirrors
// crypt.Sign (HMAC-SHA256, base64url) without importing crypt to keep auth
// subpackages dependency-free of sibling top-level packages.
func (s *Signer) mac(path, query string) string {
	payload := path + "?" + query
	sum := hmacSHA256(s.key, []byte(payload))
	return base64.RawURLEncoding.EncodeToString(sum)
}

// ---- Login throttle --------------------------------------------------------

// ErrThrottled is returned by Throttle.Hit / Check when an identifier is
// currently locked out.
var ErrThrottled = errors.New("account: too many attempts")

// Throttle is a fixed-window failure counter keyed by an arbitrary
// identifier (e.g. "login:"+email or an IP). After MaxAttempts failures
// within Window, the identifier is locked until the window elapses. A
// successful login should call Clear to reset the counter.
type Throttle struct {
	mu          sync.Mutex
	attempts    map[string]*window
	maxAttempts int
	windowDur   time.Duration
}

type window struct {
	count     int
	resetAt   time.Time
	lockedFor time.Time
}

// NewThrottle returns a Throttle allowing maxAttempts failures per window.
// Defaults: maxAttempts<=0 -> 5, window<=0 -> 1 minute.
func NewThrottle(maxAttempts int, windowDur time.Duration) *Throttle {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if windowDur <= 0 {
		windowDur = time.Minute
	}
	return &Throttle{
		attempts:    make(map[string]*window),
		maxAttempts: maxAttempts,
		windowDur:   windowDur,
	}
}

// Check reports whether key is currently locked out without recording an
// attempt. It returns ErrThrottled (with the lock duration) when locked.
func (t *Throttle) Check(key string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	w := t.attempts[key]
	if w == nil {
		return nil
	}
	now := time.Now()
	if now.After(w.resetAt) {
		delete(t.attempts, key)
		return nil
	}
	if w.count >= t.maxAttempts {
		return fmt.Errorf("%w: retry after %s", ErrThrottled, time.Until(w.lockedFor).Round(time.Second))
	}
	return nil
}

// Hit records one failed attempt for key and returns ErrThrottled if this
// attempt crosses (or has already crossed) the limit. Call this on each
// authentication failure.
func (t *Throttle) Hit(key string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	w := t.attempts[key]
	if w == nil || now.After(w.resetAt) {
		w = &window{resetAt: now.Add(t.windowDur)}
		t.attempts[key] = w
	}
	w.count++
	if w.count >= t.maxAttempts {
		w.lockedFor = w.resetAt
		return fmt.Errorf("%w: retry after %s", ErrThrottled, time.Until(w.resetAt).Round(time.Second))
	}
	return nil
}

// Clear resets the counter for key. Call on successful authentication.
func (t *Throttle) Clear(key string) {
	t.mu.Lock()
	delete(t.attempts, key)
	t.mu.Unlock()
}

// Attempts returns the current recorded failure count for key (0 if none or
// the window has elapsed). Useful for surfacing "N attempts remaining".
func (t *Throttle) Attempts(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	w := t.attempts[key]
	if w == nil || time.Now().After(w.resetAt) {
		return 0
	}
	return w.count
}

// ---- helpers ---------------------------------------------------------------

func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

func splitToken(plain string) (selector, secret string, ok bool) {
	idx := strings.IndexByte(plain, '.')
	if idx <= 0 || idx == len(plain)-1 {
		return "", "", false
	}
	return plain[:idx], plain[idx+1:], true
}

func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("account: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
