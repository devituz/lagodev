package schema_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/drivers/mysql"
	"github.com/devituz/lagodev/drivers/postgres"
	"github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/schema"
)

// usersBlueprint returns the canonical "users" CREATE blueprint used in the
// snapshot tests. Keeping it in one place keeps the tests honest about what
// each dialect actually produces for the same input.
func usersBlueprint() *schema.Definition {
	return schema.Create("users", func(t *schema.Blueprint) {
		t.ID()
		t.String("name")
		t.String("email").Unique()
		t.String("phone").Nullable()
		t.Boolean("is_admin").Default(false)
		t.Integer("age").Default(18)
		t.JSON("meta").Nullable()
		t.Timestamps()
		t.SoftDeletes()
	})
}

func TestCreateUsersTable_SQLite(t *testing.T) {
	c := schema.NewCompiler(sqlite.Grammar{})
	stmts, err := c.Compile(usersBlueprint())
	require.NoError(t, err)
	require.NotEmpty(t, stmts)

	create := stmts[0].SQL
	assert.True(t, strings.HasPrefix(create, `CREATE TABLE "users"`), "got %q", create)
	assert.Contains(t, create, `"id" INTEGER NOT NULL PRIMARY KEY`)
	assert.Contains(t, create, `"name" VARCHAR(255) NOT NULL`)
	assert.Contains(t, create, `"email" VARCHAR(255) NOT NULL UNIQUE`)
	assert.Contains(t, create, `"phone" VARCHAR(255) NULL`)
	assert.Contains(t, create, `"is_admin" BOOLEAN NOT NULL DEFAULT 0`)
	assert.Contains(t, create, `"age" INTEGER NOT NULL DEFAULT 18`)
	assert.Contains(t, create, `"meta" TEXT NULL`)
	assert.Contains(t, create, `"created_at" DATETIME NULL`)
	assert.Contains(t, create, `"deleted_at" DATETIME NULL`)
}

func TestCreateUsersTable_Postgres(t *testing.T) {
	c := schema.NewCompiler(postgres.Grammar{})
	stmts, err := c.Compile(usersBlueprint())
	require.NoError(t, err)

	create := stmts[0].SQL
	assert.True(t, strings.HasPrefix(create, `CREATE TABLE "users"`))
	assert.Contains(t, create, `"id" BIGSERIAL`)
	assert.Contains(t, create, `"email" VARCHAR(255) NOT NULL UNIQUE`)
	assert.Contains(t, create, `"meta" JSON NULL`)
	assert.Contains(t, create, `"created_at" TIMESTAMP NULL`)
}

func TestCreateUsersTable_MySQL(t *testing.T) {
	c := schema.NewCompiler(mysql.Grammar{})
	stmts, err := c.Compile(usersBlueprint())
	require.NoError(t, err)

	create := stmts[0].SQL
	assert.True(t, strings.HasPrefix(create, "CREATE TABLE `users`"))
	assert.Contains(t, create, "`id` BIGINT UNSIGNED AUTO_INCREMENT NOT NULL PRIMARY KEY")
	assert.Contains(t, create, "`email` VARCHAR(255) NOT NULL UNIQUE")
	assert.Contains(t, create, "`is_admin` TINYINT(1) NOT NULL DEFAULT 0")
	assert.Contains(t, create, "`meta` JSON NULL")
}

func TestForeignIdConstrained(t *testing.T) {
	def := schema.Create("posts", func(t *schema.Blueprint) {
		t.ID()
		t.ForeignId("user_id").Constrained("users").OnDelete("cascade")
		t.String("title")
		t.Timestamps()
	})
	c := schema.NewCompiler(sqlite.Grammar{})
	stmts, err := c.Compile(def)
	require.NoError(t, err)

	create := stmts[0].SQL
	assert.Contains(t, create, `CONSTRAINT "fk_posts_user_id" FOREIGN KEY ("user_id") REFERENCES "users" ("id")`)
	assert.Contains(t, create, "ON DELETE CASCADE")
}

func TestForeignIdInfersTableName(t *testing.T) {
	def := schema.Create("posts", func(t *schema.Blueprint) {
		t.ID()
		t.ForeignId("company_id").Constrained() // no explicit table
	})
	stmts, err := schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	require.NoError(t, err)
	assert.Contains(t, stmts[0].SQL, `REFERENCES "companies"`,
		"expected company_id → companies via plural inference")
}

