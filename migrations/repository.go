package migrations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devituz/lagodev/database"
)

// Record is a row in the migrations tracking table.
type Record struct {
	Migration string
	Batch     int
	Checksum  string
	AppliedAt time.Time
}

// Repository persists migration history.
type Repository struct {
	Conn    *database.Connection
	Table   string
}

// NewRepository builds a Repository targeting the given table.
func NewRepository(conn *database.Connection, table string) *Repository {
	if table == "" {
		table = "migrations"
	}
	return &Repository{Conn: conn, Table: table}
}

// Ensure creates the migrations table if it does not already exist.
func (r *Repository) Ensure(ctx context.Context) error {
	g := r.Conn.Grammar
	stmt := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		migration  VARCHAR(255) NOT NULL,
		batch      INTEGER NOT NULL,
		checksum   VARCHAR(64) NOT NULL DEFAULT '',
		applied_at %s NOT NULL,
		PRIMARY KEY (migration)
	)`, g.Quote(r.Table), g.CompileType("timestamp", database.ColumnTypeOptions{}))
	_, err := r.Conn.ExecContext(ctx, stmt)
	return err
}

// All returns every record sorted by application order (batch asc, migration asc).
func (r *Repository) All(ctx context.Context) ([]Record, error) {
	q := fmt.Sprintf(`SELECT migration, batch, checksum, applied_at FROM %s ORDER BY batch ASC, migration ASC`, r.Conn.Grammar.Quote(r.Table))
	rows, err := r.Conn.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var rec Record
		if err := rows.Scan(&rec.Migration, &rec.Batch, &rec.Checksum, &rec.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// LastBatch returns the highest batch number currently recorded, or 0.
func (r *Repository) LastBatch(ctx context.Context) (int, error) {
	q := fmt.Sprintf(`SELECT COALESCE(MAX(batch), 0) FROM %s`, r.Conn.Grammar.Quote(r.Table))
	var n int
	if err := r.Conn.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Insert records a successful migration application.
func (r *Repository) Insert(ctx context.Context, exec database.Executor, rec Record) error {
	g := r.Conn.Grammar
	q := fmt.Sprintf(`INSERT INTO %s (migration, batch, checksum, applied_at) VALUES (%s, %s, %s, %s)`,
		g.Quote(r.Table), g.Placeholder(1), g.Placeholder(2), g.Placeholder(3), g.Placeholder(4))
	_, err := exec.ExecContext(ctx, q, rec.Migration, rec.Batch, rec.Checksum, rec.AppliedAt)
	return err
}

// Delete removes a record by migration name.
func (r *Repository) Delete(ctx context.Context, exec database.Executor, name string) error {
	g := r.Conn.Grammar
	q := fmt.Sprintf(`DELETE FROM %s WHERE migration = %s`, g.Quote(r.Table), g.Placeholder(1))
	_, err := exec.ExecContext(ctx, q, name)
	return err
}

// Names returns the set of applied migration names as a map for O(1) lookup.
func (r *Repository) Names(ctx context.Context) (map[string]Record, error) {
	all, err := r.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Record, len(all))
	for _, rec := range all {
		out[rec.Migration] = rec
	}
	return out, nil
}

// ErrChecksumMismatch is returned when a recorded migration's checksum
// disagrees with the one computed from current source.
var ErrChecksumMismatch = errors.New("migrations: checksum mismatch — migration source changed after being applied")
