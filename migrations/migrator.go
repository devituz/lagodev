package migrations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/logger"
)

// Options configures Migrator behavior.
type Options struct {
	// Table overrides the migrations record table (default "migrations").
	Table string
	// Pretend renders SQL but does not execute it.
	Pretend bool
	// AllowOutOfOrder permits applying migrations whose names sort before a
	// previously applied migration. Off by default to mirror Laravel.
	AllowOutOfOrder bool
	// EnforceChecksums causes the migrator to refuse to run when a recorded
	// migration's checksum has drifted from current source.
	EnforceChecksums bool
	// LockTimeout for acquiring the migrations lock.
	LockTimeout time.Duration
	// Logger receives status messages.
	Logger *logger.Logger
}

// Migrator orchestrates migrations against a connection.
type Migrator struct {
	Conn     *database.Connection
	Registry *Registry
	Opts     Options
	Repo     *Repository
}

// New creates a Migrator. registry may be nil to use the package Default.
func New(conn *database.Connection, registry *Registry, opts Options) *Migrator {
	if registry == nil {
		registry = Default
	}
	if opts.Table == "" {
		opts.Table = conn.Config.MigrationsTable
	}
	return &Migrator{
		Conn:     conn,
		Registry: registry,
		Opts:     opts,
		Repo:     NewRepository(conn, opts.Table),
	}
}

func (m *Migrator) log() *logger.Logger {
	if m.Opts.Logger != nil {
		return m.Opts.Logger
	}
	if m.Conn.Log != nil {
		return m.Conn.Log
	}
	return logger.New(logger.LevelSilent)
}

// Up applies all pending migrations in a new batch. Returns the names applied.
func (m *Migrator) Up(ctx context.Context) ([]string, error) {
	return m.runUp(ctx, 0)
}

// Step applies up to n pending migrations.
func (m *Migrator) Step(ctx context.Context, n int) ([]string, error) {
	if n <= 0 {
		return nil, errors.New("migrations: step count must be > 0")
	}
	return m.runUp(ctx, n)
}

func (m *Migrator) runUp(ctx context.Context, limit int) ([]string, error) {
	if err := m.Repo.Ensure(ctx); err != nil {
		return nil, err
	}
	lock := NewLock(m.Conn, "migrator")
	if err := lock.Acquire(ctx, m.lockTimeout()); err != nil {
		return nil, err
	}
	defer lock.Release(ctx)

	applied, err := m.Repo.Names(ctx)
	if err != nil {
		return nil, err
	}
	if m.Opts.EnforceChecksums {
		if err := m.verifyChecksums(applied); err != nil {
			return nil, err
		}
	}

	all := m.Registry.All()
	pending := make([]Migration, 0, len(all))
	highestApplied := highestName(applied)
	for _, mig := range all {
		if _, done := applied[mig.Name()]; done {
			continue
		}
		if !m.Opts.AllowOutOfOrder && mig.Name() < highestApplied {
			return nil, fmt.Errorf("migrations: out-of-order migration %q (highest applied %q); pass AllowOutOfOrder to override", mig.Name(), highestApplied)
		}
		pending = append(pending, mig)
		if limit > 0 && len(pending) >= limit {
			break
		}
	}
	if len(pending) == 0 {
		m.log().Info("nothing to migrate")
		return nil, nil
	}

	batch, err := m.Repo.LastBatch(ctx)
	if err != nil {
		return nil, err
	}
	batch++

	var names []string
	for _, mig := range pending {
		if err := m.applyOne(ctx, mig, batch); err != nil {
			return names, err
		}
		names = append(names, mig.Name())
		m.log().Infof("migrated %s", mig.Name())
	}
	return names, nil
}

func (m *Migrator) applyOne(ctx context.Context, mig Migration, batch int) error {
	sum, err := Checksum(m.Conn, mig)
	if err != nil {
		return fmt.Errorf("migrations: checksum %s: %w", mig.Name(), err)
	}
	if m.Opts.Pretend {
		mctx := &Context{Conn: m.Conn, Ctx: ctx, preview: true}
		if err := mig.Up(mctx); err != nil {
			return err
		}
		for _, s := range mctx.CollectedSQL() {
			fmt.Println(s + ";")
		}
		return nil
	}
	return m.Conn.Transaction(ctx, func(tx *database.Tx) error {
		mctx := &Context{Conn: m.Conn, Tx: tx, Ctx: ctx}
		if err := mig.Up(mctx); err != nil {
			return fmt.Errorf("migrations: up %s: %w", mig.Name(), err)
		}
		return m.Repo.Insert(ctx, tx, Record{
			Migration: mig.Name(),
			Batch:     batch,
			Checksum:  sum,
			AppliedAt: Now(),
		})
	})
}

// Rollback reverses the last batch of migrations (or step) and returns the
// names reverted in application order.
func (m *Migrator) Rollback(ctx context.Context, step int) ([]string, error) {
	if err := m.Repo.Ensure(ctx); err != nil {
		return nil, err
	}
	lock := NewLock(m.Conn, "migrator")
	if err := lock.Acquire(ctx, m.lockTimeout()); err != nil {
		return nil, err
	}
	defer lock.Release(ctx)

	records, err := m.Repo.All(ctx)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}

	// Determine which records to roll back.
	sort.Slice(records, func(i, j int) bool {
		if records[i].Batch != records[j].Batch {
			return records[i].Batch > records[j].Batch
		}
		return records[i].Migration > records[j].Migration
	})
	var target []Record
	if step <= 0 {
		// Roll back the most recent batch only.
		lastBatch := records[0].Batch
		for _, rec := range records {
			if rec.Batch != lastBatch {
				break
			}
			target = append(target, rec)
		}
	} else {
		if step > len(records) {
			step = len(records)
		}
		target = records[:step]
	}

	var rolled []string
	for _, rec := range target {
		mig, ok := m.Registry.Get(rec.Migration)
		if !ok {
			return rolled, fmt.Errorf("migrations: rollback %s: migration not registered", rec.Migration)
		}
		if err := m.revertOne(ctx, mig); err != nil {
			return rolled, err
		}
		rolled = append(rolled, mig.Name())
		m.log().Infof("rolled back %s", mig.Name())
	}
	return rolled, nil
}

