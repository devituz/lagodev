package orm_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/schema"
	lagotest "github.com/devituz/lagodev/testing"
)

// Account is a soft-deletable model: it embeds orm.SoftDeletes which adds the
// deleted_at column.
type Account struct {
	orm.SoftDeletes
	Name string
}

func (Account) TableName() string { return "accounts" }

var featRegistry = migrations.NewRegistry()

func init() {
	featRegistry.Register(migrations.Define("00001_accounts",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("accounts", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.Timestamps()
				t.SoftDeletes()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("accounts"))
		},
	))
	featRegistry.Register(migrations.Define("00002_widgets",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("widgets", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.Integer("score").Default(0)
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("widgets"))
		},
	))
}

// Widget is a plain (non-soft-delete) model used to assert hard-delete behavior
// and the write idioms.
type Widget struct {
	orm.Model
	Name  string
	Score int
}

func (Widget) TableName() string { return "widgets" }

func featSetup(t *testing.T) (*database.Connection, func()) {
	t.Helper()
	return lagotest.SQLite(t, lagotest.WithRegistry(featRegistry))
}

func TestSoftDelete_HidesRowByDefault(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	a := &Account{Name: "acme"}
	require.NoError(t, orm.Save(ctx, conn, a))
	require.NoError(t, orm.Delete(ctx, conn, a))
	assert.NotNil(t, a.DeletedAt, "in-memory DeletedAt should be stamped")

	// Default scope hides it.
	_, err := orm.Query[Account](conn).Find(ctx, a.ID)
	assert.ErrorIs(t, err, orm.ErrNotFound)

	var all []Account
	require.NoError(t, orm.Query[Account](conn).Get(ctx, &all))
	assert.Len(t, all, 0)

	n, err := orm.Query[Account](conn).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	// Row physically still present.
	raw, err := orm.Query[Account](conn).WithTrashed().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), raw)
}

func TestSoftDelete_WithAndOnlyTrashed(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	live := &Account{Name: "live"}
	gone := &Account{Name: "gone"}
	require.NoError(t, orm.Save(ctx, conn, live))
	require.NoError(t, orm.Save(ctx, conn, gone))
	require.NoError(t, orm.Delete(ctx, conn, gone))

	var withTrashed []Account
	require.NoError(t, orm.Query[Account](conn).WithTrashed().OrderBy("id", "asc").Get(ctx, &withTrashed))
	require.Len(t, withTrashed, 2)

	var onlyTrashed []Account
	require.NoError(t, orm.Query[Account](conn).OnlyTrashed().Get(ctx, &onlyTrashed))
	require.Len(t, onlyTrashed, 1)
	assert.Equal(t, "gone", onlyTrashed[0].Name)

	found, err := orm.Query[Account](conn).WithTrashed().Find(ctx, gone.ID)
	require.NoError(t, err)
	assert.Equal(t, "gone", found.Name)
}

func TestSoftDelete_Restore(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	a := &Account{Name: "back"}
	require.NoError(t, orm.Save(ctx, conn, a))
	require.NoError(t, orm.Delete(ctx, conn, a))
	require.NoError(t, orm.Restore(ctx, conn, a))
	assert.Nil(t, a.DeletedAt)

	found, err := orm.Query[Account](conn).Find(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, "back", found.Name)
}

func TestSoftDelete_ForceDelete(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	a := &Account{Name: "obliterate"}
	require.NoError(t, orm.Save(ctx, conn, a))
	require.NoError(t, orm.ForceDelete(ctx, conn, a))

	raw, err := orm.Query[Account](conn).WithTrashed().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), raw)
}

func TestDelete_NonSoftDeleteStillHardDeletes(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	w := &Widget{Name: "x"}
	require.NoError(t, orm.Save(ctx, conn, w))
	require.NoError(t, orm.Delete(ctx, conn, w))

	n, err := orm.Query[Widget](conn).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestPaginate_ComputesMetadata(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	for i := 0; i < 23; i++ {
		require.NoError(t, orm.Save(ctx, conn, &Widget{Name: fmt.Sprintf("w%02d", i)}))
	}

	p1, err := orm.Query[Widget](conn).OrderBy("id", "asc").Paginate(ctx, 1, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(23), p1.Total)
	assert.Equal(t, 1, p1.Page)
	assert.Equal(t, 10, p1.PerPage)
	assert.Equal(t, 3, p1.LastPage)
	assert.True(t, p1.HasMore)
	require.Len(t, p1.Data, 10)
	assert.Equal(t, "w00", p1.Data[0].Name)

	p3, err := orm.Query[Widget](conn).OrderBy("id", "asc").Paginate(ctx, 3, 10)
	require.NoError(t, err)
	assert.False(t, p3.HasMore)
	require.Len(t, p3.Data, 3)
	assert.Equal(t, "w22", p3.Data[2].Name)
}

func TestPaginate_GuardsBounds(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, orm.Save(ctx, conn, &Widget{Name: "only"}))
	p, err := orm.Query[Widget](conn).Paginate(ctx, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, p.Page)
	assert.Equal(t, 1, p.PerPage)
	assert.Equal(t, int64(1), p.Total)
	assert.Equal(t, 1, p.LastPage)
}

