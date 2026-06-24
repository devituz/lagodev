// Package token implements Sanctum-style personal access tokens for
// lagodev-based applications.
//
// A personal access token is an opaque, high-entropy string handed to an
// API client once at creation time. The server never stores the plaintext:
// only a SHA-256 hash is persisted, so a database leak does not expose
// usable credentials. Each token carries a set of abilities (scopes), an
// optional expiry, and a last-used timestamp.
//
// The wire format is "<id>|<secret>" (Sanctum-compatible): the id selects
// the stored record in O(1) and the secret is hash-compared in constant
// time. Lookups therefore never require scanning every row.
//
// The Store interface is intentionally small so a SQL or Redis driver can
// replace MemoryStore without touching call sites. All times are UTC.
package token

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Errors returned by this package. Callers map these to HTTP responses
// (404/401/403) without inspecting strings.
var (
	// ErrNotFound is returned when no token matches the lookup.
	ErrNotFound = errors.New("token: not found")
	// ErrExpired is returned when a token matched but its expiry has passed.
	ErrExpired = errors.New("token: expired")
	// ErrRevoked is returned when a token matched but was revoked.
	ErrRevoked = errors.New("token: revoked")
	// ErrMalformed is returned when a plaintext token is not in the
	// "<id>|<secret>" form.
	ErrMalformed = errors.New("token: malformed")
)

// secretBytes is the entropy of the random secret half (256-bit).
const secretBytes = 32

// AbilityAll is the wildcard ability granting every scope. A token holding
// it passes Can(x) for any x.
const AbilityAll = "*"

// PersonalAccessToken is the stored record. The plaintext secret is never
// persisted — only Hash (hex SHA-256 of the secret half).
type PersonalAccessToken struct {
	// ID is the public selector (random, URL-safe). It is also the lookup key.
	ID string
	// UserID owns the token.
	UserID uint64
	// Name is a human label ("CI deploy key").
	Name string
	// Hash is the hex-encoded SHA-256 of the secret half. Never the plaintext.
	Hash string
	// Abilities are the granted scopes. AbilityAll ("*") grants everything.
	Abilities []string
	// CreatedAt / ExpiresAt are UTC. ExpiresAt zero means "never expires".
	CreatedAt time.Time
	ExpiresAt time.Time
	// LastUsedAt is UTC and zero until the token is first matched by Find.
	LastUsedAt time.Time
	// RevokedAt is UTC and zero unless the token has been revoked.
	RevokedAt time.Time
}

// Expired reports whether the token has an expiry in the past.
func (t *PersonalAccessToken) Expired() bool {
	return !t.ExpiresAt.IsZero() && time.Now().After(t.ExpiresAt)
}

// Revoked reports whether the token has been revoked.
func (t *PersonalAccessToken) Revoked() bool {
	return !t.RevokedAt.IsZero()
}

// Can reports whether the token grants ability. A token holding AbilityAll
// grants every ability.
func (t *PersonalAccessToken) Can(ability string) bool {
	for _, a := range t.Abilities {
		if a == AbilityAll || a == ability {
			return true
		}
	}
	return false
}

// Cant is the negation of Can.
func (t *PersonalAccessToken) Cant(ability string) bool { return !t.Can(ability) }

// Store persists access tokens keyed by their public ID. Implementations
// must be safe for concurrent use.
type Store interface {
	// Save inserts or replaces a token record.
	Save(ctx context.Context, t *PersonalAccessToken) error
	// Get returns the record for id. ok=false on miss.
	Get(ctx context.Context, id string) (*PersonalAccessToken, bool, error)
	// Delete removes the record for id (no-op if absent).
	Delete(ctx context.Context, id string) error
	// ListByUser returns every token owned by userID (including expired and
	// revoked records, so management UIs can show them).
	ListByUser(ctx context.Context, userID uint64) ([]*PersonalAccessToken, error)
}

// MemoryStore is an in-process Store backed by a sync.RWMutex. It is meant
// for single-replica apps and tests; swap for a DB-backed Store in
// production multi-replica deployments.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]PersonalAccessToken
}

// NewMemoryStore returns an empty in-memory Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]PersonalAccessToken)}
}

func (s *MemoryStore) Save(_ context.Context, t *PersonalAccessToken) error {
	s.mu.Lock()
	s.items[t.ID] = *t // store a copy; callers can't mutate state behind our back
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (*PersonalAccessToken, bool, error) {
	s.mu.RLock()
	it, ok := s.items[id]
	s.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	cp := it
	return &cp, true, nil
}

func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.items, id)
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) ListByUser(_ context.Context, userID uint64) ([]*PersonalAccessToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*PersonalAccessToken
	for _, it := range s.items {
		if it.UserID == userID {
			cp := it
			out = append(out, &cp)
		}
	}
	return out, nil
}

