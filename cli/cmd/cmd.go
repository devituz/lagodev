// Package cmd contains the implementation of each Artisan subcommand. Each
// command is a constructor returning a *cobra.Command — easy to compose,
// easy to unit-test, and decoupled from the App in cli/cli.go via the
// Env struct.
package cmd

import (
	"context"
	"time"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/logger"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/seeder"
)

// Env is the shared environment commands resolve at runtime. It is
// constructed by cli.App and passed to each cmd.New* constructor.
type Env struct {
	// Connection lazily returns the active *database.Connection.
	Connection func(ctx context.Context) (*database.Connection, error)
	Migrations *migrations.Registry
	Seeders    *seeder.Registry
	Logger     *logger.Logger
	// Timeout is the upper bound applied to commands that touch the DB.
	Timeout time.Duration
}

// ctx returns a derived context with the env's timeout.
func (e *Env) ctx() (context.Context, context.CancelFunc) {
	if e.Timeout == 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), e.Timeout)
}
