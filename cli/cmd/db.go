package cmd

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/seeder"
)

// NewDBSeed returns `db:seed [SeederName...]`. With no args, all registered
// seeders run in dependency order. Named seeders (positional args, or
// --class for Laravel parity) restrict the run; their dependencies still
// run first.
func NewDBSeed(env *Env) *cobra.Command {
	var (
		transactional bool
		class         string
	)
	c := &cobra.Command{
		Use:   "seed [Seeder...]",
		Short: "Run database seeders",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := env.ctx()
			defer cancel()
			conn, err := env.Connection(ctx)
			if err != nil {
				return err
			}
			only := append([]string(nil), args...)
			if class != "" {
				only = append(only, class)
			}
			runner := seeder.NewRunner(conn, env.Seeders, seeder.Options{
				Transactional: transactional,
				Logger:        env.Logger,
				Only:          only,
			})
			if err := runner.Run(ctx); err != nil {
				return err
			}
			cmd.Println(color.GreenString("seeded"))
			return nil
		},
	}
	c.Flags().BoolVar(&transactional, "transactional", false, "wrap each seeder in a transaction")
	c.Flags().StringVar(&class, "class", "", "run a single seeder by name (Laravel-compatible)")
	return c
}

// NewDBWipe returns `db:wipe` — DROPs every user table. Refuses to run
// without --force. Useful for tests; not intended for production.
func NewDBWipe(env *Env) *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "wipe",
		Short: "Drop all tables in the database (requires --force)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !force {
				return fmt.Errorf("refusing to wipe without --force")
			}
			ctx, cancel := env.ctx()
			defer cancel()
			conn, err := env.Connection(ctx)
			if err != nil {
				return err
			}
			tables, err := listAllTables(ctx, conn)
			if err != nil {
				return err
			}
			for _, t := range tables {
				if _, err := conn.ExecContext(ctx, "DROP TABLE IF EXISTS "+conn.Grammar.Quote(t)); err != nil {
					return fmt.Errorf("drop %s: %w", t, err)
				}
				cmd.Println(color.RedString("dropped"), t)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "confirm dropping every table")
	return c
}

// NewDBShow returns `db:show` — prints a summary of the active connection:
// driver, version, schema, table count.
func NewDBShow(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Display information about the connected database",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := env.ctx()
			defer cancel()
			conn, err := env.Connection(ctx)
			if err != nil {
				return err
			}
			cmd.Println(color.New(color.Bold).Sprint("Connection: "), conn.Name)
			cmd.Println(color.New(color.Bold).Sprint("Driver:     "), conn.Grammar.Name())
			cmd.Println(color.New(color.Bold).Sprint("Database:   "), conn.Config.Database)
			cmd.Println(color.New(color.Bold).Sprint("Host:       "), conn.Config.Host)
			cmd.Println(color.New(color.Bold).Sprint("Time zone:  "), conn.Location().String())

			tables, err := listAllTables(ctx, conn)
			if err != nil {
				return err
			}
			cmd.Println(color.New(color.Bold).Sprint("Tables:     "), len(tables))
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, color.New(color.Faint).Sprint("  NAME\tROWS"))
			for _, t := range tables {
				var n int64
				_ = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+conn.Grammar.Quote(t)).Scan(&n)
				fmt.Fprintf(tw, "  %s\t%d\n", t, n)
			}
			return tw.Flush()
		},
	}
}

// NewDBTable returns `db:table <name>` — prints schema info for one table:
// columns, types, nullability, and (where supported) approximate row count.
func NewDBTable(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "table <name>",
		Short: "Show schema for a single table",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := env.ctx()
			defer cancel()
			conn, err := env.Connection(ctx)
			if err != nil {
				return err
			}
			table := args[0]
			cols, err := describeTable(ctx, conn, table)
			if err != nil {
				return err
			}
			cmd.Println(color.New(color.Bold).Sprintf("Table: %s", table))
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, color.New(color.Bold).Sprint("  COLUMN\tTYPE\tNULLABLE\tDEFAULT"))
			for _, c := range cols {
				nullable := "NO"
				if c.Nullable {
					nullable = "YES"
				}
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", c.Name, c.Type, nullable, c.Default)
			}
			if err := tw.Flush(); err != nil {
				return err
			}

			var rows int64
			if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+conn.Grammar.Quote(table)).Scan(&rows); err == nil {
				cmd.Printf("Rows: %d\n", rows)
			}
			return nil
		},
	}
}

