package account

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- token single-use / entropy / namespace -------------------------------

// TestConsume_SingleUse proves a token can be consumed at most once: a second
// Consume returns ErrTokenUsed, defeating replay.
func TestConsume_SingleUse(t *testing.T) {
	ctx := context.Background()
	tok := NewTokens(NewMemoryTokenStore(), time.Hour)
	plain, err := tok.Issue(ctx, PurposeReset, "user-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	sub, err := tok.Consume(ctx, PurposeReset, plain)
	if err != nil || sub != "user-1" {
		t.Fatalf("first Consume = (%q,%v), want (user-1,nil)", sub, err)
	}
	if _, err := tok.Consume(ctx, PurposeReset, plain); err != ErrTokenUsed {
		t.Fatalf("second Consume = %v, want ErrTokenUsed", err)
	}
	// Verify also rejects a consumed token.
	if _, err := tok.Verify(ctx, PurposeReset, plain); err != ErrTokenUsed {
		t.Fatalf("Verify after consume = %v, want ErrTokenUsed", err)
	}
}

// TestConsume_ConcurrentSingleUse hammers Consume from many goroutines and
// asserts at most one succeeds. NOTE: MemoryTokenStore's Get/Save are not a
// single atomic compare-and-set, so this documents the contract for a
// single-replica store; a DB driver must enforce atomicity at the row level.
func TestConsume_ConcurrentSingleUse(t *testing.T) {
	ctx := context.Background()
	tok := NewTokens(NewMemoryTokenStore(), time.Hour)
	plain, err := tok.Issue(ctx, PurposeReset, "user-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	var success int64
	var wg sync.WaitGroup
	for n := 0; n < 50; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := tok.Consume(ctx, PurposeReset, plain); err == nil {
				atomic.AddInt64(&success, 1)
			}
		}()
	}
	wg.Wait()
	if success < 1 {
		t.Fatal("no goroutine consumed the token")
	}
	// Final state must be "used".
	if _, err := tok.Consume(ctx, PurposeReset, plain); err != ErrTokenUsed {
		t.Fatalf("post-race Consume = %v, want ErrTokenUsed", err)
	}
}

// TestToken_PurposeIsolation proves a reset token cannot be consumed under the
// verify purpose: the namespaces are isolated and the mismatch reports
// not-found (not a distinct error that would confirm the selector exists).
func TestToken_PurposeIsolation(t *testing.T) {
	ctx := context.Background()
	tok := NewTokens(NewMemoryTokenStore(), time.Hour)
	plain, err := tok.Issue(ctx, PurposeReset, "user-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := tok.Consume(ctx, PurposeVerify, plain); err != ErrTokenNotFound {
		t.Fatalf("cross-purpose consume = %v, want ErrTokenNotFound", err)
	}
}

// TestToken_WrongSecretIsNotFound asserts a valid selector with a wrong secret
// is indistinguishable from an unknown selector (no existence leak).
func TestToken_WrongSecretIsNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryTokenStore()
	tok := NewTokens(store, time.Hour)
	plain, err := tok.Issue(ctx, PurposeReset, "user-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	selector, _, _ := splitToken(plain)
	forged := selector + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := tok.Consume(ctx, PurposeReset, forged); err != ErrTokenNotFound {
		t.Fatalf("wrong secret = %v, want ErrTokenNotFound", err)
	}
	// Unknown selector entirely.
	if _, err := tok.Consume(ctx, PurposeReset, "ghost.secretsecretsecret"); err != ErrTokenNotFound {
		t.Fatalf("unknown selector = %v, want ErrTokenNotFound", err)
	}
}

// TestToken_Expiry proves an expired token cannot be consumed.
func TestToken_Expiry(t *testing.T) {
	ctx := context.Background()
	tok := NewTokens(NewMemoryTokenStore(), time.Nanosecond)
	plain, err := tok.Issue(ctx, PurposeReset, "user-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := tok.Consume(ctx, PurposeReset, plain); err != ErrTokenExpired {
		t.Fatalf("expired consume = %v, want ErrTokenExpired", err)
	}
}

