package schema_test

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/schema"
)

// execAll compiles a definition and runs every statement against a real DB.
func execAll(t *testing.T, db *sql.DB, def *schema.Definition) {
	t.Helper()
	stmts, err := schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	require.NoError(t, err)
	for _, s := range stmts {
		_, err := db.Exec(s.SQL)
		require.NoErrorf(t, err, "exec failed: %s", s.SQL)
	}
}

// TestRealSQLite_PrimaryKeyAndForeignKey reproduces issues #1 and #2 against a
// live SQLite engine: ID() must yield a usable PK, an FK must reference it, and
// the redundant ID()+Primary("id") case must not raise "more than one primary
// key".
func TestRealSQLite_PrimaryKeyAndForeignKey(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	execAll(t, db, schema.Create("regions", func(t *schema.Blueprint) {
		t.ID()
		t.JSON("name")
	}))

	// FK referencing regions.id — fails on a PK-less parent.
	execAll(t, db, schema.Create("districts", func(t *schema.Blueprint) {
		t.ID()
		t.UnsignedBigInteger("region_id")
		t.Foreign("region_id").References("id").On("regions")
	}))

	// ID() + redundant Primary("id") must not produce a double PK.
	execAll(t, db, schema.Create("zones", func(t *schema.Blueprint) {
		t.ID()
		t.Primary("id")
		t.String("name")
	}))

	// Insert + FK enforcement sanity.
	_, err = db.Exec(`INSERT INTO "regions" ("name") VALUES ('{}')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO "districts" ("region_id") VALUES (1)`)
	require.NoError(t, err, "valid FK insert must succeed")
	_, err = db.Exec(`INSERT INTO "districts" ("region_id") VALUES (999)`)
	require.Error(t, err, "FK violation must be rejected — proves the constraint is live")
}

// TestRealSQLite_BooleanDefault proves issue #3's fix executes on a real engine.
func TestRealSQLite_BooleanDefault(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	execAll(t, db, schema.Create("flags", func(t *schema.Blueprint) {
		t.ID()
		t.Boolean("is_active").Default(true)
		t.Boolean("is_deleted").Default(false)
	}))

	_, err = db.Exec(`INSERT INTO "flags" DEFAULT VALUES`)
	require.NoError(t, err)
	var active, deleted bool
	require.NoError(t, db.QueryRow(`SELECT "is_active", "is_deleted" FROM "flags"`).Scan(&active, &deleted))
	require.True(t, active)
	require.False(t, deleted)
}
