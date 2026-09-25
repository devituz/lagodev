package orm_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/casts"
	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/query"
	"github.com/devituz/lagodev/schema"
	lagotest "github.com/devituz/lagodev/testing"
)

type sweepAuthor struct {
	orm.Model
	Name     string
	Bio      sql.NullString
	Nick     *sql.NullString
	Code     string // stored in an INTEGER column
	Found    bool   `column:"-"`
	Articles []sweepArticle
}

func (sweepAuthor) TableName() string { return "sweep_authors" }

func (sweepAuthor) Relations() map[string]orm.RelationDef {
	return map[string]orm.RelationDef{
		"articles": {Kind: orm.HasMany, Field: "Articles", Related: sweepArticle{}, ForeignKey: "author_id"},
	}
}

func (a *sweepAuthor) AfterFind(*orm.HookContext) error { a.Found = true; return nil }

type sweepArticle struct {
	orm.SoftDeletes
	AuthorID uint64
	Title    string
	Author   *sweepAuthor // BelongsTo destination, no `relation` tag
}

func (sweepArticle) TableName() string { return "sweep_articles" }

func (sweepArticle) Relations() map[string]orm.RelationDef {
	return map[string]orm.RelationDef{
		"author": {Kind: orm.BelongsTo, Field: "Author", Related: sweepAuthor{}, ForeignKey: "author_id"},
	}
}

type sweepToken struct {
	ID    string `column:"id" orm:"primary"`
	Label string
}

func (sweepToken) TableName() string { return "sweep_tokens" }

type sweepBadCast struct{}

func (sweepBadCast) FromDB(src, dst any) error { return nil }
func (sweepBadCast) ToDB(any) (any, error)     { return nil, errors.New("cannot encode") }

type sweepCasted struct {
	orm.Model
	Payload string `orm:"cast:sweep_bad"`
}

func (sweepCasted) TableName() string { return "sweep_casted" }

var sweepRegistry = migrations.NewRegistry()

func init() {
	casts.Register("sweep_bad", sweepBadCast{})
	sweepRegistry.Register(migrations.Define("00001_sweep",
		func(ctx *migrations.Context) error {
			if err := ctx.Schema(schema.Create("sweep_authors", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.Text("bio").Nullable()
				t.String("nick").Nullable()
				t.Integer("code").Default(0)
				t.Timestamps()
			})); err != nil {
				return err
			}
			if err := ctx.Schema(schema.Create("sweep_articles", func(t *schema.Blueprint) {
				t.ID()
				t.BigInteger("author_id")
				t.String("title")
				t.Timestamps()
				t.SoftDeletes()
			})); err != nil {
				return err
			}
			if err := ctx.Schema(schema.Create("sweep_casted", func(t *schema.Blueprint) {
				t.ID()
				t.String("payload")
				t.Timestamps()
			})); err != nil {
				return err
			}
			return ctx.Schema(schema.Create("sweep_tokens", func(t *schema.Blueprint) {
				t.String("id", 64).Primary()
				t.String("label")
			}))
		},
		func(ctx *migrations.Context) error { return nil },
	))
}

func sweepSetup(t *testing.T) (*database.Connection, func()) {
	t.Helper()
	return lagotest.SQLite(t, lagotest.WithRegistry(sweepRegistry))
}

// sql.Scanner fields (sql.NullString, *sql.NullString) failed to hydrate with
// "cannot assign string to sql.NullString"; an INTEGER column read into a
// string field became a rune ("\a") instead of "7".
func TestSweep_ScannerAndNumericStringFields(t *testing.T) {
	c, cleanup := sweepSetup(t)
	defer cleanup()
	ctx := context.Background()

	a := &sweepAuthor{Name: "Ada", Bio: sql.NullString{String: "math", Valid: true}}
	require.NoError(t, orm.Save(ctx, c, a))
	_, err := query.New(c, "sweep_authors").Where("id", a.ID).Update(ctx, map[string]any{"code": 7, "nick": "ada"})
	require.NoError(t, err)

	got, err := orm.Query[sweepAuthor](c).Find(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, sql.NullString{String: "math", Valid: true}, got.Bio)
	require.NotNil(t, got.Nick)
	assert.Equal(t, "ada", got.Nick.String)
	assert.Equal(t, "7", got.Code)

	b := &sweepAuthor{Name: "NoBio"}
	require.NoError(t, orm.Save(ctx, c, b))
	got, err = orm.Query[sweepAuthor](c).Find(ctx, b.ID)
	require.NoError(t, err)
	assert.False(t, got.Bio.Valid)
	assert.Nil(t, got.Nick)
}

