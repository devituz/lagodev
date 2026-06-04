// Package cli implements the Artisan-style command-line interface. The CLI is
// a thin, embeddable layer: callers in cmd/artisan (or in their own
// applications) construct a *cli.App, register their drivers/migrations/
// seeders, and call App.Execute().
//
// The CLI is decoupled from any specific driver: applications are responsible
// for blank-importing the driver they need before calling Execute().
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/cli/cmd"
	"github.com/devituz/lagodev/config"
	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/logger"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/seeder"
)

// App ties together a database manager, migration registry, seeder registry
// and Cobra root command. Applications usually take the default App via
// Default() and add custom commands.
type App struct {
	Root        *cobra.Command
	Manager     *database.Manager
	Migrations  *migrations.Registry
	Seeders     *seeder.Registry
	Logger      *logger.Logger
	Connector   func(*App) (*database.Connection, error)
	ProjectName string
}

// Options configures a fresh App.
type Options struct {
	ProjectName string
	Manager     *database.Manager
	Migrations  *migrations.Registry
	Seeders     *seeder.Registry
	Logger      *logger.Logger
	// Connector lets the host application override how the active connection
	// is resolved. The default resolves it from environment variables.
	Connector func(*App) (*database.Connection, error)
}

// New constructs an App with the given options. Nil fields fall back to
// process-wide defaults so a vanilla `lagocli` binary just works.
func New(opts Options) *App {
	// Wire the embedded stub filesystem into the cmd package once, regardless
	// of how many App instances the host constructs.
	cmd.SetStubFS(Stubs())
	if opts.Manager == nil {
		opts.Manager = database.Global
	}
	if opts.Migrations == nil {
		opts.Migrations = migrations.Default
	}
	if opts.Seeders == nil {
		opts.Seeders = seeder.Default
	}
	if opts.Logger == nil {
		opts.Logger = logger.New(logger.LevelInfo)
	}
	if opts.ProjectName == "" {
		opts.ProjectName = "artisan"
	}
	app := &App{
		Manager:     opts.Manager,
		Migrations:  opts.Migrations,
		Seeders:     opts.Seeders,
		Logger:      opts.Logger,
		Connector:   opts.Connector,
		ProjectName: opts.ProjectName,
	}
	if app.Connector == nil {
		app.Connector = defaultConnector
	}
	app.Root = newRoot(app)
	return app
}

// Default is the zero-config App most binaries use.
func Default() *App { return New(Options{}) }

// Execute is a convenience wrapper around App.Root.Execute() that prints
// errors in red and exits with status 1 on failure.
func (a *App) Execute() {
	if err := a.Root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, color.RedString("error: %v", err))
		os.Exit(1)
	}
}

// Connection resolves the active database connection via the configured
// connector. It is the entry point every db-touching command uses.
func (a *App) Connection() (*database.Connection, error) {
	if a.Connector == nil {
		return nil, errors.New("cli: no connector configured")
	}
	return a.Connector(a)
}

// AddCommand mounts a command directly on the root.
func (a *App) AddCommand(c *cobra.Command) { a.Root.AddCommand(c) }

// defaultConnector wires up a connection from environment variables, using
// whichever driver the binary has blank-imported. It deliberately does not
// import drivers itself.
//
// The connector reads .env files in the conventional Laravel order:
//
//	.env             — the canonical project-wide file
//	.env.local       — local developer overrides (gitignored)
//	.env.$APP_ENV    — environment-specific (.env.production, .env.testing, …)
//
// Each file overlays the previous; later values win.
func defaultConnector(a *App) (*database.Connection, error) {
	if conn, err := a.Manager.Default(); err == nil {
		return conn, nil
	}
	loadEnvChain()
	cfg := config.FromEnv()
	if cfg.Driver == "" {
		return nil, errors.New("cli: DB_CONNECTION not set in environment or .env file")
	}
	return a.Manager.Open("default", cfg)
}

func loadEnvChain() {
	// best-effort: missing files are silently skipped.
	_ = config.LoadEnv(".env")
	_ = config.LoadEnv(".env.local")
	if env := os.Getenv("APP_ENV"); env != "" {
		_ = config.LoadEnv(".env." + env)
	}
}

// newRoot constructs the cobra root command with all built-in subcommands.
func newRoot(a *App) *cobra.Command {
	root := &cobra.Command{
		Use:   a.ProjectName,
		Short: "lagodev — Laravel-style migrations, ORM, factories, seeders",
		Long: fmt.Sprintf(`lagodev provides Artisan-inspired tooling for Go applications.

Run "%s <command> --help" to see options for each command.`, a.ProjectName),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	bridge := &cmd.Env{
		Connection: func(ctx context.Context) (*database.Connection, error) {
			return a.Connection()
		},
		Migrations: a.Migrations,
		Seeders:    a.Seeders,
		Logger:     a.Logger,
		Timeout:    60 * time.Second,
	}

	// Laravel-style colon-namespaced commands at the top level. These are
	// the canonical invocation: `artisan migrate:fresh`, `artisan make:model`.
	mount := func(c *cobra.Command, names ...string) {
		for i, n := range names {
			clone := *c
			clone.Use = n
			if i == 0 {
				root.AddCommand(&clone)
				continue
			}
			// Aliases to the same handler under multiple names.
			clone.Hidden = true
			root.AddCommand(&clone)
		}
	}

	mount(cmd.NewMakeModel(bridge), "make:model")
	mount(cmd.NewMakeMigration(bridge), "make:migration")
	mount(cmd.NewMakeSeeder(bridge), "make:seeder")
	mount(cmd.NewMakeFactory(bridge), "make:factory")
	mount(cmd.NewMakeTest(bridge), "make:test")
	mount(cmd.NewMakeCRUD(bridge), "make:crud")
	mount(cmd.NewMakeController(bridge), "make:controller")
	mount(cmd.NewInit(bridge), "init")
	mount(cmd.NewNew(bridge), "new")

	mount(cmd.NewMigrateUp(bridge), "migrate")
	mount(cmd.NewMigrateRollback(bridge), "migrate:rollback")
	mount(cmd.NewMigrateReset(bridge), "migrate:reset")
	mount(cmd.NewMigrateRefresh(bridge), "migrate:refresh")
	mount(cmd.NewMigrateFresh(bridge), "migrate:fresh")
	mount(cmd.NewMigrateStatus(bridge), "migrate:status")
	mount(cmd.NewMigrateStep(bridge), "migrate:step")

	mount(cmd.NewDBSeed(bridge), "db:seed")
	mount(cmd.NewDBWipe(bridge), "db:wipe")
	mount(cmd.NewDBShow(bridge), "db:show")
	mount(cmd.NewDBTable(bridge), "db:table")
	mount(cmd.NewDB(bridge), "db")

	mount(cmd.NewEnv(bridge), "env")
	mount(cmd.NewEnvInit(bridge), "env:init")
	mount(cmd.NewKeyGenerate(bridge), "key:generate")
	mount(cmd.NewMakeService(bridge), "make:service")
	mount(cmd.NewMakeScheme(bridge), "make:scheme")

	return root
}
