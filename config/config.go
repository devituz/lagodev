// Package config loads framework configuration from environment variables
// and .env files. It deliberately does not depend on viper or other heavy
// configuration libraries — applications often need full control over their
// configuration source, and lagodev only requires a handful of values.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/devituz/lagodev/database"
)

// LoadEnv loads variables from one or more .env files into the process
// environment. Missing files are silently ignored.
func LoadEnv(files ...string) error {
	if len(files) == 0 {
		files = []string{".env"}
	}
	available := files[:0]
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			available = append(available, f)
		}
	}
	if len(available) == 0 {
		return nil
	}
	return godotenv.Load(available...)
}

// String returns the env value or fallback.
func String(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Int parses an integer env value or returns fallback.
func Int(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// Bool parses a boolean env value.
func Bool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "t", "yes", "y", "on":
		return true
	case "0", "false", "f", "no", "n", "off":
		return false
	}
	return fallback
}

// Duration parses a Go-formatted duration value or returns fallback.
func Duration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// FromEnv constructs a database.Config from environment variables.
//
//	DB_CONNECTION (driver), DB_HOST, DB_PORT, DB_USERNAME, DB_PASSWORD,
//	DB_DATABASE, DB_SCHEMA, DB_SSL_MODE, DB_TIMEZONE, DB_LOG_QUERIES,
//	DB_SLOW_QUERY, DB_MAX_OPEN, DB_MAX_IDLE.
//
// Useful for 12-factor apps that prefer env-driven config.
func FromEnv() database.Config {
	return database.Config{
		Driver:          String("DB_CONNECTION", "sqlite"),
		DSN:             String("DB_DSN", ""),
		Host:            String("DB_HOST", ""),
		Port:            Int("DB_PORT", 0),
		Username:        String("DB_USERNAME", ""),
		Password:        String("DB_PASSWORD", ""),
		Database:        String("DB_DATABASE", ""),
		Schema:          String("DB_SCHEMA", ""),
		SSLMode:         String("DB_SSL_MODE", ""),
		TimeZone:        String("DB_TIMEZONE", ""),
		MaxOpenConns:    Int("DB_MAX_OPEN", 0),
		MaxIdleConns:    Int("DB_MAX_IDLE", 0),
		ConnMaxLifetime: Duration("DB_CONN_MAX_LIFETIME", 0),
		ConnMaxIdleTime: Duration("DB_CONN_MAX_IDLE_TIME", 0),
		LogQueries:      Bool("DB_LOG_QUERIES", false),
		SlowQuery:       Duration("DB_SLOW_QUERY", 0),
	}
}
