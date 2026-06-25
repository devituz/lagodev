package orm_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/query"
	"github.com/devituz/lagodev/schema"
	lagotest "github.com/devituz/lagodev/testing"
)

// ----------------------------------------------------------------------------
// Models / migrations for the edge suite
// ----------------------------------------------------------------------------

// EdgeProfile carries every cast kind so we can assert a full round-trip through
// the database: JSON, bool, datetime, plus a NULLable column.
type EdgeProfile struct {
	orm.Model
	Name     string         `column:"name"`
	Meta     map[string]any `column:"meta" orm:"cast:json"`
	Active   bool           `column:"active" orm:"cast:bool"`
	LastSeen time.Time      `column:"last_seen" orm:"cast:datetime"`
	Note     *string        `column:"note"`
}

func (EdgeProfile) TableName() string { return "edge_profiles" }

// Ledger records hook invocation order and lets a hook abort the operation.
type Ledger struct {
	orm.Model
	Name string `column:"name"`

	// order accumulates the hook names as they fire; never persisted.
	order   *[]string `orm:"-"`
	failOn  string    `orm:"-"`
	failErr error     `orm:"-"`
}

func (Ledger) TableName() string { return "ledgers" }

func (l *Ledger) record(name string) error {
	if l.order != nil {
		*l.order = append(*l.order, name)
	}
	if l.failOn == name {
		return l.failErr
	}
	return nil
}

func (l *Ledger) BeforeSave(*orm.HookContext) error   { return l.record("BeforeSave") }
func (l *Ledger) BeforeCreate(*orm.HookContext) error { return l.record("BeforeCreate") }
func (l *Ledger) AfterCreate(*orm.HookContext) error  { return l.record("AfterCreate") }
func (l *Ledger) BeforeUpdate(*orm.HookContext) error { return l.record("BeforeUpdate") }
func (l *Ledger) AfterUpdate(*orm.HookContext) error  { return l.record("AfterUpdate") }
func (l *Ledger) AfterSave(*orm.HookContext) error    { return l.record("AfterSave") }
func (l *Ledger) BeforeDelete(*orm.HookContext) error { return l.record("BeforeDelete") }
func (l *Ledger) AfterDelete(*orm.HookContext) error  { return l.record("AfterDelete") }

var edgeRegistry = migrations.NewRegistry()

func init() {
	edgeRegistry.Register(migrations.Define("00001_profiles",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("edge_profiles", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.JSON("meta").Nullable()
				t.Boolean("active").Default(false)
				t.DateTime("last_seen").Nullable()
				t.String("note").Nullable()
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("edge_profiles"))
		},
	))
	edgeRegistry.Register(migrations.Define("00002_ledgers",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("ledgers", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("ledgers"))
		},
	))
}

func edgeSetup(t *testing.T) (*database.Connection, func()) {
	t.Helper()
	return lagotest.SQLite(t, lagotest.WithRegistry(edgeRegistry))
}

// ----------------------------------------------------------------------------
// Transactions: commit / rollback / savepoint
// ----------------------------------------------------------------------------

func TestTx_CommitPersists(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	err := conn.Transaction(ctx, func(tx *database.Tx) error {
		_, ierr := orm.Query[Ledger](conn).WithTx(tx).QB().
			InsertGetID(ctx, map[string]any{
				"name":       "committed",
				"created_at": conn.Now(),
				"updated_at": conn.Now(),
			}, "id")
		return ierr
	})
	require.NoError(t, err)

	n, err := orm.Query[Ledger](conn).Where("name", "=", "committed").Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestTx_RollbackOnError(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	sentinel := errors.New("boom")
	err := conn.Transaction(ctx, func(tx *database.Tx) error {
		if _, ierr := orm.Query[Ledger](conn).WithTx(tx).QB().
			InsertGetID(ctx, map[string]any{
				"name":       "rolled-back",
				"created_at": conn.Now(),
				"updated_at": conn.Now(),
			}, "id"); ierr != nil {
			return ierr
		}
		// Abort: the inserted row must not survive.
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)

	n, err := orm.Query[Ledger](conn).Where("name", "=", "rolled-back").Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "row must be rolled back")
}

func TestTx_SavepointRollbackTo(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	err := conn.Transaction(ctx, func(tx *database.Tx) error {
		insert := func(name string) error {
			_, e := orm.Query[Ledger](conn).WithTx(tx).QB().
				InsertGetID(ctx, map[string]any{
					"name":       name,
					"created_at": conn.Now(),
					"updated_at": conn.Now(),
				}, "id")
			return e
		}
		if err := insert("keep"); err != nil {
			return err
		}
		if err := tx.Savepoint(ctx, "sp1"); err != nil {
			return err
		}
		if err := insert("discard"); err != nil {
			return err
		}
		// Undo the second insert only.
		if err := tx.RollbackTo(ctx, "sp1"); err != nil {
			return err
		}
		return nil
	})
	require.NoError(t, err)

	keep, err := orm.Query[Ledger](conn).Where("name", "=", "keep").Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), keep, "pre-savepoint row survives")

	discard, err := orm.Query[Ledger](conn).Where("name", "=", "discard").Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), discard, "post-savepoint row rolled back")
}