// AfterFind was declared and dispatchable but never invoked on hydration.
func TestSweep_AfterFindHookRuns(t *testing.T) {
	c, cleanup := sweepSetup(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, orm.Save(ctx, c, &sweepAuthor{Name: "Ada"}))

	var all []sweepAuthor
	require.NoError(t, orm.Query[sweepAuthor](c).Get(ctx, &all))
	require.Len(t, all, 1)
	assert.True(t, all[0].Found)
	first, err := orm.Query[sweepAuthor](c).First(ctx)
	require.NoError(t, err)
	assert.True(t, first.Found)
}

// A *Struct relation field without a `relation` tag was persisted as a
// column, so Save failed with "no column named author".
func TestSweep_PointerRelationFieldNotPersisted(t *testing.T) {
	c, cleanup := sweepSetup(t)
	defer cleanup()
	ctx := context.Background()

	a := &sweepAuthor{Name: "Ada"}
	require.NoError(t, orm.Save(ctx, c, a))
	art := &sweepArticle{AuthorID: a.ID, Title: "t", Author: a}
	require.NoError(t, orm.Save(ctx, c, art))
	art.Title = "t2"
	require.NoError(t, orm.Save(ctx, c, art))

	var arts []sweepArticle
	require.NoError(t, orm.Query[sweepArticle](c).With("author").Get(ctx, &arts))
	require.Len(t, arts, 1)
	assert.Equal(t, "t2", arts[0].Title)
	require.NotNil(t, arts[0].Author)
	assert.Equal(t, "Ada", arts[0].Author.Name)
}

// A caller-assigned string primary key made Save take the UPDATE path, which
// matched zero rows: the record was never inserted.
func TestSweep_SaveWithAssignedStringPK(t *testing.T) {
	c, cleanup := sweepSetup(t)
	defer cleanup()
	ctx := context.Background()

	tok := &sweepToken{ID: "tok-1", Label: "ci"}
	require.NoError(t, orm.Save(ctx, c, tok))
	assert.Equal(t, "tok-1", tok.ID)

	tok.Label = "deploy"
	require.NoError(t, orm.Save(ctx, c, tok))

	n, err := orm.Query[sweepToken](c).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	got, err := orm.Query[sweepToken](c).Find(ctx, "tok-1")
	require.NoError(t, err)
	assert.Equal(t, "deploy", got.Label)
}

type sweepComment struct {
	ID       int64 `column:"id" orm:"primary;autoincrement"`
	AuthorID int64 // int64 FK vs the parent's uint64 ID
	Body     string
}

func (sweepComment) TableName() string { return "sweep_comments" }

type sweepCommenter struct {
	orm.Model
	Name     string
	Bio      string // NULL in the DB
	Comments []sweepComment
	Tags     []sweepAuthor
}

func (sweepCommenter) TableName() string { return "sweep_authors" }

func (sweepCommenter) Relations() map[string]orm.RelationDef {
	return map[string]orm.RelationDef{
		"comments": {Kind: orm.HasMany, Field: "Comments", Related: sweepComment{}, ForeignKey: "author_id"},
		"tags": {Kind: orm.BelongsToMany, Field: "Tags", Related: sweepAuthor{},
			PivotTable: "sweep_pivot", PivotForeignFK: "a_id", PivotRelatedFK: "b_id"},
	}
}

