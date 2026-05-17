package cmd

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// NewMakeMigration returns the `make:migration` command.
//
//	artisan make:migration create_users_table
//	artisan make:migration create_users_table --fields="name:string,email:string:unique"
//	artisan make:migration add_email_to_users --table=users
func NewMakeMigration(env *Env) *cobra.Command {
	var (
		dir    string
		table  string
		fields string
		force  bool
	)
	c := &cobra.Command{
		Use:   "make:migration <name>",
		Short: "Generate a migration file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := inflect.Snake(args[0])
			inferredTable := table
			if inferredTable == "" {
				inferredTable = inferTable(name)
			}
			parsed, err := ParseFields(fields)
			if err != nil {
				return err
			}
			return generateMigrationWithFields(cmd, env, dir, name, inferredTable, parsed, force)
		},
	}
	c.Flags().StringVar(&dir, "dir", LoadProject().Paths.Migrations, "output directory")
	c.Flags().StringVar(&table, "table", "", "target table (overrides inferred name)")
	c.Flags().StringVar(&fields, "fields", "", "field spec, e.g. name:string,email:string:unique")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}

func generateMigrationWithFields(cmd *cobra.Command, _ *Env, dir, name, table string, fields []FieldSpec, force bool) error {
	stamp := timestamp()
	pkg := pkgFromOutDir(dir)
	full := stamp + "_" + name
	path := filepath.Join(dir, full+".go")
	body, err := renderStub("migration", map[string]any{
		"Package": pkg,
		"Name":    full,
		"Table":   table,
		"Fields":  fields,
	})
	if err != nil {
		return err
	}
	if err := writeFile(path, body, force); err != nil {
		return err
	}
	printCreated(cmd, path)
	return nil
}

// inferTable extracts a likely table name from a migration name.
// "create_users_table"     -> "users"
// "add_email_to_users"     -> "users"
// "drop_old_jobs_table"    -> "old_jobs"
// "modify_orders"          -> ""
func inferTable(name string) string {
	switch {
	case strings.HasPrefix(name, "create_") && strings.HasSuffix(name, "_table"):
		return strings.TrimSuffix(strings.TrimPrefix(name, "create_"), "_table")
	case strings.HasPrefix(name, "drop_") && strings.HasSuffix(name, "_table"):
		return strings.TrimSuffix(strings.TrimPrefix(name, "drop_"), "_table")
	}
	if idx := strings.LastIndex(name, "_to_"); idx >= 0 {
		return name[idx+len("_to_"):]
	}
	if idx := strings.LastIndex(name, "_from_"); idx >= 0 {
		return name[idx+len("_from_"):]
	}
	return ""
}