func TestTx_PanicRollsBack(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	func() {
		defer func() {
			r := recover()
			require.NotNil(t, r, "panic should propagate out of Transaction")
		}()
		_ = conn.Transaction(ctx, func(tx *database.Tx) error {
			_, _ = orm.Query[Ledger](conn).WithTx(tx).QB().
				InsertGetID(ctx, map[string]any{
					"name":       "panicked",
					"created_at": conn.Now(),
					"updated_at": conn.Now(),
				}, "id")
			panic("kaboom")
		})
	}()

	n, err := orm.Query[Ledger](conn).Where("name", "=", "panicked").Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "panic must roll the tx back")
}

// ----------------------------------------------------------------------------
// Lifecycle hooks: order + abort on error
// ----------------------------------------------------------------------------

func TestHooks_CreateOrder(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	var order []string
	l := &Ledger{Name: "h", order: &order}
	require.NoError(t, orm.Save(ctx, conn, l))
	assert.Equal(t, []string{"BeforeSave", "BeforeCreate", "AfterCreate", "AfterSave"}, order)
}

func TestHooks_UpdateOrder(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	var order []string
	l := &Ledger{Name: "h", order: &order}
	require.NoError(t, orm.Save(ctx, conn, l))
	order = order[:0]
	*l.order = order

	l.Name = "h2"
	require.NoError(t, orm.Save(ctx, conn, l))
	assert.Equal(t, []string{"BeforeSave", "BeforeUpdate", "AfterUpdate", "AfterSave"}, *l.order)
}

func TestHooks_DeleteOrder(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	var order []string
	l := &Ledger{Name: "h", order: &order}
	require.NoError(t, orm.Save(ctx, conn, l))
	*l.order = (*l.order)[:0]

	require.NoError(t, orm.Delete(ctx, conn, l))
	assert.Equal(t, []string{"BeforeDelete", "AfterDelete"}, *l.order)
}

func TestHooks_BeforeCreateErrorAborts(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	abort := errors.New("denied")
	var order []string
	l := &Ledger{Name: "denied", order: &order, failOn: "BeforeCreate", failErr: abort}

	err := orm.Save(ctx, conn, l)
	require.ErrorIs(t, err, abort)

	// AfterCreate / AfterSave must not have fired, and nothing was persisted.
	assert.Equal(t, []string{"BeforeSave", "BeforeCreate"}, order)
	assert.Zero(t, l.ID, "no INSERT should have happened")

	n, cerr := orm.Query[Ledger](conn).Where("name", "=", "denied").Count(ctx)
	require.NoError(t, cerr)
	assert.Equal(t, int64(0), n)
}

func TestHooks_BeforeSaveErrorAborts(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	abort := errors.New("nope")
	var order []string
	l := &Ledger{Name: "x", order: &order, failOn: "BeforeSave", failErr: abort}

	err := orm.Save(ctx, conn, l)
	require.ErrorIs(t, err, abort)
	// Only BeforeSave ran; BeforeCreate never reached.
	assert.Equal(t, []string{"BeforeSave"}, order)
	assert.Zero(t, l.ID)
}

// ----------------------------------------------------------------------------
// Casts: round-trip + NULL handling
// ----------------------------------------------------------------------------

