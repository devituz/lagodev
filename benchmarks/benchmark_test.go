// Package benchmarks holds micro-benchmarks for the hot paths in lagodev.
// Run with `go test -bench=. ./benchmarks` (add -benchmem to see allocs).
package benchmarks

import (
	"context"
	"testing"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/drivers/sqlite"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/query"
	"github.com/devituz/lagodev/schema"
)

type Widget struct {
	orm.Model
	Name  string
	Price int
}

var benchRegistry = migrations.NewRegistry()

func init() {
	benchRegistry.Register(migrations.Define("00001_widgets",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("widgets", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.Integer("price")
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("widgets"))
		},
	))
}

func openConn(b *testing.B) *database.Connection {
	mgr := database.NewManager()
	conn, err := mgr.Open("bench", database.Config{
		Driver: "sqlite",
		DSN:    "file::memory:?cache=shared",
	})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := migrations.New(conn, benchRegistry, migrations.Options{}).Up(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = mgr.Close() })
	return conn
}

// BenchmarkBuilder_ToSQL exercises the SQL composition hot path without any
// database I/O — the realistic upper bound on builder throughput.
func BenchmarkBuilder_ToSQL(b *testing.B) {
	conn := &database.Connection{Grammar: sqlite.Grammar{}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = query.New(conn, "widgets").
			Where("price", ">", 100).
			Where("name", "like", "%foo%").
			OrderBy("price", "desc").
			Limit(50).
			ToSQL()
	}
}

// BenchmarkInsert measures end-to-end Save() including reflection cache hits.
func BenchmarkInsert(b *testing.B) {
	conn := openConn(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := &Widget{Name: "x", Price: i}
		if err := orm.Save(ctx, conn, w); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBatchInsert demonstrates the throughput improvement of batching
// over single-row inserts. With realistic transaction cost, batching is a
// 10-100× speedup.
func BenchmarkBatchInsert(b *testing.B) {
	conn := openConn(b)
	ctx := context.Background()
	rows := make([]map[string]any, 100)
	for i := range rows {
		rows[i] = map[string]any{"name": "x", "price": i, "created_at": "2026-01-01", "updated_at": "2026-01-01"}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := query.New(conn, "widgets").InsertBatch(ctx, rows)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSelectAll measures a full-table scan + hydration.
func BenchmarkSelectAll(b *testing.B) {
	conn := openConn(b)
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		_ = orm.Save(ctx, conn, &Widget{Name: "x", Price: i})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out []Widget
		if err := orm.Query[Widget](conn).Get(ctx, &out); err != nil {
			b.Fatal(err)
		}
	}
}
