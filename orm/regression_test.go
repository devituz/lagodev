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

// Profile carries a nullable column (bio) to exercise NULL scanning.
type Profile struct {
	orm.Model
	Name string
	Bio  string `column:"bio"`
}

func (Profile) TableName() string { return "profiles" }

var regRegistry = migrations.NewRegistry()

func init() {
	regRegistry.Register(migrations.Define("00001_profiles",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("profiles", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.String("bio").Nullable()
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("profiles"))
		},
	))
}

func regSetup(t *testing.T) (*database.Connection, func()) {
	t.Helper()
	return lagotest.SQLite(t, lagotest.WithRegistry(regRegistry))
}

// Bug 1: UPDATE must not clobber created_at, must keep PK, must advance
// updated_at, and must leave untouched columns intact.
func TestSave_UpdateDoesNotClobberCreatedAt(t *testing.T) {
	conn, cleanup := regSetup(t)
	defer cleanup()
	ctx := context.Background()

	p := &Profile{Name: "Ada", Bio: "original"}
	require.NoError(t, orm.Save(ctx, conn, p))

	// Reload to capture the persisted created_at.
	loaded, err := orm.Query[Profile](conn).Find(ctx, p.ID)
	require.NoError(t, err)
	origCreated := loaded.CreatedAt
	origID := loaded.ID
	require.False(t, origCreated.IsZero())

	time.Sleep(2 * time.Millisecond)

	// Mutate one field, then Save (update path).
	loaded.Name = "Augusta"
	require.NoError(t, orm.Save(ctx, conn, loaded))

	reloaded, err := orm.Query[Profile](conn).Find(ctx, origID)
	require.NoError(t, err)
	assert.Equal(t, "Augusta", reloaded.Name)
	assert.Equal(t, "original", reloaded.Bio, "untouched column must survive")
	assert.Equal(t, origID, reloaded.ID, "primary key unchanged")
	assert.WithinDuration(t, origCreated, reloaded.CreatedAt, time.Millisecond,
		"created_at must not change on update")
	assert.True(t, reloaded.UpdatedAt.After(origCreated), "updated_at must advance")
}

// Bug 3: scanning a NULL from a nullable column into a non-pointer string field
// must coalesce to the zero value, not error the whole query.
func TestQuery_NullScansToZeroValue(t *testing.T) {
	conn, cleanup := regSetup(t)
	defer cleanup()
	ctx := context.Background()

	// Bio left as "" — but we want an actual SQL NULL. Insert via raw builder
	// omitting bio so the column stays NULL.
	p := &Profile{Name: "NoBio"}
	require.NoError(t, orm.Save(ctx, conn, p))
	_, err := conn.ExecContext(ctx, `UPDATE profiles SET bio = NULL WHERE id = ?`, p.ID)
	require.NoError(t, err)

	loaded, err := orm.Query[Profile](conn).Find(ctx, p.ID)
	require.NoError(t, err, "NULL must not error the scan")
	assert.Equal(t, "", loaded.Bio)
	assert.Equal(t, "NoBio", loaded.Name)
}

// Bug 6: builder methods must not leak state across calls. First/Find run on a
// clone, so the receiver retains its full clause set.
func TestBuilder_FirstThenGetReturnsAllRows(t *testing.T) {
	conn, cleanup := regSetup(t)
	defer cleanup()
	ctx := context.Background()

	for _, n := range []string{"a", "b", "c"} {
		require.NoError(t, orm.Save(ctx, conn, &Profile{Name: n}))
	}

	b := orm.Query[Profile](conn).OrderBy("id", "asc")
	first, err := b.First(ctx)
	require.NoError(t, err)
	assert.Equal(t, "a", first.Name)

	// Reusing the same builder must still return ALL rows (First's Limit(1)
	// must not have leaked into the receiver).
	var all []Profile
	require.NoError(t, b.Get(ctx, &all))
	assert.Len(t, all, 3)
}

func TestBuilder_RepeatedFind(t *testing.T) {
	conn, cleanup := regSetup(t)
	defer cleanup()
	ctx := context.Background()

	p1 := &Profile{Name: "one"}
	p2 := &Profile{Name: "two"}
	require.NoError(t, orm.Save(ctx, conn, p1))
	require.NoError(t, orm.Save(ctx, conn, p2))

	b := orm.Query[Profile](conn)
	got1, err := b.Find(ctx, p1.ID)
	require.NoError(t, err)
	assert.Equal(t, "one", got1.Name)

	// A second Find on the same builder must not accumulate the first WHERE.
	got2, err := b.Find(ctx, p2.ID)
	require.NoError(t, err)
	assert.Equal(t, "two", got2.Name)
}
