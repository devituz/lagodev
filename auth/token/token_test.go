package token

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// newIssuer returns an Issuer backed by a fresh MemoryStore.
func newIssuer(t *testing.T) (*Issuer, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	return NewIssuer(store), store
}

// --- PersonalAccessToken value-method tests -------------------------------

func TestExpired(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		expires time.Time
		want    bool
	}{
		{"zero never expires", time.Time{}, false},
		{"future", now.Add(time.Hour), false},
		{"past", now.Add(-time.Hour), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pat := &PersonalAccessToken{ExpiresAt: tc.expires}
			if got := pat.Expired(); got != tc.want {
				t.Fatalf("Expired() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRevoked(t *testing.T) {
	tests := []struct {
		name    string
		revoked time.Time
		want    bool
	}{
		{"zero not revoked", time.Time{}, false},
		{"set revoked", time.Now(), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pat := &PersonalAccessToken{RevokedAt: tc.revoked}
			if got := pat.Revoked(); got != tc.want {
				t.Fatalf("Revoked() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCanCant(t *testing.T) {
	tests := []struct {
		name      string
		abilities []string
		ability   string
		wantCan   bool
	}{
		{"exact match", []string{"read", "write"}, "read", true},
		{"second match", []string{"read", "write"}, "write", true},
		{"no match", []string{"read"}, "delete", false},
		{"empty abilities", nil, "read", false},
		{"wildcard grants anything", []string{AbilityAll}, "anything", true},
		{"wildcard grants empty too", []string{AbilityAll}, "", true},
		{"empty ability with empty grant", []string{""}, "", true},
		{"literal star only matches wildcard semantics", []string{"*"}, "x", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pat := &PersonalAccessToken{Abilities: tc.abilities}
			if got := pat.Can(tc.ability); got != tc.wantCan {
				t.Fatalf("Can(%q) = %v, want %v", tc.ability, got, tc.wantCan)
			}
			if got := pat.Cant(tc.ability); got != !tc.wantCan {
				t.Fatalf("Cant(%q) = %v, want %v", tc.ability, got, !tc.wantCan)
			}
		})
	}
}

// --- MemoryStore tests ----------------------------------------------------

func TestMemoryStoreSaveGetDelete(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	if _, ok, err := s.Get(ctx, "missing"); err != nil || ok {
		t.Fatalf("Get(missing) = ok %v, err %v; want false, nil", ok, err)
	}

	rec := &PersonalAccessToken{ID: "abc", UserID: 7, Name: "k"}
	if err := s.Save(ctx, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok, err := s.Get(ctx, "abc")
	if err != nil || !ok {
		t.Fatalf("Get(abc) = ok %v, err %v; want true, nil", ok, err)
	}
	if got.UserID != 7 || got.Name != "k" {
		t.Fatalf("Get returned %+v", got)
	}

	// Get must return a copy: mutating it must not affect the store.
	got.Name = "mutated"
	again, _, _ := s.Get(ctx, "abc")
	if again.Name != "k" {
		t.Fatalf("store mutated via returned pointer: %q", again.Name)
	}

	// Save must copy: mutating the caller's record must not affect the store.
	rec.Name = "after-save"
	stored, _, _ := s.Get(ctx, "abc")
	if stored.Name != "k" {
		t.Fatalf("store mutated via caller pointer: %q", stored.Name)
	}

	if err := s.Delete(ctx, "abc"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Get(ctx, "abc"); ok {
		t.Fatalf("record still present after Delete")
	}
	// Delete is a no-op on a missing key.
	if err := s.Delete(ctx, "missing"); err != nil {
		t.Fatalf("Delete(missing): %v", err)
	}
}

func TestMemoryStoreListByUser(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	if got, err := s.ListByUser(ctx, 1); err != nil || got != nil {
		t.Fatalf("ListByUser(empty) = %v, %v; want nil, nil", got, err)
	}

	_ = s.Save(ctx, &PersonalAccessToken{ID: "a", UserID: 1})
	_ = s.Save(ctx, &PersonalAccessToken{ID: "b", UserID: 1})
	_ = s.Save(ctx, &PersonalAccessToken{ID: "c", UserID: 2})

	u1, err := s.ListByUser(ctx, 1)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(u1) != 2 {
		t.Fatalf("ListByUser(1) returned %d, want 2", len(u1))
	}
	for _, it := range u1 {
		if it.UserID != 1 {
			t.Fatalf("ListByUser(1) returned foreign user %d", it.UserID)
		}
	}
	u2, _ := s.ListByUser(ctx, 2)
	if len(u2) != 1 || u2[0].ID != "c" {
		t.Fatalf("ListByUser(2) = %+v", u2)
	}
	if got, _ := s.ListByUser(ctx, 999); got != nil {
		t.Fatalf("ListByUser(unknown) = %+v, want nil", got)
	}
}

// --- Issuer.Issue ---------------------------------------------------------

func TestIssue(t *testing.T) {
	ctx := context.Background()
	iss, store := newIssuer(t)

	abilities := []string{"read", "write"}
	rec, plain, err := iss.Issue(ctx, 42, "CI key", abilities, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Plaintext is "<id>|<secret>" and the id half equals rec.ID.
	id, secret, ok := splitToken(plain)
	if !ok {
		t.Fatalf("Issue returned malformed plaintext %q", plain)
	}
	if id != rec.ID {
		t.Fatalf("plaintext id %q != rec.ID %q", id, rec.ID)
	}
	if secret == "" {
		t.Fatalf("empty secret half")
	}

	// Plaintext secret is never stored; only its hash.
	if rec.Hash != hashSecret(secret) {
		t.Fatalf("rec.Hash does not match hash of secret")
	}
	if strings.Contains(rec.Hash, secret) {
		t.Fatalf("hash contains plaintext secret")
	}

	// Record metadata.
	if rec.UserID != 42 || rec.Name != "CI key" {
		t.Fatalf("metadata: %+v", rec)
	}
	if rec.CreatedAt.IsZero() || rec.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt = %v (want non-zero UTC)", rec.CreatedAt)
	}
	if rec.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt zero despite positive TTL")
	}
	if rec.ExpiresAt.Location() != time.UTC {
		t.Fatalf("ExpiresAt not UTC: %v", rec.ExpiresAt)
	}
	if !rec.LastUsedAt.IsZero() {
		t.Fatalf("LastUsedAt should be zero on a fresh token")
	}

	// Defensive copy: mutating caller's slice must not change stored grants.
	abilities[0] = "hijacked"
	if rec.Can("hijacked") {
		t.Fatalf("stored abilities aliased the caller's slice")
	}
	if !rec.Can("read") || !rec.Can("write") {
		t.Fatalf("abilities not preserved: %v", rec.Abilities)
	}

	// The record was persisted under its id.
	stored, found, err := store.Get(ctx, rec.ID)
	if err != nil || !found {
		t.Fatalf("Get after Issue: found %v err %v", found, err)
	}
	if stored.Hash != rec.Hash {
		t.Fatalf("stored hash mismatch")
	}
}

func TestIssueNeverExpires(t *testing.T) {
	ctx := context.Background()
	iss, _ := newIssuer(t)
	for _, ttl := range []time.Duration{0, -time.Hour} {
		rec, _, err := iss.Issue(ctx, 1, "n", nil, ttl)
		if err != nil {
			t.Fatalf("Issue(ttl=%v): %v", ttl, err)
		}
		if !rec.ExpiresAt.IsZero() {
			t.Fatalf("ttl=%v: ExpiresAt = %v, want zero", ttl, rec.ExpiresAt)
		}
		if rec.Expired() {
			t.Fatalf("ttl=%v: token reported expired", ttl)
		}
	}
}

func TestIssueUniqueIDs(t *testing.T) {
	ctx := context.Background()
	iss, _ := newIssuer(t)
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		rec, _, err := iss.Issue(ctx, 1, "k", nil, time.Hour)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if seen[rec.ID] {
			t.Fatalf("duplicate id %q", rec.ID)
		}
		seen[rec.ID] = true
	}
}

// --- Issuer.Find ----------------------------------------------------------

func TestFindHappyPath(t *testing.T) {
	ctx := context.Background()
	iss, _ := newIssuer(t)

	rec, plain, err := iss.Issue(ctx, 9, "k", []string{"read"}, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	got, err := iss.Find(ctx, plain)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.ID != rec.ID || got.UserID != 9 {
		t.Fatalf("Find returned %+v", got)
	}
	// LastUsedAt is stamped on success.
	if got.LastUsedAt.IsZero() {
		t.Fatalf("LastUsedAt not stamped after Find")
	}
	if got.LastUsedAt.Location() != time.UTC {
		t.Fatalf("LastUsedAt not UTC: %v", got.LastUsedAt)
	}

	// The stamp is persisted.
	again, err := iss.Find(ctx, plain)
	if err != nil {
		t.Fatalf("Find (2nd): %v", err)
	}
	if again.LastUsedAt.Before(got.LastUsedAt) {
		t.Fatalf("LastUsedAt went backwards: %v then %v", got.LastUsedAt, again.LastUsedAt)
	}
}

func TestFindMalformed(t *testing.T) {
	ctx := context.Background()
	iss, _ := newIssuer(t)
	cases := []string{
		"",
		"nopipe",
		"|secret", // empty id
		"id|",     // empty secret
		"|",       // both empty
		"  ",      // no pipe
	}
	for _, plain := range cases {
		t.Run(plain, func(t *testing.T) {
			if _, err := iss.Find(ctx, plain); !errors.Is(err, ErrMalformed) {
				t.Fatalf("Find(%q) err = %v, want ErrMalformed", plain, err)
			}
		})
	}
}

func TestFindNotFound(t *testing.T) {
	ctx := context.Background()
	iss, _ := newIssuer(t)

	// Unknown id.
	if _, err := iss.Find(ctx, "ghost|secret"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Find(unknown id) err = %v, want ErrNotFound", err)
	}

	// Correct id, wrong secret -> ErrNotFound (not a distinct error, to avoid
	// leaking that the id exists).
	rec, plain, err := iss.Issue(ctx, 1, "k", nil, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := iss.Find(ctx, rec.ID+"|wrongsecret"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Find(wrong secret) err = %v, want ErrNotFound", err)
	}
	// Sanity: the right secret still works.
	if _, err := iss.Find(ctx, plain); err != nil {
		t.Fatalf("Find(correct) err = %v", err)
	}
}

func TestFindExpired(t *testing.T) {
	ctx := context.Background()
	iss, store := newIssuer(t)

	rec, plain, err := iss.Issue(ctx, 1, "k", nil, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Force expiry in the store.
	rec.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if err := store.Save(ctx, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := iss.Find(ctx, plain); !errors.Is(err, ErrExpired) {
		t.Fatalf("Find(expired) err = %v, want ErrExpired", err)
	}
}

func TestFindRevoked(t *testing.T) {
	ctx := context.Background()
	iss, _ := newIssuer(t)

	rec, plain, err := iss.Issue(ctx, 1, "k", nil, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := iss.Revoke(ctx, rec.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := iss.Find(ctx, plain); !errors.Is(err, ErrRevoked) {
		t.Fatalf("Find(revoked) err = %v, want ErrRevoked", err)
	}
}

// Revocation is checked before expiry: a token both revoked and expired
// surfaces ErrRevoked (matches code ordering).
func TestFindRevokedBeforeExpired(t *testing.T) {
	ctx := context.Background()
	iss, store := newIssuer(t)

	rec, plain, err := iss.Issue(ctx, 1, "k", nil, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	rec.RevokedAt = time.Now().UTC()
	rec.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if err := store.Save(ctx, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := iss.Find(ctx, plain); !errors.Is(err, ErrRevoked) {
		t.Fatalf("Find err = %v, want ErrRevoked", err)
	}
}

// --- Issuer.Revoke / RevokePlain -----------------------------------------

func TestRevoke(t *testing.T) {
	ctx := context.Background()
	iss, store := newIssuer(t)

	rec, _, err := iss.Issue(ctx, 1, "k", nil, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if err := iss.Revoke(ctx, rec.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got, _, _ := store.Get(ctx, rec.ID)
	if !got.Revoked() {
		t.Fatalf("token not marked revoked")
	}
	if got.RevokedAt.Location() != time.UTC {
		t.Fatalf("RevokedAt not UTC: %v", got.RevokedAt)
	}
	first := got.RevokedAt

	// Idempotent: second Revoke succeeds and does not move RevokedAt.
	if err := iss.Revoke(ctx, rec.ID); err != nil {
		t.Fatalf("Revoke (2nd): %v", err)
	}
	got2, _, _ := store.Get(ctx, rec.ID)
	if !got2.RevokedAt.Equal(first) {
		t.Fatalf("RevokedAt changed on idempotent revoke: %v -> %v", first, got2.RevokedAt)
	}

	// Revoked tokens are kept, not deleted.
	if _, found, _ := store.Get(ctx, rec.ID); !found {
		t.Fatalf("revoked token was deleted")
	}
}

func TestRevokeNotFound(t *testing.T) {
	ctx := context.Background()
	iss, _ := newIssuer(t)
	if err := iss.Revoke(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Revoke(ghost) err = %v, want ErrNotFound", err)
	}
}

func TestRevokePlain(t *testing.T) {
	ctx := context.Background()
	iss, store := newIssuer(t)

	rec, plain, err := iss.Issue(ctx, 1, "k", nil, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Malformed plaintext is rejected before any lookup.
	if err := iss.RevokePlain(ctx, "nopipe"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("RevokePlain(malformed) err = %v, want ErrMalformed", err)
	}

	// A wrong secret still revokes, since only the id half is used to locate.
	wrong := rec.ID + "|totally-wrong-secret"
	if err := iss.RevokePlain(ctx, wrong); err != nil {
		t.Fatalf("RevokePlain(wrong secret) err = %v", err)
	}
	got, _, _ := store.Get(ctx, rec.ID)
	if !got.Revoked() {
		t.Fatalf("RevokePlain did not revoke")
	}

	// Now Find with the real plaintext reports revoked.
	if _, err := iss.Find(ctx, plain); !errors.Is(err, ErrRevoked) {
		t.Fatalf("Find after RevokePlain err = %v, want ErrRevoked", err)
	}

	// Unknown id -> ErrNotFound.
	if err := iss.RevokePlain(ctx, "ghost|secret"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RevokePlain(ghost) err = %v, want ErrNotFound", err)
	}
}

// --- Issuer.List ----------------------------------------------------------

func TestList(t *testing.T) {
	ctx := context.Background()
	iss, _ := newIssuer(t)

	if got, err := iss.List(ctx, 5); err != nil || got != nil {
		t.Fatalf("List(empty) = %v, %v; want nil, nil", got, err)
	}

	a, _, _ := iss.Issue(ctx, 5, "a", nil, time.Hour)
	_, _, _ = iss.Issue(ctx, 5, "b", nil, time.Hour)
	_, _, _ = iss.Issue(ctx, 6, "c", nil, time.Hour)

	// Revoke one; List must still include revoked records.
	if err := iss.Revoke(ctx, a.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	u5, err := iss.List(ctx, 5)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(u5) != 2 {
		t.Fatalf("List(5) returned %d, want 2 (incl. revoked)", len(u5))
	}
	var sawRevoked bool
	for _, it := range u5 {
		if it.UserID != 5 {
			t.Fatalf("List(5) returned foreign user %d", it.UserID)
		}
		if it.Revoked() {
			sawRevoked = true
		}
	}
	if !sawRevoked {
		t.Fatalf("List(5) dropped the revoked token")
	}
}

// --- helper / format tests ------------------------------------------------

func TestHashSecret(t *testing.T) {
	// Matches a standalone hex SHA-256 and is deterministic.
	sum := sha256.Sum256([]byte("hello"))
	want := hex.EncodeToString(sum[:])
	if got := hashSecret("hello"); got != want {
		t.Fatalf("hashSecret = %q, want %q", got, want)
	}
	if hashSecret("a") == hashSecret("b") {
		t.Fatalf("distinct inputs collided")
	}
	if len(hashSecret("x")) != 64 {
		t.Fatalf("hash length = %d, want 64 hex chars", len(hashSecret("x")))
	}
}

func TestSplitToken(t *testing.T) {
	tests := []struct {
		in         string
		wantID     string
		wantSecret string
		wantOK     bool
	}{
		{"id|secret", "id", "secret", true},
		{"a|b|c", "a", "b|c", true}, // only the first pipe splits
		{"", "", "", false},
		{"nopipe", "", "", false},
		{"|secret", "", "", false}, // empty id
		{"id|", "", "", false},     // empty secret
		{"|", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			id, secret, ok := splitToken(tc.in)
			if ok != tc.wantOK || id != tc.wantID || secret != tc.wantSecret {
				t.Fatalf("splitToken(%q) = (%q,%q,%v), want (%q,%q,%v)",
					tc.in, id, secret, ok, tc.wantID, tc.wantSecret, tc.wantOK)
			}
		})
	}
}

// --- concurrency ----------------------------------------------------------

func TestConcurrentIssueAndFind(t *testing.T) {
	ctx := context.Background()
	iss, _ := newIssuer(t)

	const n = 50
	plains := make([]string, n)
	for i := range plains {
		_, p, err := iss.Issue(ctx, uint64(i), "k", []string{"read"}, time.Hour)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		plains[i] = p
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := iss.Find(ctx, p); err != nil {
					t.Errorf("Find: %v", err)
					return
				}
			}
		}(plains[i])
	}
	// Concurrent issuance alongside lookups exercises the store lock.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(uid uint64) {
			defer wg.Done()
			if _, _, err := iss.Issue(ctx, uid, "x", nil, time.Hour); err != nil {
				t.Errorf("Issue: %v", err)
			}
		}(uint64(i))
	}
	wg.Wait()
}
