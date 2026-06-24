package database

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Config describes one named database connection.
type Config struct {
	// Driver is the name of a registered driver: "postgres", "mysql", "sqlite".
	Driver string `json:"driver"`
	// DSN is a fully-formed connection string. If non-empty it takes precedence
	// over the field-based options below.
	DSN string `json:"dsn,omitempty"`

	Host     string            `json:"host,omitempty"`
	Port     int               `json:"port,omitempty"`
	Username string            `json:"username,omitempty"`
	Password string            `json:"password,omitempty"`
	Database string            `json:"database,omitempty"`
	Schema   string            `json:"schema,omitempty"`
	SSLMode  string            `json:"ssl_mode,omitempty"`
	TimeZone string            `json:"timezone,omitempty"`
	Params   map[string]string `json:"params,omitempty"`

	// Pool settings.
	MaxOpenConns    int           `json:"max_open_conns,omitempty"`
	MaxIdleConns    int           `json:"max_idle_conns,omitempty"`
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time,omitempty"`
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime,omitempty"`

	// Logging.
	LogQueries      bool          `json:"log_queries,omitempty"`
	SlowQuery       time.Duration `json:"slow_query,omitempty"`
	TablePrefix     string        `json:"table_prefix,omitempty"`
	MigrationsTable string        `json:"migrations_table,omitempty"`
}

// BuildDSN returns the DSN derived from the structured fields. Drivers may
// override this through their own grammar; this is the framework's default
// behavior and is safe for the common case.
func (c Config) BuildDSN() (string, error) {
	if c.DSN != "" {
		return c.DSN, nil
	}
	switch strings.ToLower(c.Driver) {
	case "postgres", "postgresql", "pgx":
		return c.buildPostgresDSN(), nil
	case "mysql", "mariadb":
		return c.buildMySQLDSN(), nil
	case "sqlite", "sqlite3":
		return c.buildSQLiteDSN(), nil
	default:
		return "", fmt.Errorf("database: unknown driver %q", c.Driver)
	}
}

func (c Config) buildPostgresDSN() string {
	u := &url.URL{Scheme: "postgres"}
	if c.Username != "" {
		if c.Password != "" {
			u.User = url.UserPassword(c.Username, c.Password)
		} else {
			u.User = url.User(c.Username)
		}
	}
	host := c.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := c.Port
	if port == 0 {
		port = 5432
	}
	u.Host = fmt.Sprintf("%s:%d", host, port)
	u.Path = "/" + c.Database

	q := u.Query()
	if c.SSLMode != "" {
		q.Set("sslmode", c.SSLMode)
	} else {
		q.Set("sslmode", "disable")
	}
	if c.TimeZone != "" {
		q.Set("timezone", c.TimeZone)
	}
	if c.Schema != "" {
		q.Set("search_path", c.Schema)
	}
	for k, v := range c.Params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// buildSQLiteDSN injects time-zone and date parsing query parameters
// understood by mattn/go-sqlite3 so timestamps round-trip with the
// expected location. Params already present in c.Database win.
func (c Config) buildSQLiteDSN() string {
	base := c.Database
	if base == "" {
		base = "file::memory:?cache=shared"
	}
	// Don't double-set params the caller already provided.
	if !strings.Contains(base, "_loc=") {
		base = appendQueryParam(base, "_loc", sqliteLocation(c.TimeZone))
	}
	if !strings.Contains(base, "parseTime=") && !strings.Contains(base, "_time_format=") {
		// NOTE: mattn/go-sqlite3 has no "_time_format" DSN parameter — it
		// ignores unknown params, so this is effectively a hint only. The
		// actual time-zone handling is done by "_loc" set above; mattn parses
		// columns whose declared type contains "DATE"/"TIMESTAMP" and applies
		// _loc to the result. Kept for forward-compat / readability.
		base = appendQueryParam(base, "_time_format", "sqlite")
	}
	return base
}

func sqliteLocation(tz string) string {
	switch tz {
	case "":
		return "UTC"
	case "Local":
		return "auto"
	}
	return tz
}

func appendQueryParam(dsn, key, val string) string {
	if dsn == "" {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + key + "=" + val
}

func (c Config) buildMySQLDSN() string {
	host := c.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := c.Port
	if port == 0 {
		port = 3306
	}
	params := url.Values{}
	params.Set("parseTime", "true")
	if c.TimeZone != "" {
		// go-sql-driver/mysql expects a Go time.Location name; pass it through.
		params.Set("loc", c.TimeZone)
	} else {
		params.Set("loc", "UTC")
	}
	for k, v := range c.Params {
		params.Set(k, v)
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?%s",
		c.Username, c.Password, host, port, c.Database, params.Encode())
}
