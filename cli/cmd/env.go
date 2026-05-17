package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// NewEnv returns `env` — prints the currently active environment
// variables that drive lagodev (DB_*, APP_*). Sensitive values
// (PASSWORD, SECRET, KEY) are redacted by default; pass --show-secrets
// to print them in clear text.
func NewEnv(_ *Env) *cobra.Command {
	var showSecrets bool
	c := &cobra.Command{
		Use:   "env",
		Short: "Print the active environment configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, color.New(color.Bold).Sprint("KEY\tVALUE"))
			keys := envKeys()
			sort.Strings(keys)
			for _, k := range keys {
				v := os.Getenv(k)
				if !showSecrets && isSensitive(k) && v != "" {
					v = "***"
				}
				fmt.Fprintf(tw, "%s\t%s\n", k, v)
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&showSecrets, "show-secrets", false, "do not redact PASSWORD/SECRET/KEY values")
	return c
}

// NewEnvInit returns `env:init` — writes a starter .env file with the
// keys lagodev understands. Refuses to overwrite without --force.
func NewEnvInit(_ *Env) *cobra.Command {
	var (
		path  string
		force bool
	)
	c := &cobra.Command{
		Use:   "env:init",
		Short: "Create a starter .env file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !force {
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("%s already exists (use --force to overwrite)", path)
				}
			}
			if err := os.WriteFile(path, []byte(envTemplate), 0o600); err != nil {
				return err
			}
			printCreated(cmd, path)
			return nil
		},
	}
	c.Flags().StringVar(&path, "path", ".env", "destination path")
	c.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	return c
}

func envKeys() []string {
	return []string{
		"APP_ENV", "APP_DEBUG", "APP_NAME",
		"DB_CONNECTION", "DB_DSN",
		"DB_HOST", "DB_PORT",
		"DB_USERNAME", "DB_PASSWORD",
		"DB_DATABASE", "DB_SCHEMA",
		"DB_SSL_MODE", "DB_TIMEZONE",
		"DB_MAX_OPEN", "DB_MAX_IDLE",
		"DB_CONN_MAX_LIFETIME", "DB_CONN_MAX_IDLE_TIME",
		"DB_LOG_QUERIES", "DB_SLOW_QUERY",
		"LAGO_CONFIG",
	}
}

func isSensitive(k string) bool {
	uk := strings.ToUpper(k)
	return strings.Contains(uk, "PASSWORD") || strings.Contains(uk, "SECRET") || strings.Contains(uk, "KEY") || strings.Contains(uk, "TOKEN")
}

const envTemplate = `# lagodev environment configuration.
# Documented at https://pkg.go.dev/github.com/devituz/lagodev/config

APP_ENV=local
APP_DEBUG=true
APP_NAME=app

# --- Database ---
# Driver: postgres | mysql | sqlite
DB_CONNECTION=sqlite

# For sqlite, DB_DATABASE is the file path (or empty for in-memory).
DB_DATABASE=app.db

# For postgres/mysql:
# DB_HOST=127.0.0.1
# DB_PORT=5432
# DB_USERNAME=postgres
# DB_PASSWORD=
# DB_DATABASE=app
# DB_SCHEMA=public
# DB_SSL_MODE=disable
# DB_TIMEZONE=Asia/Tashkent

# Connection pool (optional)
# DB_MAX_OPEN=25
# DB_MAX_IDLE=5
# DB_CONN_MAX_LIFETIME=1h
# DB_CONN_MAX_IDLE_TIME=10m

# Logging (optional)
# DB_LOG_QUERIES=false
# DB_SLOW_QUERY=200ms
`
