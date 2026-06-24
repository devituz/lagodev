package account

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---- Tokens ----------------------------------------------------------------

func TestTokensIssueVerifyConsume(t *testing.T) {
	ctx := context.Background()
	tk := NewTokens(NewMemoryTokenStore(), time.Hour)

	plain, err := tk.Issue(ctx, PurposeReset, "user-42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.Contains(plain, ".") {
		t.Fatalf("token plaintext missing selector.secret separator: %q", plain)
	}

	// Verify does not consume: it may be called repeatedly.
	for i := 0; i < 3; i++ {
		sub, err := tk.Verify(ctx, PurposeReset, plain)
		if err != nil {
			t.Fatalf("Verify call %d: %v", i, err)
		}
		if sub != "user-42" {
			t.Fatalf("Verify subject = %q, want user-42", sub)
		}
	}

	// Consume succeeds once and returns the subject.
	sub, err := tk.Consume(ctx, PurposeReset, plain)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if sub != "user-42" {
		t.Fatalf("Consume subject = %q, want user-42", sub)
	}
}

func TestTokensConsumeSingleUse(t *testing.T) {
	ctx := context.Background()
	tk := NewTokens(NewMemoryTokenStore(), time.Hour)

	plain, err := tk.Issue(ctx, PurposeVerify, "user-7")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := tk.Consume(ctx, PurposeVerify, plain); err != nil {
		t.Fatalf("first Consume: %v", err)
	}
	if _, err := tk.Consume(ctx, PurposeVerify, plain); !errors.Is(err, ErrTokenUsed) {
		t.Fatalf("second Consume err = %v, want ErrTokenUsed", err)
	}
	// Verify after consumption must also report used.
	if _, err := tk.Verify(ctx, PurposeVerify, plain); !errors.Is(err, ErrTokenUsed) {
		t.Fatalf("Verify after consume err = %v, want ErrTokenUsed", err)
	}
}

func TestTokensLookupRejections(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryTokenStore()
	tk := NewTokens(store, time.Hour)

	good, err := tk.Issue(ctx, PurposeReset, "user-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	selector := good[:strings.IndexByte(good, '.')]
	secret := good[strings.IndexByte(good, '.')+1:]

	cases := []struct {
		name    string
		purpose Purpose
		plain   string
		wantErr error
	}{
		{"wrong purpose", PurposeVerify, good, ErrTokenNotFound},
		{"garbage no dot", PurposeReset, "garbagewithoutadot", ErrTokenNotFound},
		{"empty selector", PurposeReset, ".secret", ErrTokenNotFound},
		{"empty secret", PurposeReset, selector + ".", ErrTokenNotFound},
		{"unknown selector", PurposeReset, "nope." + secret, ErrTokenNotFound},
		{"tampered secret", PurposeReset, selector + ".tamperedsecretvalue", ErrTokenNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := tk.Verify(ctx, c.purpose, c.plain); !errors.Is(err, c.wantErr) {
				t.Fatalf("Verify err = %v, want %v", err, c.wantErr)
			}
			if _, err := tk.Consume(ctx, c.purpose, c.plain); !errors.Is(err, c.wantErr) {
				t.Fatalf("Consume err = %v, want %v", err, c.wantErr)
			}
		})
	}
}

// expiredStore wraps MemoryTokenStore but forces every returned record to be
// already expired, exercising the ErrTokenExpired branch deterministically.
type expiredStore struct{ inner *MemoryTokenStore }

func (s expiredStore) Save(ctx context.Context, r *Record) error {
	return s.inner.Save(ctx, r)
}

func (s expiredStore) Get(ctx context.Context, selector string) (*Record, bool, error) {
	r, ok, err := s.inner.Get(ctx, selector)
	if r != nil {
		r.ExpiresAt = time.Now().Add(-time.Minute)
	}
	return r, ok, err
}

func (s expiredStore) Delete(ctx context.Context, selector string) error {
	return s.inner.Delete(ctx, selector)
}

func TestTokensExpired(t *testing.T) {
	ctx := context.Background()
	store := expiredStore{inner: NewMemoryTokenStore()}
	tk := NewTokens(store, time.Hour)

	plain, err := tk.Issue(ctx, PurposeReset, "user-9")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := tk.Verify(ctx, PurposeReset, plain); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("Verify err = %v, want ErrTokenExpired", err)
	}
	if _, err := tk.Consume(ctx, PurposeReset, plain); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("Consume err = %v, want ErrTokenExpired", err)
	}
}

func TestTokensExpiredTinyTTL(t *testing.T) {
	ctx := context.Background()
	tk := NewTokens(NewMemoryTokenStore(), time.Nanosecond)

	plain, err := tk.Issue(ctx, PurposeReset, "user-x")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := tk.Verify(ctx, PurposeReset, plain); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("Verify err = %v, want ErrTokenExpired", err)
	}
}