func TestPaginate_RespectsSoftDeleteScope(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	a := &Account{Name: "keep"}
	gone := &Account{Name: "gone"}
	require.NoError(t, orm.Save(ctx, conn, a))
	require.NoError(t, orm.Save(ctx, conn, gone))
	require.NoError(t, orm.Delete(ctx, conn, gone))

	p, err := orm.Query[Account](conn).Paginate(ctx, 1, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), p.Total)
	require.Len(t, p.Data, 1)
	assert.Equal(t, "keep", p.Data[0].Name)
}

func TestChunk_VisitsEveryRowOnce(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	const total = 25
	for i := 0; i < total; i++ {
		require.NoError(t, orm.Save(ctx, conn, &Widget{Name: fmt.Sprintf("w%02d", i)}))
	}

	seen := map[uint64]int{}
	batches := 0
	err := orm.Query[Widget](conn).Chunk(ctx, 7, func(batch []Widget) error {
		batches++
		for _, w := range batch {
			seen[w.ID]++
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, total, len(seen))
	for id, count := range seen {
		assert.Equalf(t, 1, count, "id %d visited %d times", id, count)
	}
	assert.Equal(t, 4, batches) // 7+7+7+4
}

func TestChunk_PropagatesError(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, orm.Save(ctx, conn, &Widget{Name: fmt.Sprintf("w%d", i)}))
	}
	sentinel := errors.New("stop")
	err := orm.Query[Widget](conn).Chunk(ctx, 2, func(batch []Widget) error {
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel)
}

func TestFirstOrCreate_CreatesThenReturnsExisting(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	w1, err := orm.FirstOrCreate[Widget](ctx, conn,
		map[string]any{"name": "uniq"},
		map[string]any{"score": 7},
	)
	require.NoError(t, err)
	assert.NotZero(t, w1.ID)
	assert.Equal(t, "uniq", w1.Name)
	assert.Equal(t, 7, w1.Score)

	// Second call must find the existing row, not duplicate.
	w2, err := orm.FirstOrCreate[Widget](ctx, conn,
		map[string]any{"name": "uniq"},
		map[string]any{"score": 99},
	)
	require.NoError(t, err)
	assert.Equal(t, w1.ID, w2.ID)
	assert.Equal(t, 7, w2.Score, "defaults must not overwrite the found row")

	n, err := orm.Query[Widget](conn).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestUpdateOrCreate_InsertThenUpdateInPlace(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	w1, err := orm.UpdateOrCreate[Widget](ctx, conn,
		map[string]any{"name": "u"},
		map[string]any{"score": 1},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, w1.Score)

	w2, err := orm.UpdateOrCreate[Widget](ctx, conn,
		map[string]any{"name": "u"},
		map[string]any{"score": 42},
	)
	require.NoError(t, err)
	assert.Equal(t, w1.ID, w2.ID)
	assert.Equal(t, 42, w2.Score)

	n, err := orm.Query[Widget](conn).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	reloaded, err := orm.Query[Widget](conn).Find(ctx, w1.ID)
	require.NoError(t, err)
	assert.Equal(t, 42, reloaded.Score)
}

func TestScope_AppliesReusableConstraint(t *testing.T) {
	conn, cleanup := featSetup(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, orm.Save(ctx, conn, &Widget{Name: "lo", Score: 1}))
	require.NoError(t, orm.Save(ctx, conn, &Widget{Name: "hi", Score: 100}))

	highScore := func(b *orm.Builder[Widget]) *orm.Builder[Widget] {
		return b.Where("score", ">=", 50)
	}

	var got []Widget
	require.NoError(t, orm.Query[Widget](conn).Scope(highScore).Get(ctx, &got))
	require.Len(t, got, 1)
	assert.Equal(t, "hi", got[0].Name)
}
