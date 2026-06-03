// Package sqlqueue implements a database-backed Queue driver that
// survives process restarts and works across replicas. It plugs into
// the parent queue package via the Queue interface.
//
// The driver uses a single `jobs` table; the schema is dialect-aware
// and ships its own migration via Setup(ctx). Pop selects the next
// available row with `... ORDER BY available_at LIMIT 1` and updates
// it to `reserved_at = now()` in a transaction so concurrent workers
// don't claim the same job.
//
// Usage:
//
//	q, err := sqlqueue.New(conn)            // uses the project's
//	if err != nil { return err }            // database.Connection
//	if err := q.Setup(ctx); err != nil { ... } // creates the table
//	queue.Dispatch(ctx, q, SendEmail{...})
//	worker := queue.NewWorker(q)
//	go worker.Run(ctx)
package sqlqueue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/queue"
)

// Queue is a database-backed queue. Safe for concurrent use across
// goroutines and processes.
type Queue struct {
	conn    *database.Connection
	table   string
	visTout time.Duration
}

// Option customises a Queue.
type Option func(*Queue)

// WithTable overrides the table name (default "jobs").
func WithTable(name string) Option { return func(q *Queue) { q.table = name } }

// WithVisibilityTimeout sets how long a reserved-but-not-acked job
// stays invisible. Default 5 minutes — pick a value larger than the
// longest handler runtime.
func WithVisibilityTimeout(d time.Duration) Option {
	return func(q *Queue) { q.visTout = d }
}

// New constructs a Queue.
func New(conn *database.Connection, opts ...Option) (*Queue, error) {
	if conn == nil {
		return nil, errors.New("sqlqueue: nil connection")
	}
	q := &Queue{conn: conn, table: "jobs", visTout: 5 * time.Minute}
	for _, o := range opts {
		o(q)
	}
	return q, nil
}

// Setup creates the jobs table if it doesn't already exist. Safe to
// call repeatedly (CREATE TABLE IF NOT EXISTS).
func (q *Queue) Setup(ctx context.Context) error {
	stmts := schemaForGrammar(q.conn.Grammar.Name(), q.table)
	for _, s := range stmts {
		if _, err := q.conn.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("sqlqueue: create schema: %w", err)
		}
	}
	return nil
}

