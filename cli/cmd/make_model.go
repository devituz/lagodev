package cmd

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// NewMakeModel returns the `make:model` command. Flags:
//
//	-m         also create a migration for the model
//	-f         also create a factory
//	-s         also create a seeder
//	-t         also create a test
//	-a         all of the above
//	--fields   field spec: name:type[:modifier], comma-separated
//	--dir      output directory (default: models/)
//	--force    overwrite existing files
//
// Example:
//
//	artisan make:model Post -m -f \
//	    --fields="title:string,body:text,published:bool:default(false)"
//
// Fields declared once on the command line are written identically to the
// model (PascalCase + column tag) and to the migration (snake_case schema
// methods) so the two stay in lockstep.
func NewMakeModel(env *Env) *cobra.Command {
	var (
		dir        string
		migrate    bool
		factory    bool
		seeder     bool
		test       bool
		controller bool
		resource   bool
		policy     bool
		all        bool
		force      bool
		fields     string
		framework  string
	)
	c := &cobra.Command{
		Use:   "make:model <Name>",
		Short: "Generate a model struct",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := inflect.Pascal(args[0])
			pkg := pkgFromOutDir(dir)
			table := inflect.Table(name)

			parsed, err := ParseFields(fields)
			if err != nil {
				return err
			}

			path := filepath.Join(dir, inflect.Snake(name)+".go")
			body, err := renderStub("model", map[string]any{
				"Name":            name,
				"Package":         pkg,
				"Table":           table,
				"Fields":          parsed,
				"NeedsTimeImport": needsTimeImport(parsed),
			})
			if err != nil {
				return err
			}
			if err := writeFile(path, body, force); err != nil {
				return err
			}
			printCreated(cmd, path)

			paths := LoadProject().Paths
			if all || migrate {
				if err := generateMigrationWithFields(cmd, env, paths.Migrations,
					"create_"+table+"_table", table, parsed, force); err != nil {
					return err
				}
			}
			if all || factory {
				if err := generateFactoryWithFields(cmd, env,
					paths.Factories, dir, name+"Factory", name, parsed, force); err != nil {
					return err
				}
			}
			if all || seeder {
				if err := generateSeederInDir(cmd, env, paths.Seeders, name+"Seeder", force); err != nil {
					return err
				}
			}
			if all || test {
				if err := generateTestInDir(cmd, env, paths.Tests, name, force); err != nil {
					return err
				}
			}
			if all || controller {
				if err := generateControllerInDirFor(cmd, env,
					"controllers", LoadProject().Paths.Models,
					name+"Controller", name, framework, force); err != nil {
					return err
				}
			}
			if all || resource {
				if err := generateResourceWithFields(cmd, env,
					paths.Resources, dir, name+"Resource", name, parsed, force); err != nil {
					return err
				}
			}
			if all || policy {
				if err := generatePolicyInDir(cmd, env,
					paths.Policies, dir, name+"Policy", name, "User", force); err != nil {
					return err
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", LoadProject().Paths.Models, "output directory")
	c.Flags().BoolVarP(&migrate, "migration", "m", false, "also create a migration")
	c.Flags().BoolVarP(&factory, "factory", "f", false, "also create a factory")
	c.Flags().BoolVarP(&seeder, "seeder", "s", false, "also create a seeder")
	c.Flags().BoolVarP(&test, "test", "t", false, "also create a test")
	c.Flags().BoolVarP(&controller, "controller", "c", false, "also create a REST controller")
	c.Flags().BoolVarP(&resource, "resource", "r", false, "also create an API resource")
	c.Flags().BoolVarP(&policy, "policy", "p", false, "also create an authorization policy")
	c.Flags().BoolVarP(&all, "all", "a", false, "create migration, factory, seeder, test, controller, resource and policy")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	c.Flags().StringVar(&fields, "fields", "", "field spec, e.g. name:string,email:string:unique")
	c.Flags().StringVar(&framework, "framework", "web", "controller flavor for -c: web (default) or gin")
	return c
}

func needsTimeImport(fields []FieldSpec) bool {
	for _, f := range fields {
		if f.NeedsTimeImport() {
			return true
		}
	}
	return false
}
