package cmd

import (
	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// NewMakeCRUD returns `make:crud <Name> --fields=...` — a one-shot
// scaffold that generates the model, migration, factory, seeder, and test
// from a single field spec. This is the "Laravel-equivalent" of running
// `php artisan make:model -mfsr` plus a controller scaffold.
//
//	artisan make:crud Post \
//	    --fields="title:string,body:text,published:bool:default(false)"
//
// The same field declarations end up in every artifact, so column names
// and types stay in lockstep across the model + migration + factory.
func NewMakeCRUD(env *Env) *cobra.Command {
	var (
		modelDir     string
		migrationDir string
		factoryDir   string
		seederDir    string
		testDir      string
		fields       string
		force        bool
		skipFactory  bool
		skipSeeder   bool
		skipTest     bool
	)
	c := &cobra.Command{
		Use:   "make:crud <Name>",
		Short: "Generate a full CRUD scaffold (model + migration + factory + seeder + test)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := inflect.Pascal(args[0])
			table := inflect.Table(name)
			parsed, err := ParseFields(fields)
			if err != nil {
				return err
			}

			// 1. Model
			modelBody, err := renderStub("model", map[string]any{
				"Name":            name,
				"Package":         pkgFromOutDir(modelDir),
				"Table":           table,
				"Fields":          parsed,
				"NeedsTimeImport": needsTimeImport(parsed),
			})
			if err != nil {
				return err
			}
			modelPath := modelDir + "/" + inflect.Snake(name) + ".go"
			if err := writeFile(modelPath, modelBody, force); err != nil {
				return err
			}
			printCreated(cmd, modelPath)

			// 2. Migration
			if err := generateMigrationWithFields(cmd, env, migrationDir,
				"create_"+table+"_table", table, parsed, force); err != nil {
				return err
			}

			// 3. Factory
			if !skipFactory {
				if err := generateFactoryWithFields(cmd, env, factoryDir, modelDir,
					name+"Factory", name, parsed, force); err != nil {
					return err
				}
			}

			// 4. Seeder
			if !skipSeeder {
				if err := generateSeederInDir(cmd, env, seederDir, name+"Seeder", force); err != nil {
					return err
				}
			}

			// 5. Test
			if !skipTest {
				if err := generateTestInDir(cmd, env, testDir, name, force); err != nil {
					return err
				}
			}
			return nil
		},
	}
	paths := LoadProject().Paths
	c.Flags().StringVar(&modelDir, "models-dir", paths.Models, "model output directory")
	c.Flags().StringVar(&migrationDir, "migrations-dir", paths.Migrations, "migration output directory")
	c.Flags().StringVar(&factoryDir, "factories-dir", paths.Factories, "factory output directory")
	c.Flags().StringVar(&seederDir, "seeders-dir", paths.Seeders, "seeder output directory")
	c.Flags().StringVar(&testDir, "tests-dir", paths.Tests, "test output directory")
	c.Flags().StringVar(&fields, "fields", "", "field spec, e.g. name:string,email:string:unique")
	c.Flags().BoolVar(&skipFactory, "no-factory", false, "skip factory generation")
	c.Flags().BoolVar(&skipSeeder, "no-seeder", false, "skip seeder generation")
	c.Flags().BoolVar(&skipTest, "no-test", false, "skip test generation")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}
