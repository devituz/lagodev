package orm_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/schema"
)

// LoadCounter is the model exercised by the concurrency/load test. It lives in
// its own table so the load suite never contends with the other ORM tests.
type LoadCounter struct {
	orm.Model
	Bucket int    `column:"bucket"`
	Name   string `column:"name"`
	Hits   int    `column:"hits"`
}

func (LoadCounter) TableName() string { return "load_counters" }

var loadRegistry = migrations.NewRegistry()

func init() {
	loadRegistry.Register(migrations.Define("00001_load_counters",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("load_counters", func(t *schema.Blueprint) {
				t.ID()
				t.Integer("bucket").Default(0)
				t.String("name")
				t.Integer("hits").Default(0)
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("load_counters"))
		},
	))
}

// loadSetup opens a temp-file SQLite database tuned for concurrent writers
// (WAL journal + a busy_timeout so writers serialize instead of erroring with
// SQLITE_BUSY). The default in-memory shared-cache harness is unsuitable for a
// write-heavy concurrency test, so the load suite manages its own connection.
func loadSetup(t *testing.T) (*database.Connection, func()) {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "load.db") +
		"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL"

	mgr := database.NewManager()
	conn, err := mgr.Open("load", database.Config{Driver: "sqlite", DSN: dsn})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	migrator := migrations.New(conn, loadRegistry, migrations.Options{})
	_, err = migrator.Up(ctx)
	require.NoError(t, err)

	return conn, func() { _ = mgr.Close() }
}

// TestLoad_ConcurrentReadsWritesTransactions hammers the ORM from many
// goroutines doing inserts, updates, reads and transactions simultaneously,
// then asserts the persisted state matches what was issued. Run with -race to
// catch data races in the shared builder/connection paths.
func TestLoad_ConcurrentReadsWritesTransactions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in -short mode")
	}
	conn, cleanup := loadSetup(t)
	defer cleanup()
	ctx := context.Background()

	const (
		writers          = 16
		insertsPerWriter = 80
		readers          = 8
		readsPerReader   = 200
		txWorkers        = 8
		txPerWorker      = 25
	)

	var (
		wg          sync.WaitGroup
		insertCount int64
		readCount   int64
		txCommitted int64
		firstErr    atomic.Value // error
	)
	fail := func(err error) {
		if err != nil {
			firstErr.CompareAndSwap(nil, err)
		}
	}

	start := time.Now()

	// Writers: plain inserts across distinct buckets.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < insertsPerWriter; i++ {
				c := &LoadCounter{
					Bucket: w,
					Name:   fmt.Sprintf("w%d-%d", w, i),
					Hits:   i,
				}
				if err := orm.Save(ctx, conn, c); err != nil {
					fail(fmt.Errorf("insert w%d-%d: %w", w, i, err))
					return
				}
				atomic.AddInt64(&insertCount, 1)
			}
		}(w)
	}

	// Readers: concurrent Count/Get/First against the growing table.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < readsPerReader; i++ {
				var rows []LoadCounter
				if err := orm.Query[LoadCounter](conn).
					Where("hits", ">=", 0).
					OrderBy("id", "desc").
					Limit(10).
					Get(ctx, &rows); err != nil {
					fail(fmt.Errorf("read get: %w", err))
					return
				}
				if _, err := orm.Query[LoadCounter](conn).Count(ctx); err != nil {
					fail(fmt.Errorf("read count: %w", err))
					return
				}
				atomic.AddInt64(&readCount, 1)
			}
		}()
	}

	// Transaction workers: each inserts a row inside a tx and commits.
	for tw := 0; tw < txWorkers; tw++ {
		wg.Add(1)
		go func(tw int) {
			defer wg.Done()
			for i := 0; i < txPerWorker; i++ {
				err := conn.Transaction(ctx, func(tx *database.Tx) error {
					id, err := orm.Query[LoadCounter](conn).WithTx(tx).QB().
						InsertGetID(ctx, map[string]any{
							"bucket":     1000 + tw,
							"name":       fmt.Sprintf("tx%d-%d", tw, i),
							"hits":       1,
							"created_at": conn.Now(),
							"updated_at": conn.Now(),
						}, "id")
					if err != nil {
						return err
					}
					if id == 0 {
						return fmt.Errorf("tx insert returned zero id")
					}
					return nil
				})
				if err != nil {
					fail(fmt.Errorf("tx %d-%d: %w", tw, i, err))
					return
				}
				atomic.AddInt64(&txCommitted, 1)
			}
		}(tw)
	}

	wg.Wait()
	elapsed := time.Since(start)

	if v := firstErr.Load(); v != nil {
		t.Fatalf("concurrent op failed: %v", v.(error))
	}

	wantInserts := int64(writers * insertsPerWriter)
	wantTx := int64(txWorkers * txPerWorker)
	require.Equal(t, wantInserts, insertCount, "all plain inserts accounted for")
	require.Equal(t, wantTx, txCommitted, "all transactions committed")
	require.Equal(t, int64(readers*readsPerReader), readCount)

	// The persisted row count must equal plain inserts + committed tx inserts.
	total, err := orm.Query[LoadCounter](conn).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, wantInserts+wantTx, total, "persisted rows match issued writes")

	// No connection leak: the pool must be drainable and reusable afterwards.
	require.NoError(t, conn.Ping(ctx))
	stats := conn.DB.Stats()
	require.Zero(t, stats.InUse, "no connection left checked out")

	ops := insertCount + readCount + txCommitted
	t.Logf("load: %d ops (%d inserts, %d reads, %d tx) in %s = %.0f ops/sec",
		ops, insertCount, readCount, txCommitted, elapsed.Round(time.Millisecond),
		float64(ops)/elapsed.Seconds())
}

// TestLoad_ConcurrentUpdatesSameRows checks that many goroutines updating a
// small shared set of rows produce a consistent final state with no lost
// connections. Each writer owns a disjoint row, so the final Hits value is
// deterministic even though all writers run concurrently.
func TestLoad_ConcurrentUpdatesSameRows(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in -short mode")
	}
	conn, cleanup := loadSetup(t)
	defer cleanup()
	ctx := context.Background()

	const rows = 12
	const bumps = 50

	seeded := make([]*LoadCounter, rows)
	for i := 0; i < rows; i++ {
		c := &LoadCounter{Bucket: 9000, Name: fmt.Sprintf("shared-%d", i), Hits: 0}
		require.NoError(t, orm.Save(ctx, conn, c))
		seeded[i] = c
	}

	var wg sync.WaitGroup
	var firstErr atomic.Value
	for i := 0; i < rows; i++ {
		wg.Add(1)
		go func(c *LoadCounter) {
			defer wg.Done()
			for b := 0; b < bumps; b++ {
				c.Hits++
				if err := orm.Save(ctx, conn, c); err != nil {
					firstErr.CompareAndSwap(nil, err)
					return
				}
			}
		}(seeded[i])
	}
	wg.Wait()
	if v := firstErr.Load(); v != nil {
		t.Fatalf("concurrent update failed: %v", v.(error))
	}

	for i := 0; i < rows; i++ {
		loaded, err := orm.Query[LoadCounter](conn).Find(ctx, seeded[i].ID)
		require.NoError(t, err)
		require.Equal(t, bumps, loaded.Hits, "row %d final hits", i)
	}

	stats := conn.DB.Stats()
	require.Zero(t, stats.InUse, "no connection left checked out")
}
