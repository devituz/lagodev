// Package seeders registers the blog's seeders at import time. The runner
// will pick them up via seeder.Default; dependencies are declared
// explicitly to give the runner enough information to topologically sort.
package seeders

import (
	"context"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/seeder"

	"github.com/devituz/lagodev/examples/blog/factories"
)

func init() {
	seeder.Register(&UserSeeder{})
	seeder.Register(&PostSeeder{})
	seeder.Register(&CommentSeeder{})
}

// UserSeeder creates a handful of authors.
type UserSeeder struct{}

func (UserSeeder) Name() string           { return "UserSeeder" }
func (UserSeeder) Dependencies() []string { return nil }
func (UserSeeder) Run(ctx context.Context, conn *database.Connection) error {
	_, err := factories.UserFactory(conn).Count(5).Create(ctx)
	return err
}

// PostSeeder gives each user 3 posts.
type PostSeeder struct{}

func (PostSeeder) Name() string           { return "PostSeeder" }
func (PostSeeder) Dependencies() []string { return []string{"UserSeeder"} }
func (PostSeeder) Run(ctx context.Context, conn *database.Connection) error {
	// We could query users.id here; this example pins author IDs to keep
	// the seeder deterministic.
	for uid := uint64(1); uid <= 5; uid++ {
		if _, err := factories.PostFactory(conn, uid).Count(3).Create(ctx); err != nil {
			return err
		}
	}
	return nil
}

// CommentSeeder drops two comments on each post.
type CommentSeeder struct{}

func (CommentSeeder) Name() string           { return "CommentSeeder" }
func (CommentSeeder) Dependencies() []string { return []string{"PostSeeder"} }
func (CommentSeeder) Run(ctx context.Context, conn *database.Connection) error {
	for pid := uint64(1); pid <= 15; pid++ {
		if _, err := factories.CommentFactory(conn, pid, (pid%5)+1).Count(2).Create(ctx); err != nil {
			return err
		}
	}
	return nil
}
