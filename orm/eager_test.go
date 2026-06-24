package orm_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/logger"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/query"
	"github.com/devituz/lagodev/schema"
	lagotest "github.com/devituz/lagodev/testing"
)

// selectCounter is an io.Writer plugged into the connection's SQL logger; it
// tallies the SELECT statements that flow through so a test can assert an eager
// load issues one query per relation level rather than N+1.
type selectCounter struct {
	mu sync.Mutex
	n  int
}

func (c *selectCounter) Write(p []byte) (int, error) {
	if strings.Contains(strings.ToUpper(string(p)), "SELECT") {
		c.mu.Lock()
		c.n++
		c.mu.Unlock()
	}
	return len(p), nil
}

func (c *selectCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *selectCounter) reset() {
	c.mu.Lock()
	c.n = 0
	c.mu.Unlock()
}

// --- Eager-loading test models: users → posts → comments. ----------------

type EUser struct {
	orm.Model
	Name  string
	Posts []EPost `column:"-"`
}

func (EUser) TableName() string { return "eusers" }

func (EUser) Relations() map[string]orm.RelationDef {
	return map[string]orm.RelationDef{
		"posts": {Kind: orm.HasMany, Field: "Posts", Related: EPost{}, ForeignKey: "user_id"},
	}
}

type EPost struct {
	orm.Model
	UserID   uint64 `column:"user_id"`
	Title    string
	Comments []EComment `column:"-"`
	Author   *EUser     `column:"-"`
}

func (EPost) TableName() string { return "eposts" }

func (EPost) Relations() map[string]orm.RelationDef {
	return map[string]orm.RelationDef{
		"comments": {Kind: orm.HasMany, Field: "Comments", Related: EComment{}, ForeignKey: "post_id"},
		"author":   {Kind: orm.BelongsTo, Field: "Author", Related: EUser{}, ForeignKey: "user_id"},
	}
}

// EComment is soft-deletable so the default eager-load scope can be asserted.
type EComment struct {
	orm.SoftDeletes
	PostID uint64 `column:"post_id"`
	Body   string
}

func (EComment) TableName() string { return "ecomments" }

var eagerRegistry = migrations.NewRegistry()