func TestEnumCompilation(t *testing.T) {
	def := schema.Create("orders", func(t *schema.Blueprint) {
		t.ID()
		t.Enum("status", "pending", "paid", "shipped")
	})
	mysqlStmts, _ := schema.NewCompiler(mysql.Grammar{}).Compile(def)
	assert.Contains(t, mysqlStmts[0].SQL, "ENUM('pending', 'paid', 'shipped')")

	pgStmts, _ := schema.NewCompiler(postgres.Grammar{}).Compile(def)
	assert.Contains(t, pgStmts[0].SQL, "CHECK (VALUE IN ('pending', 'paid', 'shipped'))")

	sqliteStmts, _ := schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	assert.Contains(t, sqliteStmts[0].SQL, `"status" TEXT NOT NULL`)
}

func TestUniqueAndCompositeIndex(t *testing.T) {
	def := schema.Create("memberships", func(t *schema.Blueprint) {
		t.ID()
		t.ForeignId("user_id").Constrained()
		t.ForeignId("team_id").Constrained()
		t.Unique("user_id", "team_id")
		t.AddIndex("team_id", "user_id")
	})
	stmts, err := schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	require.NoError(t, err)
	// One CREATE TABLE + two CREATE [UNIQUE] INDEX.
	require.Len(t, stmts, 3)
	assert.Contains(t, stmts[1].SQL, "CREATE UNIQUE INDEX")
	assert.Contains(t, stmts[2].SQL, "CREATE INDEX")
}

func TestDropTable(t *testing.T) {
	stmts, err := schema.NewCompiler(sqlite.Grammar{}).Compile(schema.Drop("widgets"))
	require.NoError(t, err)
	assert.Equal(t, `DROP TABLE "widgets"`, stmts[0].SQL)

	stmts, err = schema.NewCompiler(sqlite.Grammar{}).Compile(schema.DropIfExists("widgets"))
	require.NoError(t, err)
	assert.Equal(t, `DROP TABLE IF EXISTS "widgets"`, stmts[0].SQL)
}

func TestAlterTableAddColumn(t *testing.T) {
	def := schema.Table("users", func(t *schema.Blueprint) {
		t.String("nickname").Nullable()
	})
	stmts, err := schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	require.NoError(t, err)
	require.Len(t, stmts, 1)
	assert.Equal(t, `ALTER TABLE "users" ADD COLUMN "nickname" VARCHAR(255) NULL`, stmts[0].SQL)
}

func TestRenameTable(t *testing.T) {
	stmts, err := schema.NewCompiler(sqlite.Grammar{}).Compile(schema.Rename("old", "new"))
	require.NoError(t, err)
	assert.Equal(t, `ALTER TABLE "old" RENAME TO "new"`, stmts[0].SQL)
}

func TestDuplicateColumnRejected(t *testing.T) {
	def := schema.Create("bad", func(t *schema.Blueprint) {
		t.ID()
		t.String("name")
		t.Integer("name") // duplicate
	})
	_, err := schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate column")
}

func TestColumnTypeMatrix(t *testing.T) {
	// One simple sanity test that every column kind compiles to *something*
	// for every supported grammar — no silent fall-throughs.
	def := schema.Create("kitchen_sink", func(t *schema.Blueprint) {
		t.ID()
		t.UUID("uid")
		t.BigInteger("big")
		t.SmallInteger("small")
		t.TinyInteger("tiny")
		t.Float("f")
		t.Double("d")
		t.Decimal("price", 10, 2)
		t.Boolean("active")
		t.Char("code", 4)
		t.Text("body")
		t.LongText("long")
		t.Binary("blob")
		t.Date("on")
		t.DateTime("at")
		t.Time("clock")
		t.JSONB("payload")
		t.Set("tags", "a", "b")
	})
	for _, g := range []database.Grammar{sqlite.Grammar{}, postgres.Grammar{}, mysql.Grammar{}} {
		t.Run(g.Name(), func(t *testing.T) {
			stmts, err := schema.NewCompiler(g).Compile(def)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(stmts[0].SQL, "CREATE TABLE"))
		})
	}
}
