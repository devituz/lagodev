package orm_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/schema"
	lagotest "github.com/devituz/lagodev/testing"
)

// User is the canonical test model; the harness applies the matching
// migration registered in init() below.
type User struct {
	orm.Model
	Name  string
	Email string
	Age   int
}

type Post struct {
	orm.Model
	UserID uint64 `column:"user_id"`
	Title  string
}

func (Post) TableName() string { return "posts" }

// Each test binary gets its own registry to avoid step-on collisions when
// multiple packages run inside `go test ./...`.
var ormRegistry = migrations.NewRegistry()

func init() {
	ormRegistry.Register(migrations.Define("00001_users",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("users", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.String("email").Unique()
				t.Integer("age").Default(0)
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("users"))
		},
	))
	ormRegistry.Register(migrations.Define("00002_posts",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("posts", func(t *schema.Blueprint) {
				t.ID()
				t.ForeignId("user_id").Constrained("users").OnDelete("cascade")
				t.String("title")
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("posts"))
		},
	))
}

func setup(t *testing.T) (*database.Connection, func()) {
	t.Helper()
	return lagotest.SQLite(t, lagotest.WithRegistry(ormRegistry))
}

func TestSave_InsertsAndAssignsID(t *testing.T) {
	conn, cleanup := setup(t)
	defer cleanup()

	u := &User{Name: "Ada", Email: "ada@example.com", Age: 36}
	require.NoError(t, orm.Save(context.Background(), conn, u))
	assert.NotZero(t, u.ID)
	assert.False(t, u.CreatedAt.IsZero())
}

func TestSave_UpdatesExistingRow(t *testing.T) {
	conn, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	u := &User{Name: "Ada", Email: "ada@example.com"}
	require.NoError(t, orm.Save(ctx, conn, u))
	original := u.UpdatedAt
	time.Sleep(2 * time.Millisecond)

	u.Name = "Augusta"
	require.NoError(t, orm.Save(ctx, conn, u))

	loaded, err := orm.Query[User](conn).Find(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "Augusta", loaded.Name)
	assert.True(t, loaded.UpdatedAt.After(original), "updated_at should advance")
}

func TestFind_NotFoundReturnsSentinel(t *testing.T) {
	conn, cleanup := setup(t)
	defer cleanup()

	_, err := orm.Query[User](conn).Find(context.Background(), 9999)
	assert.ErrorIs(t, err, orm.ErrNotFound)
}

func TestDelete_RemovesRow(t *testing.T) {
	conn, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	u := &User{Name: "Gone", Email: "gone@example.com"}
	require.NoError(t, orm.Save(ctx, conn, u))
	require.NoError(t, orm.Delete(ctx, conn, u))

	_, err := orm.Query[User](conn).Find(ctx, u.ID)
	assert.ErrorIs(t, err, orm.ErrNotFound)
}

func TestQuery_WhereAndOrdering(t *testing.T) {
	conn, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	for i, name := range []string{"Alice", "Bob", "Carol"} {
		require.NoError(t, orm.Save(ctx, conn, &User{
			Name: name, Email: name + "@example.com", Age: 20 + i,
		}))
	}

	var users []User
	require.NoError(t, orm.Query[User](conn).
		Where("age", ">=", 21).
		OrderBy("age", "desc").
		Get(ctx, &users))
	require.Len(t, users, 2)
	assert.Equal(t, "Carol", users[0].Name)
	assert.Equal(t, "Bob", users[1].Name)
}

func TestQuery_Count(t *testing.T) {
	conn, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		require.NoError(t, orm.Save(ctx, conn, &User{
			Name: "u", Email: rstr(i) + "@x.com",
		}))
	}
	n, err := orm.Query[User](conn).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(5), n)
}

// Hook-bearing model — verifies that hooks are dispatched.
type Hooked struct {
	orm.Model
	Name    string
	Email   string
	Touched bool `orm:"-"`
}

func (Hooked) TableName() string { return "users" }

func (h *Hooked) BeforeCreate(*orm.HookContext) error {
	h.Touched = true
	return nil
}

func TestHooks_Dispatched(t *testing.T) {
	conn, cleanup := setup(t)
	defer cleanup()

	h := &Hooked{Name: "hooked", Email: "hooked@example.com"}
	require.NoError(t, orm.Save(context.Background(), conn, h))
	assert.True(t, h.Touched, "BeforeCreate hook should have set Touched")
}

func rstr(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	if i < len(letters) {
		return string(letters[i])
	}
	return "x"
}
