// Example: end-to-end usage of lagodev with an in-memory SQLite database.
// Run it with `go run ./examples/basic`.
//
// The example is the shortest possible tour:
//   1. open a connection;
//   2. apply a migration via the migrator;
//   3. seed rows through the factory;
//   4. query the ORM with eager-loaded relations;
//   5. perform a soft delete.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/factory"
	"github.com/devituz/lagodev/logger"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/relations"
	"github.com/devituz/lagodev/schema"
)

// --- Models --------------------------------------------------------------

type User struct {
	orm.Model
	Name  string
	Email string
}

type Post struct {
	orm.Model
	UserID uint64 `column:"user_id"`
	Title  string
	Body   string
}

// --- Migrations ----------------------------------------------------------

func init() {
	migrations.Register(migrations.Define("20260101000001_create_users_table",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("users", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.String("email").Unique()
				t.Timestamps()
				t.SoftDeletes()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("users"))
		},
	))
	migrations.Register(migrations.Define("20260101000002_create_posts_table",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("posts", func(t *schema.Blueprint) {
				t.ID()
				t.ForeignId("user_id").Constrained("users").OnDelete("cascade")
				t.String("title")
				t.Text("body")
				t.Timestamps()
				t.SoftDeletes()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("posts"))
		},
	))
}

// --- App entry point -----------------------------------------------------

func main() {
	ctx := context.Background()

	log := logger.New(logger.LevelInfo)
	mgr := database.NewManager()
	mgr.SetLogger(log)
	conn, err := mgr.Open("default", database.Config{
		Driver: "sqlite",
		DSN:    "file::memory:?cache=shared",
	})
	must(err)

	migrator := migrations.New(conn, nil, migrations.Options{Logger: log})
	if _, err := migrator.Up(ctx); err != nil {
		panic(err)
	}

	// Build a UserFactory inline.
	userFactory := factory.New(conn, func(f *factory.Faker) User {
		return User{Name: f.Name(), Email: f.Email()}
	})

	users, err := userFactory.Count(3).Create(ctx)
	must(err)

	for i := range users {
		for j := 0; j < 2; j++ {
			must(orm.Save(ctx, conn, &Post{
				UserID: users[i].ID,
				Title:  fmt.Sprintf("Post %d", j+1),
				Body:   "Lorem ipsum",
			}))
		}
	}

	// Find a user and eager-load their posts.
	first, err := orm.Query[User](conn).Find(ctx, users[0].ID)
	must(err)
	var posts []Post
	must(relations.HasManyOf(conn, first, &posts, "user_id").Load(ctx))
	fmt.Printf("%s has %d posts\n", first.Name, len(posts))

	// Soft-delete a user and confirm the default scope hides them.
	must(orm.Delete(ctx, conn, &users[1]))
	if missing, err := orm.Query[User](conn).Find(ctx, users[1].ID); err == nil {
		panic(fmt.Errorf("expected soft-delete to hide user, but loaded %v", missing))
	}

	// WithTrashed surfaces soft-deleted rows.
	soft, err := orm.Query[User](conn).WithTrashed().Find(ctx, users[1].ID)
	must(err)
	fmt.Printf("trashed user: %s (deleted_at=%v)\n", soft.Name, soft.DeletedAt.Time.Format(time.RFC3339))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