func TestCasts_RoundTrip(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	seen := time.Date(2024, 3, 1, 12, 30, 0, 0, time.UTC)
	note := "hello"
	p := &EdgeProfile{
		Name:     "ada",
		Meta:     map[string]any{"role": "admin", "level": float64(7)},
		Active:   true,
		LastSeen: seen,
		Note:     &note,
	}
	require.NoError(t, orm.Save(ctx, conn, p))
	require.NotZero(t, p.ID)

	loaded, err := orm.Query[EdgeProfile](conn).Find(ctx, p.ID)
	require.NoError(t, err)

	assert.Equal(t, "admin", loaded.Meta["role"])
	assert.Equal(t, float64(7), loaded.Meta["level"])
	assert.True(t, loaded.Active)
	assert.True(t, loaded.LastSeen.Equal(seen), "datetime cast round-trip: got %v want %v", loaded.LastSeen, seen)
	require.NotNil(t, loaded.Note)
	assert.Equal(t, "hello", *loaded.Note)
}

func TestCasts_NullHandling(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	// Zero time → NULL via datetime cast; nil map → NULL JSON; nil *string.
	p := &EdgeProfile{Name: "blank", Active: false}
	require.NoError(t, orm.Save(ctx, conn, p))

	loaded, err := orm.Query[EdgeProfile](conn).Find(ctx, p.ID)
	require.NoError(t, err)

	assert.Nil(t, loaded.Meta, "nil JSON column hydrates to nil map")
	assert.False(t, loaded.Active)
	assert.True(t, loaded.LastSeen.IsZero(), "NULL datetime hydrates to zero time")
	assert.Nil(t, loaded.Note, "NULL string column hydrates to nil pointer")
}

// ----------------------------------------------------------------------------
// SQL-injection safety
// ----------------------------------------------------------------------------

func TestInjection_MaliciousValueStoredIntact(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	payloads := []string{
		`Robert'); DROP TABLE ledgers;--`,
		`x" OR "1"="1`,
		`'; DELETE FROM ledgers WHERE '1'='1`,
		`100%; -- comment`,
	}
	for _, p := range payloads {
		l := &Ledger{Name: p}
		require.NoError(t, orm.Save(ctx, conn, l))
	}

	// The table still exists and holds every row verbatim — proof the values
	// were parameterized, not interpolated into the SQL text.
	n, err := orm.Query[Ledger](conn).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(len(payloads)), n, "injection payloads did not execute as SQL")

	for _, p := range payloads {
		got, err := orm.Query[Ledger](conn).Where("name", "=", p).First(ctx)
		require.NoError(t, err, "round-trip payload %q", p)
		assert.Equal(t, p, got.Name, "value stored and read back byte-for-byte")
	}
}

func TestInjection_ToSQLUsesPlaceholders(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()

	malicious := `'; DROP TABLE ledgers;--`
	sql, args, err := query.New(conn, "ledgers").
		Where("name", "=", malicious).
		WhereIn("id", []int{1, 2, 3}).
		ToSQL()
	require.NoError(t, err)

	// The dangerous string must never appear in the SQL text; it lives only in
	// the bound args.
	assert.NotContains(t, sql, "DROP TABLE", "value must not be interpolated into SQL")
	assert.NotContains(t, sql, malicious)
	assert.Equal(t, strings.Count(sql, "?"), len(args), "every arg has exactly one placeholder")
	assert.Contains(t, args, malicious, "malicious value travels as a bound arg")
}

// ----------------------------------------------------------------------------
// Pagination boundaries (integer-overflow class)
// ----------------------------------------------------------------------------

func TestPaginate_Boundaries(t *testing.T) {
	conn, cleanup := edgeSetup(t)
	defer cleanup()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, orm.Save(ctx, conn, &Ledger{Name: "p"}))
	}

	cases := []struct {
		name    string
		page    int
		perPage int
	}{
		{"page-zero", 0, 10},
		{"negative-page", -5, 10},
		{"perpage-zero", 1, 0},
		{"negative-perpage", 1, -3},
		{"huge-page", 1_000_000_000, 10},
		{"max-int-page", int(^uint(0) >> 1), 10},
		{"max-int-perpage", 1, int(^uint(0) >> 1)},
		{"both-max", int(^uint(0) >> 1), int(^uint(0) >> 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Must not panic on integer overflow in (page-1)*perPage.
			pg, err := orm.Query[Ledger](conn).Paginate(ctx, tc.page, tc.perPage)
			require.NoError(t, err)
			require.NotNil(t, pg)
			assert.Equal(t, int64(5), pg.Total)
			assert.GreaterOrEqual(t, pg.Page, 1)
			assert.GreaterOrEqual(t, pg.PerPage, 1)
			assert.GreaterOrEqual(t, pg.LastPage, 1)
		})
	}
}