func TestNewTokensDefaultTTL(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryTokenStore()
	tk := NewTokens(store, 0) // ttl<=0 -> 1h default

	plain, err := tk.Issue(ctx, PurposeReset, "user-d")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	selector := plain[:strings.IndexByte(plain, '.')]
	rec, ok, err := store.Get(ctx, selector)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got := rec.ExpiresAt.Sub(rec.CreatedAt); got < 59*time.Minute || got > 61*time.Minute {
		t.Fatalf("default ttl = %v, want ~1h", got)
	}
}

// ---- Signer ----------------------------------------------------------------

func TestSignerSignVerify(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))

	signed, err := s.Sign("https://app.test/reset?user=42&lang=uz", time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := s.Verify(signed); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestSignerKeyCopyIsolation(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	s := NewSigner(key)
	signed, err := s.Sign("https://app.test/x", time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// Mutating the caller's key slice must not affect the signer.
	for i := range key {
		key[i] = 0
	}
	if err := s.Verify(signed); err != nil {
		t.Fatalf("Verify after key mutation: %v", err)
	}
}

func TestSignerTampering(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	signed, err := s.Sign("https://app.test/reset?user=42", time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(u *url.URL)
		wantErr error
	}{
		{
			name:    "tamper path",
			mutate:  func(u *url.URL) { u.Path = "/admin" },
			wantErr: ErrInvalidSignature,
		},
		{
			name: "tamper query param",
			mutate: func(u *url.URL) {
				q := u.Query()
				q.Set("user", "1")
				u.RawQuery = q.Encode()
			},
			wantErr: ErrInvalidSignature,
		},
		{
			name: "add extra param",
			mutate: func(u *url.URL) {
				q := u.Query()
				q.Set("admin", "true")
				u.RawQuery = q.Encode()
			},
			wantErr: ErrInvalidSignature,
		},
		{
			name: "tamper signature",
			mutate: func(u *url.URL) {
				q := u.Query()
				q.Set("signature", "deadbeef")
				u.RawQuery = q.Encode()
			},
			wantErr: ErrInvalidSignature,
		},
		{
			name: "drop signature",
			mutate: func(u *url.URL) {
				q := u.Query()
				q.Del("signature")
				u.RawQuery = q.Encode()
			},
			wantErr: ErrInvalidSignature,
		},
		{
			name: "non-numeric expires",
			mutate: func(u *url.URL) {
				q := u.Query()
				q.Set("expires", "soon")
				u.RawQuery = q.Encode()
			},
			wantErr: ErrInvalidSignature,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, err := url.Parse(signed)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			c.mutate(u)
			if err := s.Verify(u.String()); !errors.Is(err, c.wantErr) {
				t.Fatalf("Verify err = %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestSignerExpired(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))

	// Sign uses Unix-second expiry granularity, so a tiny TTL stays within the
	// current second and would not expire reliably. Build a properly-signed URL
	// with a past expiry directly via the in-package mac so the signature is
	// valid but the embedded expiry has passed.
	u, err := url.Parse("https://app.test/reset?user=42")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	q.Set("expires", strconv.FormatInt(time.Now().Add(-time.Hour).UTC().Unix(), 10))
	u.RawQuery = q.Encode()
	q.Set("signature", s.mac(u.Path, u.RawQuery))
	u.RawQuery = q.Encode()

	if err := s.Verify(u.String()); !errors.Is(err, ErrSignatureExpired) {
		t.Fatalf("Verify err = %v, want ErrSignatureExpired", err)
	}
}

func TestSignerNonExpiring(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	for _, ttl := range []time.Duration{0, -time.Hour} {
		signed, err := s.Sign("https://app.test/perma", ttl)
		if err != nil {
			t.Fatalf("Sign ttl=%v: %v", ttl, err)
		}
		u, err := url.Parse(signed)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got := u.Query().Get("expires"); got != "0" {
			t.Fatalf("expires = %q, want 0 for non-expiring link", got)
		}
		if err := s.Verify(signed); err != nil {
			t.Fatalf("Verify non-expiring (ttl=%v): %v", ttl, err)
		}
	}
}

func TestSignerQueryReorderingVerifies(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	signed, err := s.Sign("https://app.test/reset?b=2&a=1&c=3", time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	u, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Rebuild RawQuery in a deliberately scrambled, non-canonical order while
	// preserving every key/value. Canonical encoding on Verify must still match.
	q := u.Query()
	parts := []string{
		"signature=" + url.QueryEscape(q.Get("signature")),
		"c=" + url.QueryEscape(q.Get("c")),
		"expires=" + url.QueryEscape(q.Get("expires")),
		"a=" + url.QueryEscape(q.Get("a")),
		"b=" + url.QueryEscape(q.Get("b")),
	}
	u.RawQuery = strings.Join(parts, "&")

	if err := s.Verify(u.String()); err != nil {
		t.Fatalf("Verify reordered query: %v", err)
	}
}

func TestSignerStripsCallerSignature(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	// A caller-supplied signature in base must not poison the payload.
	signed, err := s.Sign("https://app.test/reset?user=42&signature=forged", time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := s.Verify(signed); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// ---- Throttle --------------------------------------------------------------

func TestThrottleHitAndLockout(t *testing.T) {
	th := NewThrottle(3, time.Minute)
	const key = "login:user@test"

	// Under-limit hits return nil.
	if err := th.Hit(key); err != nil {
		t.Fatalf("hit 1: %v", err)
	}
	if got := th.Attempts(key); got != 1 {
		t.Fatalf("Attempts after 1 hit = %d, want 1", got)
	}
	if err := th.Hit(key); err != nil {
		t.Fatalf("hit 2: %v", err)
	}
	if got := th.Attempts(key); got != 2 {
		t.Fatalf("Attempts after 2 hits = %d, want 2", got)
	}
	// Check does not record and is still clear.
	if err := th.Check(key); err != nil {
		t.Fatalf("Check before lockout: %v", err)
	}
	if got := th.Attempts(key); got != 2 {
		t.Fatalf("Attempts after Check = %d, want 2 (Check must not record)", got)
	}

	// Third hit crosses MaxAttempts -> ErrThrottled.
	if err := th.Hit(key); !errors.Is(err, ErrThrottled) {
		t.Fatalf("hit 3 err = %v, want ErrThrottled", err)
	}
	// Check now reflects the lockout, without recording.
	if err := th.Check(key); !errors.Is(err, ErrThrottled) {
		t.Fatalf("Check after lockout = %v, want ErrThrottled", err)
	}
	if got := th.Attempts(key); got != 3 {
		t.Fatalf("Attempts after lockout = %d, want 3", got)
	}
	// Further hits keep returning ErrThrottled.
	if err := th.Hit(key); !errors.Is(err, ErrThrottled) {
		t.Fatalf("hit 4 err = %v, want ErrThrottled", err)
	}
}

func TestThrottleClear(t *testing.T) {
	th := NewThrottle(2, time.Minute)
	const key = "login:clear"

	_ = th.Hit(key)
	if err := th.Hit(key); !errors.Is(err, ErrThrottled) {
		t.Fatalf("hit 2 err = %v, want ErrThrottled", err)
	}
	th.Clear(key)
	if got := th.Attempts(key); got != 0 {
		t.Fatalf("Attempts after Clear = %d, want 0", got)
	}
	if err := th.Check(key); err != nil {
		t.Fatalf("Check after Clear: %v", err)
	}
	// Counter starts fresh.
	if err := th.Hit(key); err != nil {
		t.Fatalf("hit after Clear: %v", err)
	}
}

func TestThrottleWindowExpiry(t *testing.T) {
	th := NewThrottle(2, 20*time.Millisecond)
	const key = "login:window"

	_ = th.Hit(key)
	if err := th.Hit(key); !errors.Is(err, ErrThrottled) {
		t.Fatalf("hit 2 err = %v, want ErrThrottled", err)
	}
	// Wait for the window to elapse; counter resets.
	time.Sleep(40 * time.Millisecond)
	if got := th.Attempts(key); got != 0 {
		t.Fatalf("Attempts after window expiry = %d, want 0", got)
	}
	if err := th.Check(key); err != nil {
		t.Fatalf("Check after window expiry: %v", err)
	}
	// A new hit starts a fresh window and is under-limit again.
	if err := th.Hit(key); err != nil {
		t.Fatalf("hit after window expiry: %v", err)
	}
	if got := th.Attempts(key); got != 1 {
		t.Fatalf("Attempts after fresh hit = %d, want 1", got)
	}
}

func TestThrottleEmptyKey(t *testing.T) {
	th := NewThrottle(5, time.Minute)
	if got := th.Attempts("unknown"); got != 0 {
		t.Fatalf("Attempts unknown = %d, want 0", got)
	}
	if err := th.Check("unknown"); err != nil {
		t.Fatalf("Check unknown: %v", err)
	}
}

func TestNewThrottleDefaults(t *testing.T) {
	th := NewThrottle(0, 0) // -> 5 attempts, 1 minute
	const key = "login:defaults"
	for i := 0; i < 4; i++ {
		if err := th.Hit(key); err != nil {
			t.Fatalf("hit %d (under default limit): %v", i+1, err)
		}
	}
	if err := th.Hit(key); !errors.Is(err, ErrThrottled) {
		t.Fatalf("hit 5 err = %v, want ErrThrottled (default max 5)", err)
	}
}
