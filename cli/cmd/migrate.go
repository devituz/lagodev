package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/seeder"
)

// seederRunnerFor builds a runner with the env's seeder registry / logger.
// Kept here so migrate:fresh --seed and db:seed share construction.
func seederRunnerFor(env *Env, conn *database.Connection) *seeder.Runner {
	return seeder.NewRunner(conn, env.Seeders, seeder.Options{Logger: env.Logger})
}

// NewMigrateUp returns the `migrate` command.
func NewMigrateUp(env *Env) *cobra.Command {
	var (
		pretend bool
		force   bool
		path    string
	)
	c := &cobra.Command{
		Use:   "up",
		Short: "Run all pending migrations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := env.ctx()
			defer cancel()
			conn, err := env.Connection(ctx)
			if err != nil {
				return err
			}
			registry := env.Migrations
			if path != "" {
				// --path filters the registry down to a single named migration.
				registry = filterRegistry(env.Migrations, path)
			}
			m := migrations.New(conn, registry, migrations.Options{Pretend: pretend, Logger: env.Logger})
			applied, err := m.Up(ctx)
			if err != nil {
				return err
			}
			if len(applied) == 0 {
				cmd.Println(color.YellowString("nothing to migrate"))
				return nil
			}
			for _, name := range applied {
				cmd.Println(color.GreenString("migrated"), name)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&pretend, "pretend", false, "print the SQL without executing it")
	c.Flags().BoolVar(&force, "force", false, "force running in production")
	c.Flags().StringVar(&path, "path", "", "restrict to a single migration by name (e.g. 2026_01_01_000000_create_users_table)")
	return c
}

// filterRegistry returns a new registry containing only the named migration
// (and migrations sorting before it that are already applied — the runner's
// out-of-order check is not bypassed).
func filterRegistry(src *migrations.Registry, name string) *migrations.Registry {
	r := migrations.NewRegistry()
	for _, m := range src.All() {
		if m.Name() == name || strings.Contains(m.Name(), name) {
			r.Register(m)
		}
	}
	return r
}

// NewMigrateRollback returns the `migrate:rollback` command.
func NewMigrateRollback(env *Env) *cobra.Command {
	var step int
	c := &cobra.Command{
		Use:   "rollback",
		Short: "Rollback the last migration batch (or --step N)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := env.ctx()
			defer cancel()
			conn, err := env.Connection(ctx)
			if err != nil {
				return err
			}
			m := migrations.New(conn, env.Migrations, migrations.Options{Logger: env.Logger})
			rolled, err := m.Rollback(ctx, step)
			if err != nil {
				return err
			}
			if len(rolled) == 0 {
				cmd.Println(color.YellowString("nothing to rollback"))
				return nil
			}
			for _, name := range rolled {
				cmd.Println(color.GreenString("rolled back"), name)
			}
			return nil
		},
	}
	c.Flags().IntVar(&step, "step", 0, "rollback this many migrations (default: latest batch)")
	return c
}

// NewMigrateReset returns the `migrate:reset` command.
func NewMigrateReset(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Rollback every applied migration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := env.ctx()
			defer cancel()
			conn, err := env.Connection(ctx)
			if err != nil {
				return err
			}
			m := migrations.New(conn, env.Migrations, migrations.Options{Logger: env.Logger})
			rolled, err := m.Reset(ctx)
			if err != nil {
				return err
			}
			cmd.Println(color.GreenString("reset"), len(rolled), "migrations")
			return nil
		},
	}
}

// NewMigrateRefresh returns the `migrate:refresh` command (rollback + up).
func NewMigrateRefresh(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Rollback and re-run all migrations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := env.ctx()
			defer cancel()
			conn, err := env.Connection(ctx)
			if err != nil {
				return err
			}
			m := migrations.New(conn, env.Migrations, migrations.Options{Logger: env.Logger})
			applied, err := m.Refresh(ctx)
			if err != nil {
				return err
			}
			cmd.Println(color.GreenString("refreshed"), len(applied), "migrations")
			return nil
		},
	}
}

// NewMigrateFresh returns the `migrate:fresh` command (drop all tables + up).
func NewMigrateFresh(env *Env) *cobra.Command {
	var seed bool
	c := &cobra.Command{
		Use:   "fresh",
		Short: "Drop all tables and re-run migrations (optionally seed)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := env.ctx()
			defer cancel()
			conn, err := env.Connection(ctx)
			if err != nil {
				return err
			}
			m := migrations.New(conn, env.Migrations, migrations.Options{Logger: env.Logger})
			applied, err := m.Fresh(ctx)
			if err != nil {
				return err
			}
			cmd.Println(color.GreenString("fresh"), len(applied), "migrations applied")
			if seed {
				runner := seederRunnerFor(env, conn)
				if err := runner.Run(ctx); err != nil {
					return err
				}
				cmd.Println(color.GreenString("seeded"))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&seed, "seed", false, "run seeders after fresh")
	return c
}

// NewMigrateStatus returns the `migrate:status` command.
func NewMigrateStatus(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the status of each migration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := env.ctx()
			defer cancel()
			conn, err := env.Connection(ctx)
			if err != nil {
				return err
			}
			m := migrations.New(conn, env.Migrations, migrations.Options{Logger: env.Logger})
			rows, err := m.Status(ctx)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, color.New(color.Bold).Sprint("STATUS\tBATCH\tNAME\tAPPLIED AT"))
			for _, r := range rows {
				badge := color.RedString("pending")
				batch := "—"
				when := "—"
				if r.Applied {
					badge = color.GreenString("applied")
					batch = fmt.Sprintf("%d", r.Batch)
					when = r.AppliedAt.Format("2006-01-02 15:04:05")
				}
				if r.Stale {
					badge = color.YellowString("stale")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", badge, batch, r.Name, when)
			}
			return tw.Flush()
		},
	}
}

// NewMigrateStep returns the `migrate:step` command.
func NewMigrateStep(env *Env) *cobra.Command {
	var n int
	c := &cobra.Command{
		Use:   "step",
		Short: "Apply up to N pending migrations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if n <= 0 {
				return fmt.Errorf("--n must be > 0")
			}
			ctx, cancel := env.ctx()
			defer cancel()
			conn, err := env.Connection(ctx)
			if err != nil {
				return err
			}
			m := migrations.New(conn, env.Migrations, migrations.Options{Logger: env.Logger})
			applied, err := m.Step(ctx, n)
			if err != nil {
				return err
			}
			cmd.Println(color.GreenString("step"), strings.Join(applied, ", "))
			return nil
		},
	}
	c.Flags().IntVar(&n, "n", 1, "number of migrations to apply")
	return c
}
