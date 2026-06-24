package account_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devituz/lagodev/auth/account"
)

// ExampleTokens_Consume issues a password-reset token, consumes it once
// (success), then shows a second Consume of the same token is rejected as
// already-used — the single-use property that defeats link replay.
func ExampleTokens_Consume() {
	ctx := context.Background()
	tokens := account.NewTokens(account.NewMemoryTokenStore(), time.Hour)

	// Mint a reset token for a user; the plaintext goes into the email link.
	plain, err := tokens.Issue(ctx, account.PurposeReset, "user-42")
	if err != nil {
		panic(err)
	}

	// User clicks the link: consume the token to perform the reset.
	subject, err := tokens.Consume(ctx, account.PurposeReset, plain)
	fmt.Println("first consume:", subject, err == nil)

	// Replaying the same link must fail.
	_, err = tokens.Consume(ctx, account.PurposeReset, plain)
	fmt.Println("second consume used:", errors.Is(err, account.ErrTokenUsed))

	// Output:
	// first consume: user-42 true
	// second consume used: true
}

// ExampleSigner shows a tamper-proof signed URL: Sign produces a link with an
// embedded signature, Verify accepts the untouched link and rejects a tampered
// one.
func ExampleSigner() {
	signer := account.NewSigner([]byte("0123456789abcdef0123456789abcdef"))

	signed, err := signer.Sign("https://app.test/invite?team=acme", time.Hour)
	if err != nil {
		panic(err)
	}

	fmt.Println("valid:", signer.Verify(signed) == nil)
	fmt.Println("tampered invalid:", errors.Is(signer.Verify(signed+"x"), account.ErrInvalidSignature))

	// Output:
	// valid: true
	// tampered invalid: true
}

// ExampleThrottle shows the login throttler locking an identifier out after the
// configured number of failed attempts.
func ExampleThrottle() {
	th := account.NewThrottle(3, time.Minute)
	key := "login:alice@example.com"

	// Record three failed logins; the third crosses the limit.
	for i := 1; i <= 3; i++ {
		err := th.Hit(key)
		fmt.Printf("attempt %d locked: %v\n", i, errors.Is(err, account.ErrThrottled))
	}

	// Further checks report the lock without recording a new attempt.
	fmt.Println("check locked:", errors.Is(th.Check(key), account.ErrThrottled))

	// A successful login clears the counter.
	th.Clear(key)
	fmt.Println("after clear:", th.Check(key) == nil)

	// Output:
	// attempt 1 locked: false
	// attempt 2 locked: false
	// attempt 3 locked: true
	// check locked: true
	// after clear: true
}