// Issuer mints and verifies personal access tokens against a Store.
type Issuer struct {
	store Store
}

// NewIssuer returns an Issuer backed by store.
func NewIssuer(store Store) *Issuer {
	return &Issuer{store: store}
}

// Issue creates a new token for userID with the given name, abilities, and
// TTL (ttl<=0 means "never expires"). It returns the stored record plus the
// one-time plaintext token ("<id>|<secret>") which the caller must show the
// user immediately — it is unrecoverable afterwards.
func (i *Issuer) Issue(ctx context.Context, userID uint64, name string, abilities []string, ttl time.Duration) (*PersonalAccessToken, string, error) {
	id, err := randID()
	if err != nil {
		return nil, "", err
	}
	secret, err := randSecret()
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	var exp time.Time
	if ttl > 0 {
		exp = now.Add(ttl)
	}
	// Defensive copy of abilities so later mutation of the caller's slice
	// can't change the stored grant set.
	ab := append([]string(nil), abilities...)
	rec := &PersonalAccessToken{
		ID:        id,
		UserID:    userID,
		Name:      name,
		Hash:      hashSecret(secret),
		Abilities: ab,
		CreatedAt: now,
		ExpiresAt: exp,
	}
	if err := i.store.Save(ctx, rec); err != nil {
		return nil, "", fmt.Errorf("token: save: %w", err)
	}
	plain := id + "|" + secret
	cp := *rec
	return &cp, plain, nil
}

// Find resolves a plaintext token to its record. It validates the format,
// looks up the record by id, constant-time-compares the secret hash, and
// rejects expired/revoked tokens. On success it stamps LastUsedAt (best
// effort) and returns the (updated) record.
//
// Errors: ErrMalformed, ErrNotFound, ErrExpired, ErrRevoked.
func (i *Issuer) Find(ctx context.Context, plain string) (*PersonalAccessToken, error) {
	id, secret, ok := splitToken(plain)
	if !ok {
		return nil, ErrMalformed
	}
	rec, found, err := i.store.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("token: get: %w", err)
	}
	if !found {
		return nil, ErrNotFound
	}
	// Constant-time hash comparison: never branch on secret content.
	want := hashSecret(secret)
	if subtle.ConstantTimeCompare([]byte(rec.Hash), []byte(want)) != 1 {
		return nil, ErrNotFound
	}
	if rec.Revoked() {
		return nil, ErrRevoked
	}
	if rec.Expired() {
		return nil, ErrExpired
	}
	// Best-effort last-used stamp; a write failure must not deny a valid token.
	rec.LastUsedAt = time.Now().UTC()
	_ = i.store.Save(ctx, rec)
	return rec, nil
}

// Revoke marks the token with the given id revoked. It is idempotent and
// returns ErrNotFound only when no such id exists. Revoked tokens are kept
// (not deleted) so audit/listing still shows them; use Delete to purge.
func (i *Issuer) Revoke(ctx context.Context, id string) error {
	rec, found, err := i.store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("token: get: %w", err)
	}
	if !found {
		return ErrNotFound
	}
	if rec.Revoked() {
		return nil
	}
	rec.RevokedAt = time.Now().UTC()
	if err := i.store.Save(ctx, rec); err != nil {
		return fmt.Errorf("token: save: %w", err)
	}
	return nil
}

// RevokePlain revokes the token identified by a plaintext value. It does not
// require the secret to match the stored hash beyond locating the id, but it
// still validates format. Useful for "log out this token" flows where the
// client presents its own token.
func (i *Issuer) RevokePlain(ctx context.Context, plain string) error {
	id, _, ok := splitToken(plain)
	if !ok {
		return ErrMalformed
	}
	return i.Revoke(ctx, id)
}

// List returns all tokens owned by userID.
func (i *Issuer) List(ctx context.Context, userID uint64) ([]*PersonalAccessToken, error) {
	return i.store.ListByUser(ctx, userID)
}

// hashSecret returns the hex SHA-256 of the secret half. SHA-256 (not
// bcrypt) is appropriate here because the secret is high-entropy random
// data, not a low-entropy human password — there is nothing to brute-force.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// splitToken parses "<id>|<secret>" into its halves.
func splitToken(plain string) (id, secret string, ok bool) {
	idx := strings.IndexByte(plain, '|')
	if idx <= 0 || idx == len(plain)-1 {
		return "", "", false
	}
	return plain[:idx], plain[idx+1:], true
}

// randID returns a short URL-safe random selector.
func randID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("token: id generation: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// randSecret returns the high-entropy secret half.
func randSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("token: secret generation: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