func init() {
	eagerRegistry.Register(migrations.Define("00001_eusers",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("eusers", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error { return ctx.Schema(schema.DropIfExists("eusers")) },
	))
	eagerRegistry.Register(migrations.Define("00002_eposts",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("eposts", func(t *schema.Blueprint) {
				t.ID()
				t.ForeignId("user_id").Constrained("eusers").OnDelete("cascade")
				t.String("title")
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error { return ctx.Schema(schema.DropIfExists("eposts")) },
	))
	eagerRegistry.Register(migrations.Define("00003_ecomments",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("ecomments", func(t *schema.Blueprint) {
				t.ID()
				t.ForeignId("post_id").Constrained("eposts").OnDelete("cascade")
				t.String("body")
				t.Timestamps()
				t.SoftDeletes()
			}))
		},
		func(ctx *migrations.Context) error { return ctx.Schema(schema.DropIfExists("ecomments")) },
	))
}

func eagerSetup(t *testing.T) (*database.Connection, func()) {
	t.Helper()
	return lagotest.SQLite(t, lagotest.WithRegistry(eagerRegistry))
}

// seedEager creates two users, gives the first two posts and the second one
// post, then attaches comments. Returns the created user IDs.
func seedEager(t *testing.T, conn *database.Connection) (u1, u2 *EUser) {
	t.Helper()
	ctx := context.Background()

	u1 = &EUser{Name: "Ada"}
	u2 = &EUser{Name: "Grace"}
	require.NoError(t, orm.Save(ctx, conn, u1))
	require.NoError(t, orm.Save(ctx, conn, u2))

	p1 := &EPost{UserID: u1.ID, Title: "p1"}
	p2 := &EPost{UserID: u1.ID, Title: "p2"}
	p3 := &EPost{UserID: u2.ID, Title: "p3"}
	require.NoError(t, orm.Save(ctx, conn, p1))
	require.NoError(t, orm.Save(ctx, conn, p2))
	require.NoError(t, orm.Save(ctx, conn, p3))

	require.NoError(t, orm.Save(ctx, conn, &EComment{PostID: p1.ID, Body: "c1a"}))
	require.NoError(t, orm.Save(ctx, conn, &EComment{PostID: p1.ID, Body: "c1b"}))
	require.NoError(t, orm.Save(ctx, conn, &EComment{PostID: p3.ID, Body: "c3a"}))
	return u1, u2
}

func TestWith_LoadsHasManyBatched(t *testing.T) {
	counter := &selectCounter{}
	log := logger.NewWith(counter, counter, logger.LevelDebug, false)
	conn, cleanup := lagotest.SQLite(t,
		lagotest.WithRegistry(eagerRegistry), lagotest.WithLogger(log))
	defer cleanup()
	ctx := context.Background()

	u1, u2 := seedEager(t, conn)

	counter.reset()
	var users []EUser
	require.NoError(t, orm.Query[EUser](conn).OrderBy("id", "asc").With("posts").Get(ctx, &users))

	require.Len(t, users, 2)
	assert.Equal(t, u1.ID, users[0].ID)
	assert.Len(t, users[0].Posts, 2, "Ada has two posts")
	assert.Len(t, users[1].Posts, 1, "Grace has one post")
	assert.Equal(t, u2.ID, users[1].Posts[0].UserID)

	// One query for the users, one for all their posts: no N+1.
	assert.Equal(t, 2, counter.count(), "expected exactly 2 SELECTs (users + batched posts)")
}

func TestWith_NestedLoadsComments(t *testing.T) {
	counter := &selectCounter{}
	log := logger.NewWith(counter, counter, logger.LevelDebug, false)
	conn, cleanup := lagotest.SQLite(t,
		lagotest.WithRegistry(eagerRegistry), lagotest.WithLogger(log))
	defer cleanup()
	ctx := context.Background()

	seedEager(t, conn)

	counter.reset()
	var users []EUser
	require.NoError(t, orm.Query[EUser](conn).OrderBy("id", "asc").
		With("posts", "posts.comments").Get(ctx, &users))

	// Three relation levels → exactly three SELECTs: users, posts, comments.
	assert.Equal(t, 3, counter.count(), "nested eager load must stay one query per level")

	require.Len(t, users, 2)
	// Ada: post p1 has 2 comments, p2 has 0.
	require.Len(t, users[0].Posts, 2)
	got := map[string]int{}
	for _, p := range users[0].Posts {
		got[p.Title] = len(p.Comments)
	}
	assert.Equal(t, 2, got["p1"])
	assert.Equal(t, 0, got["p2"])
	// Grace: post p3 has 1 comment.
	require.Len(t, users[1].Posts, 1)
	assert.Len(t, users[1].Posts[0].Comments, 1)
}

func TestWith_BelongsToSharedParentFannedOut(t *testing.T) {
	conn, cleanup := eagerSetup(t)
	defer cleanup()
	ctx := context.Background()

	u1, _ := seedEager(t, conn)

	// Both of Ada's posts share user_id=u1; each must receive the Author.
	var posts []EPost
	require.NoError(t, orm.Query[EPost](conn).
		Where("user_id", "=", u1.ID).OrderBy("id", "asc").
		With("author").Get(ctx, &posts))

	require.Len(t, posts, 2)
	for _, p := range posts {
		require.NotNil(t, p.Author, "every post sharing the FK must get the author")
		assert.Equal(t, "Ada", p.Author.Name)
	}
}

func TestWith_ConstrainedEagerLoadFiltersChildren(t *testing.T) {
	conn, cleanup := eagerSetup(t)
	defer cleanup()
	ctx := context.Background()

	u1, _ := seedEager(t, conn)

	var users []EUser
	require.NoError(t, orm.Query[EUser](conn).
		Where("id", "=", u1.ID).
		WithWhere("posts", func(q *query.Builder) { q.Where("title", "=", "p1") }).
		Get(ctx, &users))

	require.Len(t, users, 1)
	require.Len(t, users[0].Posts, 1, "constraint must filter the eager-loaded posts")
	assert.Equal(t, "p1", users[0].Posts[0].Title)
}

func TestWith_SoftDeletedChildrenExcludedByDefault(t *testing.T) {
	conn, cleanup := eagerSetup(t)
	defer cleanup()
	ctx := context.Background()

	seedEager(t, conn)

	// Soft-delete one of p1's comments.
	var comments []EComment
	require.NoError(t, orm.Query[EComment](conn).Where("body", "=", "c1a").Get(ctx, &comments))
	require.Len(t, comments, 1)
	require.NoError(t, orm.Delete(ctx, conn, &comments[0]))

	var users []EUser
	require.NoError(t, orm.Query[EUser](conn).OrderBy("id", "asc").
		With("posts.comments").Get(ctx, &users))

	for _, p := range users[0].Posts {
		if p.Title == "p1" {
			require.Len(t, p.Comments, 1, "the soft-deleted comment must be excluded")
			assert.Equal(t, "c1b", p.Comments[0].Body)
		}
	}
}

func TestWith_DedupsAndEmptyResult(t *testing.T) {
	conn, cleanup := eagerSetup(t)
	defer cleanup()
	ctx := context.Background()

	// No rows at all: eager load must be a no-op, not an error.
	var users []EUser
	require.NoError(t, orm.Query[EUser](conn).With("posts", "posts").Get(ctx, &users))
	assert.Empty(t, users)
}