// NewDB returns `db` — a tiny SQL REPL. Each line is exec'd against the
// active connection; SELECTs render as a tab-aligned table.
func NewDB(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "db",
		Short: "Open a simple SQL prompt against the active connection",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := env.ctx()
			defer cancel()
			conn, err := env.Connection(ctx)
			if err != nil {
				return err
			}
			cmd.Printf("%s connected (type \\q or Ctrl-D to exit)\n", color.GreenString(conn.Grammar.Name()))

			reader := bufio.NewReader(os.Stdin)
			for {
				fmt.Print(color.CyanString("sql> "))
				line, err := reader.ReadString('\n')
				if errors.Is(err, io.EOF) {
					cmd.Println()
					return nil
				}
				if err != nil {
					return err
				}
				stmt := strings.TrimSpace(line)
				if stmt == "" {
					continue
				}
				if stmt == `\q` || stmt == "exit" || stmt == "quit" {
					return nil
				}
				if err := runOneSQL(ctx, conn, stmt); err != nil {
					fmt.Fprintln(os.Stderr, color.RedString("error: %v", err))
				}
			}
		},
	}
}

// columnInfo describes a single column for db:table.
type columnInfo struct {
	Name     string
	Type     string
	Nullable bool
	Default  string
}

func describeTable(ctx context.Context, conn *database.Connection, table string) ([]columnInfo, error) {
	switch conn.Grammar.Name() {
	case "sqlite":
		rows, err := conn.QueryContext(ctx, "PRAGMA table_info("+conn.Grammar.Quote(table)+")")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []columnInfo
		for rows.Next() {
			var (
				cid     int
				name    string
				typ     string
				notnull int
				dflt    sql.NullString
				pk      int
			)
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				return nil, err
			}
			out = append(out, columnInfo{
				Name: name, Type: typ, Nullable: notnull == 0, Default: dflt.String,
			})
		}
		return out, rows.Err()

	case "postgres":
		q := `SELECT column_name, data_type, is_nullable, COALESCE(column_default, '')
		      FROM information_schema.columns
		      WHERE table_schema = current_schema() AND table_name = $1
		      ORDER BY ordinal_position`
		rows, err := conn.QueryContext(ctx, q, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []columnInfo
		for rows.Next() {
			var c columnInfo
			var nullable string
			if err := rows.Scan(&c.Name, &c.Type, &nullable, &c.Default); err != nil {
				return nil, err
			}
			c.Nullable = strings.EqualFold(nullable, "YES")
			out = append(out, c)
		}
		return out, rows.Err()

	case "mysql":
		q := `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COALESCE(COLUMN_DEFAULT, '')
		      FROM information_schema.columns
		      WHERE table_schema = DATABASE() AND table_name = ?
		      ORDER BY ordinal_position`
		rows, err := conn.QueryContext(ctx, q, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []columnInfo
		for rows.Next() {
			var c columnInfo
			var nullable string
			if err := rows.Scan(&c.Name, &c.Type, &nullable, &c.Default); err != nil {
				return nil, err
			}
			c.Nullable = strings.EqualFold(nullable, "YES")
			out = append(out, c)
		}
		return out, rows.Err()
	}
	return nil, fmt.Errorf("db:table: unsupported driver %q", conn.Grammar.Name())
}

// runOneSQL executes one statement and prints results to stdout. SELECT
// statements render row-by-row using tabwriter; everything else prints
// the rows-affected count where the driver reports it.
func runOneSQL(ctx context.Context, conn *database.Connection, stmt string) error {
	upper := strings.ToUpper(strings.TrimSpace(stmt))
	if strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "PRAGMA") || strings.HasPrefix(upper, "SHOW") || strings.HasPrefix(upper, "EXPLAIN") {
		rows, err := conn.QueryContext(ctx, stmt)
		if err != nil {
			return err
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, color.New(color.Bold).Sprint(strings.Join(cols, "\t")))
		holders := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range holders {
			ptrs[i] = &holders[i]
		}
		count := 0
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			parts := make([]string, len(cols))
			for i, h := range holders {
				if b, ok := h.([]byte); ok {
					parts[i] = string(b)
				} else {
					parts[i] = fmt.Sprintf("%v", h)
				}
			}
			fmt.Fprintln(tw, strings.Join(parts, "\t"))
			count++
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Printf("%s\n", color.New(color.Faint).Sprintf("(%d rows)", count))
		return rows.Err()
	}
	res, err := conn.ExecContext(ctx, stmt)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err == nil {
		fmt.Printf("%s\n", color.GreenString("OK — %d rows affected", affected))
	} else {
		fmt.Println(color.GreenString("OK"))
	}
	return nil
}

func listAllTables(ctx context.Context, conn *database.Connection) ([]string, error) {
	var query string
	switch conn.Grammar.Name() {
	case "sqlite":
		query = "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
	case "postgres":
		query = "SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = 'public'"
	case "mysql":
		query = "SELECT TABLE_NAME FROM information_schema.tables WHERE table_schema = DATABASE()"
	default:
		return nil, fmt.Errorf("db:wipe: unsupported driver %q", conn.Grammar.Name())
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
		if strings.EqualFold(name, "migrations") || strings.EqualFold(name, "migrations_lock") {
			continue
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
