package schema_test

import (
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
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
}

func TestCreateUsersTable_Postgres(t *testing.T) {
	c := schema.NewCompiler(postgres.Grammar{})
	stmts, err := c.Compile(usersBlueprint())
	require.NoError(t, err)

	create := stmts[0].SQL
	assert.True(t, strings.HasPrefix(create, `CREATE TABLE "users"`))
	assert.Contains(t, create, `"id" BIGSERIAL NOT NULL PRIMARY KEY`)
	assert.Contains(t, create, `"email" VARCHAR(255) NOT NULL UNIQUE`)
	assert.Contains(t, create, `"is_admin" BOOLEAN NOT NULL DEFAULT FALSE`)
	assert.Contains(t, create, `"meta" JSON NULL`)
	assert.Contains(t, create, `"created_at" TIMESTAMP NULL`)
}

// TestPrimaryKeyAcrossDialects locks issues #1 and #2: a single-column ID()
// must produce a real PRIMARY KEY on every dialect (so FKs can reference it),
// and adding a redundant Primary("id") must not emit the PK twice.
func TestPrimaryKeyAcrossDialects(t *testing.T) {
	grammars := []database.Grammar{sqlite.Grammar{}, postgres.Grammar{}, mysql.Grammar{}}

	t.Run("single ID is primary key", func(t *testing.T) {
		def := schema.Create("regions", func(t *schema.Blueprint) {
			t.ID()
			t.JSONB("name")
		})
		for _, g := range grammars {
			stmts, err := schema.NewCompiler(g).Compile(def)
			require.NoError(t, err)
			create := stmts[0].SQL
			assert.Equal(t, 1, strings.Count(create, "PRIMARY KEY"),
				"[%s] expected exactly one PRIMARY KEY: %s", g.Name(), create)
		}
	})

	t.Run("ID plus redundant Primary does not double", func(t *testing.T) {
		def := schema.Create("districts", func(t *schema.Blueprint) {
			t.ID()
			t.Primary("id")
			t.UnsignedBigInteger("region_id")
		})
		for _, g := range grammars {
			stmts, err := schema.NewCompiler(g).Compile(def)
			require.NoError(t, err)
			create := stmts[0].SQL
			assert.Equal(t, 1, strings.Count(create, "PRIMARY KEY"),
				"[%s] ID()+Primary(\"id\") must yield one PK: %s", g.Name(), create)
		}
	})

	t.Run("composite primary key is table-level", func(t *testing.T) {
		def := schema.Create("memberships", func(t *schema.Blueprint) {
			t.UnsignedBigInteger("user_id")
			t.UnsignedBigInteger("team_id")
			t.Primary("user_id", "team_id")
		})
		for _, g := range grammars {
			stmts, err := schema.NewCompiler(g).Compile(def)
			require.NoError(t, err)
			create := stmts[0].SQL
			assert.Contains(t, create, "PRIMARY KEY (",
				"[%s] composite PK must be table-level: %s", g.Name(), create)
			assert.Equal(t, 1, strings.Count(create, "PRIMARY KEY"),
				"[%s] composite PK emitted once: %s", g.Name(), create)
		}
	})
}

// TestForeignKeyReferencesPrimaryKey reproduces the regions/districts FK case
// from issue #1: the referenced single-column PK must exist for the FK to be
// valid on Postgres.
func TestForeignKeyReferencesPrimaryKey(t *testing.T) {
	def := schema.Create("districts", func(t *schema.Blueprint) {
		t.ID()
		t.UnsignedBigInteger("region_id")
		t.Foreign("region_id").References("id").On("regions")
	})
	for _, g := range []database.Grammar{sqlite.Grammar{}, postgres.Grammar{}, mysql.Grammar{}} {
		stmts, err := schema.NewCompiler(g).Compile(def)
		require.NoError(t, err)
		create := stmts[0].SQL
		assert.Contains(t, create, "PRIMARY KEY", "[%s] %s", g.Name(), create)
		assert.Contains(t, create, "FOREIGN KEY", "[%s] %s", g.Name(), create)
	}
}

