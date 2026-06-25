package query_test

import (
	"testing"

	"github.com/devituz/lagodev/drivers/postgres"
	"github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/query"
)

// FuzzBuilder drives the SAFE (non-Raw) API with fuzzed identifiers and values
// and asserts two invariants for both grammars:
//  1. ToSQL never panics.
//  2. The number of placeholders emitted equals the number of bound args.
//
// Only methods that bind values through placeholders are exercised here; the
// Raw escape hatches (WhereRaw/OrderByRaw) are excluded by contract.
func FuzzBuilder(f *testing.F) {
	// Seed corpus: legitimate inputs plus injection-shaped ones.
	seeds := []struct {
		col, op, val, in1, in2 string
		lo, hi                 string
		limit, offset          int
	}{
		{"name", "=", "Ada", "1", "2", "a", "z", 10, 0},
		{"id", "like", "' OR 1=1 --", "x'--", "y", "0", "9", 0, 5},
		{`c"olumn`, "<>", "\x00", "", "", "", "", 0, 0},
		{"日本", ">=", "café", "値1", "値2", "α", "ω", 100, 50},
		{"", "", "", "", "", "", "", -1, -1},
		{"a.b", "ilike", "%x%", ";DROP", "TABLE", "lo", "hi", 1, 1},
	}
	for _, s := range seeds {
		f.Add(s.col, s.op, s.val, s.in1, s.in2, s.lo, s.hi, s.limit, s.offset)
	}

	// validOps mirrors the builder's operator whitelist (normalizeOp). The
	// fuzzer maps the fuzzed op string onto one of these so it always drives a
	// supported operator: an unsupported operator is REJECTED by design (it
	// panics, see TestInjection_OperatorWhitelist), which is correct behaviour
	// and not the panic this fuzzer is hunting for.
	validOps := []string{"=", "<>", "<", "<=", ">", ">=", "like", "ilike"}

	f.Fuzz(func(t *testing.T,
		col, op, val, in1, in2, lo, hi string,
		limit, offset int,
	) {
		// Deterministically fold the fuzzed op onto a supported one.
		sum := 0
		for _, r := range op {
			sum += int(r)
		}
		safeOp := validOps[sum%len(validOps)]

		check := func(pg bool) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ToSQL panicked (pg=%v) col=%q op=%q val=%q: %v",
						pg, col, op, val, r)
				}
			}()

			var b *query.Builder
			if pg {
				b = query.New(conn(postgres.Grammar{}), "t")
			} else {
				b = query.New(conn(sqlite.Grammar{}), "t")
			}

			// Build through the safe API only, with a whitelisted operator so
			// the placeholder/args invariant is the property under test (the
			// operator whitelist itself is covered by an explicit unit test).
			b = b.
				Where(col, safeOp, val).
				WhereIn(col, []string{in1, in2}).
				WhereBetween(col, lo, hi).
				WhereNull(col).
				WhereNotNull(col).
				GroupBy(col).
				Having(col, "=", val).
				Limit(limit).
				Offset(offset)

			sql, args, err := b.ToSQL()
			if err != nil {
				// An error return (e.g. empty table) is acceptable; table is
				// always set here so we don't expect one, but tolerate it.
				return
			}
			if got := countPlaceholders(sql, pg); got != len(args) {
				t.Fatalf("placeholder/args mismatch (pg=%v): %d placeholders vs %d args\nsql=%q\nargs=%v",
					pg, got, len(args), sql, args)
			}
		}

		check(false)
		check(true)
	})
}
