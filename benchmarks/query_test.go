package benchmarks

import (
	"testing"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/query"
)

// BenchmarkQuery_Complex exercises a heavier SQL composition path — joins,
// multiple wheres, IN clause, grouping and ordering — to capture builder
// overhead beyond the trivial case in BenchmarkBuilder_ToSQL. No DB I/O.
func BenchmarkQuery_Complex(b *testing.B) {
	conn := &database.Connection{Grammar: sqlite.Grammar{}}
	ids := []int{1, 2, 3, 4, 5}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = query.New(conn, "orders").
			Select("orders.id", "users.name", "orders.total").
			Join("users", "users.id = orders.user_id").
			Where("orders.total", ">", 100).
			WhereIn("orders.user_id", ids).
			WhereNotNull("orders.paid_at").
			GroupBy("orders.user_id").
			OrderBy("orders.total", "desc").
			Limit(25).
			ToSQL()
	}
}
