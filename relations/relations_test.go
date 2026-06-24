package relations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/relations"
	"github.com/devituz/lagodev/schema"
	lagotest "github.com/devituz/lagodev/testing"
)

type Author struct {
	orm.Model
	Name string
}

type Book struct {
	orm.Model
	AuthorID uint64 `column:"author_id"`
	Title    string
}

var registry = migrations.NewRegistry()

func init() {
	registry.Register(migrations.Define("00001_authors",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("authors", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("authors"))
		},
	))
	registry.Register(migrations.Define("00002_books",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("books", func(t *schema.Blueprint) {
				t.ID()
				t.ForeignId("author_id").Constrained("authors").OnDelete("cascade")
				t.String("title")
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("books"))
		},
	))
}

func setup(t *testing.T) (*database.Connection, func()) {
	t.Helper()
	return lagotest.SQLite(t, lagotest.WithRegistry(registry))
}

func TestHasMany_LoadsChildren(t *testing.T) {
	conn, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	a := &Author{Name: "Ada"}
	require.NoError(t, orm.Save(ctx, conn, a))
	for _, title := range []string{"Notes on the Analytical Engine", "Translator's Notes"} {
		require.NoError(t, orm.Save(ctx, conn, &Book{AuthorID: a.ID, Title: title}))
	}

	var books []Book
	rel := relations.HasManyOf(conn, a, &books, "author_id")
	require.NoError(t, rel.Load(ctx))
	assert.Len(t, books, 2)
}

func TestBelongsTo_LoadsParent(t *testing.T) {
	conn, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	a := &Author{Name: "Grace"}
	require.NoError(t, orm.Save(ctx, conn, a))
	b := &Book{AuthorID: a.ID, Title: "COBOL: a memoir"}
	require.NoError(t, orm.Save(ctx, conn, b))

	var owner Author
	rel := relations.BelongsToOf(conn, b, &owner, "author_id")
	require.NoError(t, rel.Load(ctx))
	assert.Equal(t, "Grace", owner.Name)
}

// Bug 2: two children sharing the same author_id must BOTH receive the parent
// after a batch BelongsTo load — the lookup must fan out over all parents
// sharing a key, not keep only the last.
func TestBelongsTo_LoadManySharedKey(t *testing.T) {
	conn, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	a := &Author{Name: "Shared"}
	require.NoError(t, orm.Save(ctx, conn, a))

	b1 := &Book{AuthorID: a.ID, Title: "first"}
	b2 := &Book{AuthorID: a.ID, Title: "second"}
	require.NoError(t, orm.Save(ctx, conn, b1))
	require.NoError(t, orm.Save(ctx, conn, b2))

	owners := map[uint64]string{}
	parents := []any{b1, b2}
	rel := relations.BelongsToOf(conn, b1, &Author{}, "author_id")
	require.NoError(t, rel.LoadMany(ctx, parents, func(parent any, child any) {
		book := parent.(*Book)
		author := child.(Author)
		owners[book.ID] = author.Name
	}))

	assert.Equal(t, "Shared", owners[b1.ID])
	assert.Equal(t, "Shared", owners[b2.ID], "second child sharing the FK must also get the parent")
}

func TestHasMany_LoadManyDistributesChildren(t *testing.T) {
	conn, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	a1 := &Author{Name: "A1"}
	a2 := &Author{Name: "A2"}
	require.NoError(t, orm.Save(ctx, conn, a1))
	require.NoError(t, orm.Save(ctx, conn, a2))
	for _, ax := range []*Author{a1, a1, a2} {
		require.NoError(t, orm.Save(ctx, conn, &Book{AuthorID: ax.ID, Title: "t"}))
	}

	parents := []any{a1, a2}
	bucket := map[uint64][]Book{}
	rel := relations.HasManyOf(conn, a1, &[]Book{}, "author_id")
	require.NoError(t, rel.LoadMany(ctx, parents, func(parent any, children any) {
		owner := parent.(*Author)
		bucket[owner.ID] = children.([]Book)
	}))
	assert.Len(t, bucket[a1.ID], 2)
	assert.Len(t, bucket[a2.ID], 1)
}