func schemaForGrammar(name, table string) []string {
	// id INTEGER PK AUTOINCREMENT pattern differs per dialect.
	switch name {
	case "postgres":
		return []string{fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id BIGSERIAL PRIMARY KEY,
			job_id TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			payload BYTEA NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			available_at TIMESTAMPTZ NOT NULL,
			reserved_at TIMESTAMPTZ
		)`, table),
			fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_avail_idx ON %s (available_at) WHERE reserved_at IS NULL`, table, table),
		}
	case "mysql":
		return []string{fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			job_id VARCHAR(128) UNIQUE NOT NULL,
			name VARCHAR(255) NOT NULL,
			payload LONGBLOB NOT NULL,
			attempts INT NOT NULL DEFAULT 0,
			available_at DATETIME(6) NOT NULL,
			reserved_at DATETIME(6) NULL,
			INDEX %s_avail_idx (available_at)
		)`, table, table)}
	default: // sqlite, etc.
		return []string{fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			payload BLOB NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			available_at TIMESTAMP NOT NULL,
			reserved_at TIMESTAMP
		)`, table),
			fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_avail_idx ON %s (available_at)`, table, table),
		}
	}
}

// Push inserts j into the queue.
func (q *Queue) Push(ctx context.Context, j queue.Job) error {
	avail := j.AvailableAt
	if avail.IsZero() {
		avail = time.Now()
	}
	g := q.conn.Grammar
	stmt := fmt.Sprintf(
		"INSERT INTO %s (job_id, name, payload, attempts, available_at) VALUES (%s, %s, %s, %s, %s)",
		q.table, g.Placeholder(1), g.Placeholder(2), g.Placeholder(3), g.Placeholder(4), g.Placeholder(5),
	)
	_, err := q.conn.ExecContext(ctx, stmt, j.ID, j.Name, j.Payload, j.Attempts, avail)
	if err != nil {
		return fmt.Errorf("sqlqueue: insert: %w", err)
	}
	return nil
}

// Pop selects the next available job within wait. Reserves it via
// transactional update so concurrent workers cannot pick it up.
func (q *Queue) Pop(ctx context.Context, wait time.Duration) (queue.Job, error) {
	deadline := time.Now().Add(wait)
	for {
		j, err := q.tryReserve(ctx)
		if err == nil {
			return j, nil
		}
		if !errors.Is(err, queue.ErrEmpty) {
			return queue.Job{}, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return queue.Job{}, queue.ErrEmpty
		}
		sleep := 100 * time.Millisecond
		if remaining < sleep {
			sleep = remaining
		}
		select {
		case <-ctx.Done():
			return queue.Job{}, ctx.Err()
		case <-time.After(sleep):
		}
	}
}

func (q *Queue) tryReserve(ctx context.Context) (queue.Job, error) {
	// Promote orphaned reservations whose visibility window has
	// elapsed without an Ack/Nack — they're considered lost and become
	// eligible again.
	q.recoverOrphans(ctx)

	tx, err := q.conn.DB.BeginTx(ctx, nil)
	if err != nil {
		return queue.Job{}, fmt.Errorf("sqlqueue: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	g := q.conn.Grammar
	now := time.Now()
	// Use FOR UPDATE SKIP LOCKED on Postgres/MySQL when available;
	// SQLite serialises writes by default so plain SELECT is fine.
	var lockClause string
	switch g.Name() {
	case "postgres", "mysql":
		lockClause = " FOR UPDATE SKIP LOCKED"
	}
	selectSQL := fmt.Sprintf(
		"SELECT id, job_id, name, payload, attempts FROM %s WHERE reserved_at IS NULL AND available_at <= %s ORDER BY available_at LIMIT 1%s",
		q.table, g.Placeholder(1), lockClause,
	)
	row := tx.QueryRowContext(ctx, selectSQL, now)

	var (
		id       int64
		jobID    string
		name     string
		payload  []byte
		attempts int
	)
	if err := row.Scan(&id, &jobID, &name, &payload, &attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return queue.Job{}, queue.ErrEmpty
		}
		return queue.Job{}, fmt.Errorf("sqlqueue: scan: %w", err)
	}
	updateSQL := fmt.Sprintf(
		"UPDATE %s SET reserved_at = %s WHERE id = %s",
		q.table, g.Placeholder(1), g.Placeholder(2),
	)
	if _, err := tx.ExecContext(ctx, updateSQL, now, id); err != nil {
		return queue.Job{}, fmt.Errorf("sqlqueue: reserve: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return queue.Job{}, fmt.Errorf("sqlqueue: commit: %w", err)
	}
	return queue.Job{
		ID:       jobID,
		Name:     name,
		Payload:  payload,
		Attempts: attempts,
	}, nil
}

// recoverOrphans clears reserved_at on jobs whose worker died without
// Ack/Nack. They become eligible for re-delivery.
func (q *Queue) recoverOrphans(ctx context.Context) {
	cutoff := time.Now().Add(-q.visTout)
	g := q.conn.Grammar
	stmt := fmt.Sprintf(
		"UPDATE %s SET reserved_at = NULL WHERE reserved_at IS NOT NULL AND reserved_at < %s",
		q.table, g.Placeholder(1),
	)
	_, _ = q.conn.ExecContext(ctx, stmt, cutoff)
}

// Ack deletes the job.
func (q *Queue) Ack(ctx context.Context, jobID string) error {
	g := q.conn.Grammar
	stmt := fmt.Sprintf("DELETE FROM %s WHERE job_id = %s", q.table, g.Placeholder(1))
	if _, err := q.conn.ExecContext(ctx, stmt, jobID); err != nil {
		return fmt.Errorf("sqlqueue: ack: %w", err)
	}
	return nil
}

// Nack returns the job to the queue, incrementing attempts. If
// retryAfter > 0 the job stays invisible until that time.
func (q *Queue) Nack(ctx context.Context, jobID string, retryAfter time.Duration) error {
	g := q.conn.Grammar
	avail := time.Now()
	if retryAfter > 0 {
		avail = avail.Add(retryAfter)
	}
	stmt := fmt.Sprintf(
		"UPDATE %s SET reserved_at = NULL, attempts = attempts + 1, available_at = %s WHERE job_id = %s",
		q.table, g.Placeholder(1), g.Placeholder(2),
	)
	if _, err := q.conn.ExecContext(ctx, stmt, avail, jobID); err != nil {
		return fmt.Errorf("sqlqueue: nack: %w", err)
	}
	return nil
}

// Len returns the row count of the jobs table (cheap on SQLite, OK on
// other dialects under modest sizes; for production metrics prefer a
// time-bucketed query).
func (q *Queue) Len() int {
	row := q.conn.QueryRowContext(context.Background(), fmt.Sprintf("SELECT COUNT(*) FROM %s", q.table))
	var n int
	_ = row.Scan(&n)
	return n
}

// Static compile-time assertion: *Queue satisfies queue.Queue.
var _ queue.Queue = (*Queue)(nil)