func (m *Migrator) revertOne(ctx context.Context, mig Migration) error {
	if m.Opts.Pretend {
		mctx := &Context{Conn: m.Conn, Ctx: ctx, preview: true}
		if err := mig.Down(mctx); err != nil {
			return err
		}
		for _, s := range mctx.CollectedSQL() {
			fmt.Println(s + ";")
		}
		return nil
	}
	return m.Conn.Transaction(ctx, func(tx *database.Tx) error {
		mctx := &Context{Conn: m.Conn, Tx: tx, Ctx: ctx}
		if err := mig.Down(mctx); err != nil {
			return fmt.Errorf("migrations: down %s: %w", mig.Name(), err)
		}
		return m.Repo.Delete(ctx, tx, mig.Name())
	})
}

// Reset rolls back every applied migration in reverse order.
func (m *Migrator) Reset(ctx context.Context) ([]string, error) {
	if err := m.Repo.Ensure(ctx); err != nil {
		return nil, err
	}
	records, err := m.Repo.All(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Batch != records[j].Batch {
			return records[i].Batch > records[j].Batch
		}
		return records[i].Migration > records[j].Migration
	})
	return m.Rollback(ctx, len(records))
}

// Refresh: reset + up.
func (m *Migrator) Refresh(ctx context.Context) ([]string, error) {
	if _, err := m.Reset(ctx); err != nil {
		return nil, err
	}
	return m.Up(ctx)
}

// Fresh drops all tables and re-applies all migrations from scratch. The
// list of tables is enumerated from each driver — sqlite_master /
// information_schema / pg_catalog — and excludes the migrations table.
func (m *Migrator) Fresh(ctx context.Context) ([]string, error) {
	if err := m.dropAllTables(ctx); err != nil {
		return nil, err
	}
	return m.Up(ctx)
}

// Status returns one StatusRow per known migration.
func (m *Migrator) Status(ctx context.Context) ([]StatusRow, error) {
	if err := m.Repo.Ensure(ctx); err != nil {
		return nil, err
	}
	applied, err := m.Repo.Names(ctx)
	if err != nil {
		return nil, err
	}
	all := m.Registry.All()
	out := make([]StatusRow, 0, len(all))
	for _, mig := range all {
		row := StatusRow{Name: mig.Name()}
		if rec, ok := applied[mig.Name()]; ok {
			row.Applied = true
			row.Batch = rec.Batch
			row.AppliedAt = rec.AppliedAt
			row.Checksum = rec.Checksum
		}
		out = append(out, row)
	}
	// Surface applied migrations not present in the registry (stale records).
	known := make(map[string]struct{}, len(all))
	for _, mig := range all {
		known[mig.Name()] = struct{}{}
	}
	for name, rec := range applied {
		if _, ok := known[name]; ok {
			continue
		}
		out = append(out, StatusRow{
			Name:      name,
			Applied:   true,
			Stale:     true,
			Batch:     rec.Batch,
			AppliedAt: rec.AppliedAt,
			Checksum:  rec.Checksum,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// StatusRow describes one entry in the status report.
type StatusRow struct {
	Name      string
	Applied   bool
	Stale     bool
	Batch     int
	AppliedAt time.Time
	Checksum  string
}

func (m *Migrator) verifyChecksums(applied map[string]Record) error {
	for name, rec := range applied {
		mig, ok := m.Registry.Get(name)
		if !ok {
			continue
		}
		sum, err := Checksum(m.Conn, mig)
		if err != nil {
			return fmt.Errorf("migrations: checksum %s: %w", name, err)
		}
		if rec.Checksum != "" && sum != rec.Checksum {
			return fmt.Errorf("%w: %s", ErrChecksumMismatch, name)
		}
	}
	return nil
}

func (m *Migrator) dropAllTables(ctx context.Context) error {
	tables, err := listTables(ctx, m.Conn)
	if err != nil {
		return err
	}
	for _, t := range tables {
		_, err := m.Conn.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", m.Conn.Grammar.Quote(t)))
		if err != nil {
			return fmt.Errorf("migrations: drop %s: %w", t, err)
		}
	}
	return nil
}

func (m *Migrator) lockTimeout() time.Duration {
	if m.Opts.LockTimeout > 0 {
		return m.Opts.LockTimeout
	}
	return 30 * time.Second
}

func highestName(applied map[string]Record) string {
	if len(applied) == 0 {
		return ""
	}
	names := make([]string, 0, len(applied))
	for n := range applied {
		names = append(names, n)
	}
	sort.Strings(names)
	return names[len(names)-1]
}

// listTables returns all user tables for the active driver. Implementation
// is driver-specific by name; we keep it here to avoid driver imports in
// the migrations package.
func listTables(ctx context.Context, conn *database.Connection) ([]string, error) {
	var query string
	switch conn.Grammar.Name() {
	case "sqlite":
		query = "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
	case "postgres":
		query = "SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = 'public'"
	case "mysql":
		query = "SELECT TABLE_NAME FROM information_schema.tables WHERE table_schema = DATABASE()"
	default:
		return nil, fmt.Errorf("migrations: unsupported driver %q for fresh", conn.Grammar.Name())
	}
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if strings.EqualFold(name, "migrations_lock") {
			// Keep the lock table alive while we work.
			continue
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
