package orm_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/auth"
	"github.com/devituz/lagodev/authz"
	"github.com/devituz/lagodev/orm"
)

// account is the authenticated principal used in the RBAC checks below.
type account struct {
	ID   uint64
	Role string
}

// TestEndToEnd_CRUD_JWT_RBAC exercises the full stack the way a real service
// would: migrate → create a user (CRUD) → mint and verify a JWT → enforce RBAC
// on a resource → update/delete. It guards against regressions in the three
// schema fixes (real PK so the posts→users FK is valid) plus the JWT jti fix.
func TestEndToEnd_CRUD_JWT_RBAC(t *testing.T) {
	conn, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	// --- Auth bootstrap ---
	mgr, err := auth.New(auth.Config{
		Secret:     "e2e-secret-please-rotate-in-production-32!",
		Issuer:     "lagodev-e2e",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: time.Hour,
		BcryptCost: 4,
	})
	require.NoError(t, err)

	hash, err := mgr.HashPassword("s3cret-pw")
	require.NoError(t, err)
	require.True(t, mgr.VerifyPassword(hash, "s3cret-pw"))
	require.False(t, mgr.VerifyPassword(hash, "wrong"))

	// --- CREATE: persist the user (FK target must have a real PK) ---
	author := &User{Name: "Ada", Email: "ada@example.com", Age: 36}
	require.NoError(t, orm.Save(ctx, conn, author))
	require.NotZero(t, author.ID)

	// --- JWT: issue an access token and parse it back ---
	access, exp, err := mgr.IssueAccess(author.ID, "author")
	require.NoError(t, err)
	require.True(t, exp.After(time.Now()))

	claims, err := mgr.Parse(access)
	require.NoError(t, err)
	require.Equal(t, author.ID, claims.UserID)
	require.Equal(t, "author", claims.Role)
	require.NotEmpty(t, claims.ID, "jti must be present (issue #4)")

	// Two refresh tokens minted back-to-back must differ (no deterministic dup).
	r1, _, err := mgr.IssueRefresh(author.ID, "author")
	require.NoError(t, err)
	r2, _, err := mgr.IssueRefresh(author.ID, "author")
	require.NoError(t, err)
	require.NotEqual(t, r1, r2)

	// --- CREATE child row across the FK (posts.user_id → users.id) ---
	post := &Post{UserID: author.ID, Title: "Hello"}
	require.NoError(t, orm.Save(ctx, conn, post))
	require.NotZero(t, post.ID)

	// --- READ back ---
	got, err := orm.Query[Post](conn).Find(ctx, post.ID)
	require.NoError(t, err)
	require.Equal(t, "Hello", got.Title)
	require.Equal(t, author.ID, got.UserID)

	// --- RBAC: only the author or an admin may update a post ---
	gate := authz.New[account]()
	authz.Before(gate, func(_ context.Context, u account, _ string) bool {
		return u.Role == "admin" // admin overrides everything
	})
	authz.Define(gate, "update", func(_ context.Context, u account, p *Post) bool {
		return p.UserID == u.ID
	})

	owner := account{ID: author.ID, Role: "author"}
	stranger := account{ID: author.ID + 999, Role: "author"}
	admin := account{ID: 1, Role: "admin"}

	require.NoError(t, gate.Authorize(ctx, "update", owner, got))
	require.Error(t, gate.Authorize(ctx, "update", stranger, got))
	require.NoError(t, gate.Authorize(ctx, "update", admin, got))

	// --- UPDATE (allowed principal) ---
	got.Title = "Hello, edited"
	require.NoError(t, orm.Save(ctx, conn, got))
	reread, err := orm.Query[Post](conn).Find(ctx, post.ID)
	require.NoError(t, err)
	require.Equal(t, "Hello, edited", reread.Title)

	// --- DELETE ---
	require.NoError(t, orm.Delete(ctx, conn, reread))
	_, err = orm.Query[Post](conn).Find(ctx, post.ID)
	require.Error(t, err, "row must be gone after delete")
}
