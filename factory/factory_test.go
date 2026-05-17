package factory_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/factory"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/schema"
	lagotest "github.com/devituz/lagodev/testing"
)

type User struct {
	orm.Model
	Name  string
	Email string
}

var reg = migrations.NewRegistry()

func init() {
	reg.Register(migrations.Define("00001_users",
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
}

func userFactory(conn *database.Connection) *factory.Factory[User] {
	return factory.New(conn, func(f *factory.Faker) User {
		return User{Name: f.Name(), Email: f.Email()}
	})
}

func TestMake_DoesNotPersist(t *testing.T) {
	conn, cleanup := lagotest.SQLite(t, lagotest.WithRegistry(reg))
	defer cleanup()

	users := userFactory(conn).Count(3).Make()
	require.Len(t, users, 3)
	for _, u := range users {
		assert.Zero(t, u.ID, "Make() must not assign an ID")
		assert.NotEmpty(t, u.Name)
		assert.NotEmpty(t, u.Email)
	}
}

func TestCreate_Persists(t *testing.T) {
	conn, cleanup := lagotest.SQLite(t, lagotest.WithRegistry(reg))
	defer cleanup()

	users, err := userFactory(conn).Count(5).Create(context.Background())
	require.NoError(t, err)
	require.Len(t, users, 5)
	for _, u := range users {
		assert.NotZero(t, u.ID)
	}

	n, err := orm.Query[User](conn).Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(5), n)
}

func TestState_Override(t *testing.T) {
	conn, cleanup := lagotest.SQLite(t, lagotest.WithRegistry(reg))
	defer cleanup()

	u := userFactory(conn).
		State(func(_ *factory.Faker, u *User) { u.Name = "Pinned" }).
		MakeOne()
	assert.Equal(t, "Pinned", u.Name)
}

func TestSeededFaker_Deterministic(t *testing.T) {
	a := factory.NewSeeded(42)
	b := factory.NewSeeded(42)
	assert.Equal(t, a.Name(), b.Name())
}