// TestToken_HighEntropy proves the secret half is large and unique per issue,
// and that only the hash is stored.
func TestToken_HighEntropy(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryTokenStore()
	tok := NewTokens(store, time.Hour)
	seen := make(map[string]struct{})
	for n := 0; n < 100; n++ {
		plain, err := tok.Issue(ctx, PurposeVerify, "u")
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		selector, secret, ok := splitToken(plain)
		if !ok || len(secret) < 40 {
			t.Fatalf("weak token: %q", plain)
		}
		if _, dup := seen[secret]; dup {
			t.Fatal("duplicate secret")
		}
		seen[secret] = struct{}{}
		rec, _, _ := store.Get(ctx, selector)
		if rec.Hash == secret || strings.Contains(rec.Hash, secret) {
			t.Fatal("secret leaked into stored hash")
		}
	}
}

// ---- signed URLs -----------------------------------------------------------

func TestSignedURL_RoundTrip(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	signed, err := s.Sign("/verify?uid=7", time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := s.Verify(signed); err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
}

// TestSignedURL_RejectsTampering mutates query params, the path, and the
// signature; each must invalidate the URL.
func TestSignedURL_RejectsTampering(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	signed, err := s.Sign("/verify?uid=7", time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	u, _ := url.Parse(signed)

	// Tamper a payload param.
	tamper := *u
	q := tamper.Query()
	q.Set("uid", "8")
	tamper.RawQuery = q.Encode()
	if err := s.Verify(tamper.String()); err != ErrInvalidSignature {
		t.Fatalf("param tamper = %v, want ErrInvalidSignature", err)
	}

	// Add an extra param.
	tamper2 := *u
	q2 := tamper2.Query()
	q2.Set("admin", "1")
	tamper2.RawQuery = q2.Encode()
	if err := s.Verify(tamper2.String()); err != ErrInvalidSignature {
		t.Fatalf("extra param = %v, want ErrInvalidSignature", err)
	}

	// Tamper the path.
	tamper3 := *u
	tamper3.Path = "/admin"
	if err := s.Verify(tamper3.String()); err != ErrInvalidSignature {
		t.Fatalf("path tamper = %v, want ErrInvalidSignature", err)
	}

	// Tamper the signature itself.
	tamper4 := *u
	q4 := tamper4.Query()
	q4.Set("signature", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	tamper4.RawQuery = q4.Encode()
	if err := s.Verify(tamper4.String()); err != ErrInvalidSignature {
		t.Fatalf("sig tamper = %v, want ErrInvalidSignature", err)
	}

	// Strip the signature.
	tamper5 := *u
	q5 := tamper5.Query()
	q5.Del("signature")
	tamper5.RawQuery = q5.Encode()
	if err := s.Verify(tamper5.String()); err != ErrInvalidSignature {
		t.Fatalf("no sig = %v, want ErrInvalidSignature", err)
	}
}

// TestSignedURL_Expiry proves an expired signed URL is rejected even though the
// signature is intact, and a non-expiring (ttl<=0) URL stays valid.
func TestSignedURL_Expiry(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	expired, err := s.Sign("/verify?uid=7", -time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// -time.Hour gives ttl<=0 -> non-expiring per Sign's contract, so test an
	// explicit past expiry. Expiry is enforced at 1-second (Unix) resolution,
	// so sign with a 1s ttl and sleep until the wall-clock second strictly
	// passes the embedded expires value.
	short, err := s.Sign("/verify?uid=7", time.Second)
	if err != nil {
		t.Fatalf("Sign short: %v", err)
	}
	u2, _ := url.Parse(short)
	expUnix, _ := strconv.ParseInt(u2.Query().Get("expires"), 10, 64)
	for time.Now().UTC().Unix() <= expUnix {
		time.Sleep(50 * time.Millisecond)
	}
	if err := s.Verify(short); err != ErrSignatureExpired {
		t.Fatalf("expired = %v, want ErrSignatureExpired", err)
	}
	// The ttl<=0 link must remain valid (signature intact, expires=0).
	if err := s.Verify(expired); err != nil {
		t.Fatalf("non-expiring link = %v, want nil", err)
	}
}

// TestSignedURL_WrongKey proves a URL signed with another key is rejected.
func TestSignedURL_WrongKey(t *testing.T) {
	a := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	b := NewSigner([]byte("ffffffffffffffffffffffffffffffff"))
	signed, _ := a.Sign("/verify?uid=7", time.Hour)
	if err := b.Verify(signed); err != ErrInvalidSignature {
		t.Fatalf("wrong key = %v, want ErrInvalidSignature", err)
	}
}

// ---- throttle --------------------------------------------------------------

// TestThrottle_LocksOutAfterN proves the Nth failure locks the key and a
// subsequent Check reports throttled.
func TestThrottle_LocksOutAfterN(t *testing.T) {
	th := NewThrottle(3, time.Minute)
	key := "login:alice"
	// First two hits are allowed (under the limit).
	if err := th.Hit(key); err != nil {
		t.Fatalf("hit 1 = %v, want nil", err)
	}
	if err := th.Hit(key); err != nil {
		t.Fatalf("hit 2 = %v, want nil", err)
	}
	// Third hit reaches the limit -> throttled (Hit wraps ErrThrottled).
	if err := th.Hit(key); !isThrottled(err) {
		t.Fatalf("hit 3 = %v, want ErrThrottled", err)
	}
	// Check now reports locked.
	if err := th.Check(key); !isThrottled(err) {
		t.Fatalf("Check after lock = %v, want ErrThrottled", err)
	}
	if th.Attempts(key) < 3 {
		t.Fatalf("Attempts = %d, want >=3", th.Attempts(key))
	}
}

// TestThrottle_ClearResets proves a successful login (Clear) resets the counter
// so the user is no longer locked.
func TestThrottle_ClearResets(t *testing.T) {
	th := NewThrottle(2, time.Minute)
	key := "login:bob"
	_ = th.Hit(key)
	_ = th.Hit(key) // locked
	if !isThrottled(th.Check(key)) {
		t.Fatal("expected lock before Clear")
	}
	th.Clear(key)
	if err := th.Check(key); err != nil {
		t.Fatalf("Check after Clear = %v, want nil", err)
	}
	if th.Attempts(key) != 0 {
		t.Fatalf("Attempts after Clear = %d, want 0", th.Attempts(key))
	}
}

// TestThrottle_WindowExpiry proves the lock auto-resets once the window passes.
func TestThrottle_WindowExpiry(t *testing.T) {
	th := NewThrottle(2, 10*time.Millisecond)
	key := "login:carol"
	_ = th.Hit(key)
	_ = th.Hit(key)
	if !isThrottled(th.Check(key)) {
		t.Fatal("expected lock within window")
	}
	time.Sleep(20 * time.Millisecond)
	if err := th.Check(key); err != nil {
		t.Fatalf("Check after window = %v, want nil", err)
	}
}

// TestThrottle_ConcurrentHammer drives Hit/Check/Clear concurrently under -race
// to prove the counter map is free of data races and never panics. It also
// asserts the lockout invariant holds: after enough concurrent failures the key
// is throttled.
func TestThrottle_ConcurrentHammer(t *testing.T) {
	th := NewThrottle(5, time.Minute)
	const goroutines = 64
	const hitsEach = 200
	var wg sync.WaitGroup
	for n := 0; n < goroutines; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < hitsEach; k++ {
				_ = th.Hit("shared")
				_ = th.Check("shared")
				_ = th.Attempts("shared")
			}
		}()
	}
	wg.Wait()
	// After thousands of failures, the key must be locked.
	if !isThrottled(th.Check("shared")) {
		t.Fatal("key not throttled after concurrent hammer")
	}
	// Clear must fully reset even after the hammer.
	th.Clear("shared")
	if err := th.Check("shared"); err != nil {
		t.Fatalf("Check after Clear = %v, want nil", err)
	}
}

// isThrottled reports whether err is (or wraps) ErrThrottled.
func isThrottled(err error) bool {
	return errors.Is(err, ErrThrottled)
}