// Relation loading scanned straight into struct fields (NULL columns failed
// with "converting NULL to string is unsupported") and bucketed by raw key
// values, so an int64 FK never matched a uint64 parent ID.
func TestSweep_RelationsNullColumnsAndKeyTypes(t *testing.T) {
	c, cleanup := sweepSetup(t)
	defer cleanup()
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE sweep_comments (id INTEGER PRIMARY KEY AUTOINCREMENT, author_id INTEGER, body TEXT)`,
		`CREATE TABLE sweep_pivot (a_id INTEGER, b_id INTEGER)`,
	} {
		_, err := c.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}

	a := &sweepAuthor{Name: "Ada"} // bio stays NULL
	b := &sweepAuthor{Name: "Bob"}
	require.NoError(t, orm.Save(ctx, c, a))
	require.NoError(t, orm.Save(ctx, c, b))
	require.NoError(t, orm.Save(ctx, c, &sweepComment{AuthorID: int64(a.ID), Body: "hi"}))
	_, err := query.New(c, "sweep_pivot").Insert(ctx, map[string]any{"a_id": a.ID, "b_id": b.ID})
	require.NoError(t, err)

	var got []sweepCommenter
	require.NoError(t, orm.Query[sweepCommenter](c).With("comments", "tags").Where("id", a.ID).Get(ctx, &got))
	require.Len(t, got, 1)
	require.Len(t, got[0].Comments, 1)
	assert.Equal(t, "hi", got[0].Comments[0].Body)
	require.Len(t, got[0].Tags, 1)
	assert.Equal(t, "Bob", got[0].Tags[0].Name)
	assert.False(t, got[0].Tags[0].Bio.Valid)
}

// A failing cast ToDB was swallowed and the raw value written instead.
func TestSweep_CastErrorPropagates(t *testing.T) {
	c, cleanup := sweepSetup(t)
	defer cleanup()
	err := orm.Save(context.Background(), c, &sweepCasted{Payload: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot encode")
}

// Paginate and Chunk ignored With(): relations stayed empty.
func TestSweep_PaginateAndChunkEagerLoad(t *testing.T) {
	c, cleanup := sweepSetup(t)
	defer cleanup()
	ctx := context.Background()

	a := &sweepAuthor{Name: "Ada"}
	require.NoError(t, orm.Save(ctx, c, a))
	for _, title := range []string{"a", "b"} {
		require.NoError(t, orm.Save(ctx, c, &sweepArticle{AuthorID: a.ID, Title: title}))
	}

	page, err := orm.Query[sweepAuthor](c).With("articles").Paginate(ctx, 1, 10)
	require.NoError(t, err)
	require.Len(t, page.Data, 1)
	assert.Len(t, page.Data[0].Articles, 2)

	loaded := 0
	require.NoError(t, orm.Query[sweepAuthor](c).With("articles").Chunk(ctx, 10, func(batch []sweepAuthor) error {
		for _, x := range batch {
			loaded += len(x.Articles)
		}
		return nil
	}))
	assert.Equal(t, 2, loaded)
}

// The soft-delete scope (and Chunk's key cursor) was appended after OR-ed user
// conditions, binding only to the last branch: trashed rows leaked and Chunk
// looped over the same rows.
func TestSweep_ScopeAppliesToWholeOrFilter(t *testing.T) {
	c, cleanup := sweepSetup(t)
	defer cleanup()
	ctx := context.Background()

	var arts []*sweepArticle
	for _, title := range []string{"a", "b", "c"} {
		art := &sweepArticle{AuthorID: 1, Title: title}
		require.NoError(t, orm.Save(ctx, c, art))
		arts = append(arts, art)
	}
	require.NoError(t, orm.Delete(ctx, c, arts[0]))

	n, err := orm.Query[sweepArticle](c).Where("title", "a").OrWhere("title", "b").Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "soft-deleted row must not leak through OR")

	seen := 0
	err = orm.Query[sweepArticle](c).WithTrashed().Where("title", "a").OrWhere("title", "b").
		Chunk(ctx, 1, func(batch []sweepArticle) error {
			seen += len(batch)
			if seen > 10 {
				return errors.New("chunk did not advance")
			}
			return nil
		})
	require.NoError(t, err)
	assert.Equal(t, 2, seen)
}
