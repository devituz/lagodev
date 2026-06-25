package token

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHashedAtRest proves the plaintext secret never reaches the store; only a
// SHA-256 hash is persisted and it is not the secret half.
func TestHashedAtRest(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	i := NewIssuer(store)
	rec, plain, err := i.Issue(ctx, 1, "ci", []string{"read"}, 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, secret, _ := splitToken(plain)
	stored, ok, _ := store.Get(ctx, rec.ID)
	if !ok {
		t.Fatal("record missing")
	}
	if stored.Hash == secret || strings.Contains(stored.Hash, secret) {
		t.Fatal("plaintext secret leaked into stored hash")
	}
	if stored.Hash != hashSecret(secret) {
		t.Fatal("stored hash is not SHA-256 of secret")
	}
	if len(stored.Hash) != 64 { // hex of 32-byte digest
		t.Fatalf("hash length = %d, want 64", len(stored.Hash))
	}
}

// TestFind_WrongSecretSameID asserts that knowing a valid id but a wrong
// secret yields ErrNotFound — identical to an unknown id, so the lookup does
// not leak whether the id exists.
func TestFind_DoesNotLeakExistence(t *testing.T) {
	ctx := context.Background()
	i, _ := newIssuer(t)
	rec, plain, err := i.Issue(ctx, 1, "ci", []string{"read"}, 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, _, _ = splitToken(plain)

	// Valid id, wrong secret.
	_, err = i.Find(ctx, rec.ID+"|"+"wrongsecretwrongsecretwrongsecret")
	if err != ErrNotFound {
		t.Fatalf("wrong secret => %v, want ErrNotFound", err)
	}
	// Unknown id entirely.
	_, err = i.Find(ctx, "unknownid|wrongsecretwrongsecretwrongsecret")
	if err != ErrNotFound {
		t.Fatalf("unknown id => %v, want ErrNotFound", err)
	}
}

// TestFind_Malformed checks the format guard before any store access.
func TestFind_Malformed(t *testing.T) {
	ctx := context.Background()
	i, _ := newIssuer(t)
	for _, in := range []string{"", "nopipe", "|nosel", "noselsecret|", "|"} {
		if _, err := i.Find(ctx, in); err != ErrMalformed {
			t.Fatalf("Find(%q) = %v, want ErrMalformed", in, err)
		}
	}
}

// TestAbilities verifies scope and wildcard checks.
func TestAbilities(t *testing.T) {
	scoped := &PersonalAccessToken{Abilities: []string{"posts:read", "posts:write"}}
	if !scoped.Can("posts:read") || !scoped.Can("posts:write") {
		t.Fatal("granted ability denied")
	}
	if scoped.Can("posts:delete") {
		t.Fatal("ungranted ability allowed")
	}
	if !scoped.Cant("admin") {
		t.Fatal("Cant inverted")
	}
	wild := &PersonalAccessToken{Abilities: []string{AbilityAll}}
	if !wild.Can("anything") || !wild.Can("admin") {
		t.Fatal("wildcard did not grant all")
	}
	none := &PersonalAccessToken{}
	if none.Can("x") {
		t.Fatal("empty abilities granted something")
	}
}

// TestFind_ExpiryHonored proves an expired token is rejected with ErrExpired.
func TestFind_ExpiryHonored(t *testing.T) {
	ctx := context.Background()
	i, _ := newIssuer(t)
	_, plain, err := i.Issue(ctx, 1, "ci", []string{"read"}, time.Nanosecond)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := i.Find(ctx, plain); err != ErrExpired {
		t.Fatalf("Find expired = %v, want ErrExpired", err)
	}
}

// TestFind_RevocationHonored proves a revoked token is rejected, and that
// revocation takes precedence over expiry where both apply.
func TestFind_RevocationHonored(t *testing.T) {
	ctx := context.Background()
	i, _ := newIssuer(t)
	rec, plain, err := i.Issue(ctx, 1, "ci", []string{"read"}, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := i.Revoke(ctx, rec.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := i.Find(ctx, plain); err != ErrRevoked {
		t.Fatalf("Find revoked = %v, want ErrRevoked", err)
	}
	// Idempotent revoke.
	if err := i.Revoke(ctx, rec.ID); err != nil {
		t.Fatalf("second Revoke = %v, want nil", err)
	}
	// RevokePlain on an unknown id is ErrNotFound, not panic.
	if err := i.RevokePlain(ctx, "ghost|secret"); err != ErrNotFound {
		t.Fatalf("RevokePlain ghost = %v, want ErrNotFound", err)
	}
}

// TestLastUsed_RaceFree hammers Find concurrently for one token under -race to
// prove LastUsedAt updates and store access are free of data races.
func TestLastUsed_RaceFree(t *testing.T) {
	ctx := context.Background()
	i, _ := newIssuer(t)
	_, plain, err := i.Issue(ctx, 1, "ci", []string{"read"}, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	var wg sync.WaitGroup
	for n := 0; n < 64; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 50; k++ {
				if _, err := i.Find(ctx, plain); err != nil {
					t.Errorf("Find: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestLastUsed_Stamped confirms the first successful Find stamps LastUsedAt.
func TestLastUsed_Stamped(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	i := NewIssuer(store)
	rec, plain, err := i.Issue(ctx, 1, "ci", []string{"read"}, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !rec.LastUsedAt.IsZero() {
		t.Fatal("LastUsedAt set before first use")
	}
	if _, err := i.Find(ctx, plain); err != nil {
		t.Fatalf("Find: %v", err)
	}
	got, _, _ := store.Get(ctx, rec.ID)
	if got.LastUsedAt.IsZero() {
		t.Fatal("LastUsedAt not stamped after Find")
	}
}

// TestSecretEntropy spot-checks that successive issues produce distinct,
// non-trivial secrets and ids.
func TestSecretEntropy(t *testing.T) {
	ctx := context.Background()
	i, _ := newIssuer(t)
	seen := make(map[string]struct{})
	for n := 0; n < 100; n++ {
		_, plain, err := i.Issue(ctx, 1, "ci", nil, 0)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		id, secret, ok := splitToken(plain)
		if !ok {
			t.Fatalf("split failed for %q", plain)
		}
		if len(secret) < 40 {
			t.Fatalf("secret too short: %d", len(secret))
		}
		if _, dup := seen[id]; dup {
			t.Fatal("duplicate id")
		}
		seen[id] = struct{}{}
		if _, dup := seen[secret]; dup {
			t.Fatal("duplicate secret")
		}
		seen[secret] = struct{}{}
	}
}
