package token_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/devituz/lagodev/auth/token"
)

// ExampleIssuer shows the personal-access-token lifecycle: issue a token with a
// scoped ability, find it back from the plaintext, check the ability, then
// revoke it and show the revoked token is rejected.
func ExampleIssuer() {
	ctx := context.Background()
	issuer := token.NewIssuer(token.NewMemoryStore())

	// Issue a never-expiring token for user 7, scoped to "posts:read".
	rec, plain, err := issuer.Issue(ctx, 7, "CI deploy key", []string{"posts:read"}, 0)
	if err != nil {
		panic(err)
	}
	fmt.Println("name:", rec.Name)

	// A client presents the plaintext; resolve it back to its record.
	found, err := issuer.Find(ctx, plain)
	if err != nil {
		panic(err)
	}
	fmt.Println("found user:", found.UserID)
	fmt.Println("can posts:read:", found.Can("posts:read"))
	fmt.Println("can posts:write:", found.Can("posts:write"))

	// Revoke it, then show the same plaintext is now rejected.
	if err := issuer.Revoke(ctx, rec.ID); err != nil {
		panic(err)
	}
	_, err = issuer.Find(ctx, plain)
	fmt.Println("revoked rejected:", errors.Is(err, token.ErrRevoked))

	// Output:
	// name: CI deploy key
	// found user: 7
	// can posts:read: true
	// can posts:write: false
	// revoked rejected: true
}
