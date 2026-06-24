package search

import (
	"context"
	"database/sql"
	"encoding/json"
)

// decodeJSONFields unmarshals a JSON/JSONB column payload into dst. An empty
// payload leaves dst untouched (nil map).
func decodeJSONFields(data []byte, dst *map[string]any) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, dst)
}

// sqlDB adapts a *sql.DB to the engine's querier interface so NewPostgres can be
// fed a real connection while remaining testable against a fake. It bridges the
// concrete *sql.Rows/*sql.Result to the rows/result interfaces.
type sqlDB struct {
	db *sql.DB
}

// WrapDB adapts a standard *sql.DB to the querier expected by NewPostgres.
func WrapDB(db *sql.DB) querier { return sqlDB{db: db} }

func (s sqlDB) QueryContext(ctx context.Context, query string, args ...any) (rows, error) {
	return s.db.QueryContext(ctx, query, args...)
}

func (s sqlDB) ExecContext(ctx context.Context, query string, args ...any) (result, error) {
	return s.db.ExecContext(ctx, query, args...)
}