// TestBooleanDefaultPerDialect locks issue #3: boolean defaults must render as
// TRUE/FALSE on Postgres and 1/0 on MySQL/SQLite.
func TestBooleanDefaultPerDialect(t *testing.T) {
	def := schema.Create("flags", func(t *schema.Blueprint) {
		t.ID()
		t.Boolean("is_active").Default(true)
		t.Boolean("is_deleted").Default(false)
	})

	pg, err := schema.NewCompiler(postgres.Grammar{}).Compile(def)
	require.NoError(t, err)
	assert.Contains(t, pg[0].SQL, `"is_active" BOOLEAN NOT NULL DEFAULT TRUE`)
	assert.Contains(t, pg[0].SQL, `"is_deleted" BOOLEAN NOT NULL DEFAULT FALSE`)

	sq, err := schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	require.NoError(t, err)
	assert.Contains(t, sq[0].SQL, `"is_active" BOOLEAN NOT NULL DEFAULT 1`)
	assert.Contains(t, sq[0].SQL, `"is_deleted" BOOLEAN NOT NULL DEFAULT 0`)

	my, err := schema.NewCompiler(mysql.Grammar{}).Compile(def)
	require.NoError(t, err)
	assert.Contains(t, my[0].SQL, "`is_active` TINYINT(1) NOT NULL DEFAULT 1")
	assert.Contains(t, my[0].SQL, "`is_deleted` TINYINT(1) NOT NULL DEFAULT 0")
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
	// Postgres stores enums as VARCHAR with a table-level CHECK on the column
	// name — "VALUE IN (...)" is invalid outside CREATE DOMAIN.
	assert.Contains(t, pgStmts[0].SQL, `"status" VARCHAR(255) NOT NULL`)
	assert.Contains(t, pgStmts[0].SQL, `CHECK ("status" IN ('pending', 'paid', 'shipped'))`)
	assert.NotContains(t, pgStmts[0].SQL, "VALUE IN")

	sqliteStmts, _ := schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	assert.Contains(t, sqliteStmts[0].SQL, `"status" TEXT NOT NULL`)
	assert.Contains(t, sqliteStmts[0].SQL, `CHECK ("status" IN ('pending', 'paid', 'shipped'))`)
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

// TestEnumCheckIsValidSQL locks bug #1/#6: Postgres/SQLite enum CHECK must
// reference the column name, not the CREATE-DOMAIN-only "VALUE" keyword, and
// SQLite must constrain enum/set values too. It executes against real SQLite to
// prove the CHECK is enforced.
func TestEnumCheckIsValidSQL(t *testing.T) {
	def := schema.Create("tickets", func(t *schema.Blueprint) {
		t.ID()
		t.Enum("status", "open", "closed")
		t.Set("tags", "a", "b")
	})

	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	stmts, err := schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	require.NoError(t, err)
	for _, s := range stmts {
		_, err := db.Exec(s.SQL)
		require.NoErrorf(t, err, "exec failed: %s", s.SQL)
	}
	_, err = db.Exec(`INSERT INTO "tickets" ("status", "tags") VALUES ('open', 'a')`)
	require.NoError(t, err, "valid enum value accepted")
	_, err = db.Exec(`INSERT INTO "tickets" ("status", "tags") VALUES ('bogus', 'a')`)
	require.Error(t, err, "invalid enum value rejected by CHECK")

	// Postgres: VARCHAR + table-level CHECK on the column name (never VALUE).
	pg, err := schema.NewCompiler(postgres.Grammar{}).Compile(def)
	require.NoError(t, err)
	assert.Contains(t, pg[0].SQL, `CHECK ("status" IN ('open', 'closed'))`)
	assert.NotContains(t, pg[0].SQL, "VALUE IN")

	// MySQL keeps native ENUM/SET — no CHECK.
	my, err := schema.NewCompiler(mysql.Grammar{}).Compile(def)
	require.NoError(t, err)
	assert.Contains(t, my[0].SQL, "ENUM('open', 'closed')")
	assert.NotContains(t, my[0].SQL, "CHECK")
}

// TestDropForeignPerDialect locks bug #2: FK drop syntax differs per dialect and
// SQLite is unsupported.
func TestDropForeignPerDialect(t *testing.T) {
	def := schema.Table("posts", func(t *schema.Blueprint) {
		t.DropForeign("fk_posts_user_id")
	})

	pg, err := schema.NewCompiler(postgres.Grammar{}).Compile(def)
	require.NoError(t, err)
	assert.Equal(t, `ALTER TABLE "posts" DROP CONSTRAINT "fk_posts_user_id"`, pg[0].SQL)

	my, err := schema.NewCompiler(mysql.Grammar{}).Compile(def)
	require.NoError(t, err)
	assert.Equal(t, "ALTER TABLE `posts` DROP FOREIGN KEY `fk_posts_user_id`", my[0].SQL)

	_, err = schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	require.Error(t, err, "SQLite FK drop is unsupported")
	assert.Contains(t, err.Error(), "table rebuild")
}

// TestDropIndexPerDialect locks bug #3: MySQL needs "ON <table>".
func TestDropIndexPerDialect(t *testing.T) {
	def := schema.Table("posts", func(t *schema.Blueprint) {
		t.DropIndex("posts_title_index")
	})

	my, err := schema.NewCompiler(mysql.Grammar{}).Compile(def)
	require.NoError(t, err)
	assert.Equal(t, "DROP INDEX `posts_title_index` ON `posts`", my[0].SQL)

	for _, g := range []database.Grammar{sqlite.Grammar{}, postgres.Grammar{}} {
		stmts, err := schema.NewCompiler(g).Compile(def)
		require.NoError(t, err)
		assert.Equal(t, "DROP INDEX "+g.Quote("posts_title_index"), stmts[0].SQL,
			"[%s] bare DROP INDEX", g.Name())
	}
}

// TestSingleColumnIndexEmitted locks bug #4: col.Index() must emit a CREATE
// INDEX statement.
func TestSingleColumnIndexEmitted(t *testing.T) {
	def := schema.Create("posts", func(t *schema.Blueprint) {
		t.ID()
		t.String("slug").Index()
	})
	stmts, err := schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	require.NoError(t, err)
	require.Len(t, stmts, 2)
	assert.Equal(t, `CREATE INDEX "posts_slug_index" ON "posts" ("slug")`, stmts[1].SQL)
}

// TestAlterAddUniqueColumnUsesIndex locks bug #5: ADD COLUMN must not inline
// UNIQUE (SQLite rejects it); a separate CREATE UNIQUE INDEX is emitted. Runs on
// a real SQLite engine.
func TestAlterAddUniqueColumnUsesIndex(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	create := schema.Create("users", func(t *schema.Blueprint) {
		t.ID()
		t.String("name")
	})
	cs, err := schema.NewCompiler(sqlite.Grammar{}).Compile(create)
	require.NoError(t, err)
	for _, s := range cs {
		_, err := db.Exec(s.SQL)
		require.NoError(t, err)
	}

	alter := schema.Table("users", func(t *schema.Blueprint) {
		t.String("email").Unique()
	})
	stmts, err := schema.NewCompiler(sqlite.Grammar{}).Compile(alter)
	require.NoError(t, err)
	require.Len(t, stmts, 2)
	assert.NotContains(t, stmts[0].SQL, "UNIQUE", "ADD COLUMN must not inline UNIQUE")
	assert.Contains(t, stmts[1].SQL, "CREATE UNIQUE INDEX")
	for _, s := range stmts {
		_, err := db.Exec(s.SQL)
		require.NoErrorf(t, err, "exec failed: %s", s.SQL)
	}
	// UNIQUE is enforced.
	_, err = db.Exec(`INSERT INTO "users" ("name", "email") VALUES ('a', 'x@y.z')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO "users" ("name", "email") VALUES ('b', 'x@y.z')`)
	require.Error(t, err, "duplicate email rejected by unique index")
}

// TestGeneratedIndexNameCollision locks bug #6: two same-type indexes over the
// same columns must get distinct generated names.
func TestGeneratedIndexNameCollision(t *testing.T) {
	def := schema.Create("t", func(t *schema.Blueprint) {
		t.ID()
		t.String("a")
		t.AddIndex("a")
		t.AddIndex("a")
	})
	stmts, err := schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	require.NoError(t, err)
	require.Len(t, stmts, 3)
	assert.Contains(t, stmts[1].SQL, `"t_a_index"`)
	assert.Contains(t, stmts[2].SQL, `"t_a_index_2"`)
}

// TestIndexAndForeignNameSetters locks bug #6's fluent setters.
func TestIndexAndForeignNameSetters(t *testing.T) {
	def := schema.Create("posts", func(t *schema.Blueprint) {
		t.ID()
		t.UnsignedBigInteger("user_id")
		t.AddIndex("user_id").Name("idx_custom")
		t.Foreign("user_id").References("id").On("users").Name("fk_custom")
	})
	stmts, err := schema.NewCompiler(sqlite.Grammar{}).Compile(def)
	require.NoError(t, err)
	assert.Contains(t, stmts[0].SQL, `CONSTRAINT "fk_custom"`)
	assert.Contains(t, stmts[1].SQL, `"idx_custom"`)
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
