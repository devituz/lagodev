package cmd

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// NewMakeSeeder returns the `make:seeder` command.
func NewMakeSeeder(env *Env) *cobra.Command {
	var (
		dir   string
		force bool
	)
	c := &cobra.Command{
		Use:   "make:seeder <Name>",
		Short: "Generate a seeder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := inflect.Pascal(args[0])
			if !strings.HasSuffix(name, "Seeder") {
				name += "Seeder"
			}
			return generateSeederInDir(cmd, env, dir, name, force)
		},
	}
	c.Flags().StringVar(&dir, "dir", LoadProject().Paths.Seeders, "output directory")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}

func generateSeeder(cmd *cobra.Command, env *Env, name string, force bool) error {
	return generateSeederInDir(cmd, env, "seeders", name, force)
}

func generateSeederInDir(cmd *cobra.Command, _ *Env, dir, name string, force bool) error {
	pkg := pkgFromOutDir(dir)
	path := filepath.Join(dir, inflect.Snake(name)+".go")
	body, err := renderStub("seeder", map[string]any{
		"Package": pkg,
		"Name":    name,
	})
	if err != nil {
		return err
	}
	if err := writeFile(path, body, force); err != nil {
		return err
	}
	printCreated(cmd, path)

	// First-run convenience: if there is no DatabaseSeeder yet, scaffold one
	// so the project has an orchestrator. We don't try to wire the new
	// seeder into its Call() list automatically — auto-editing user code is
	// fragile, so we just print a hint.
	if name != "DatabaseSeeder" {
		dbPath := filepath.Join(dir, "database_seeder.go")
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			dbBody, err := renderStub("database_seeder", map[string]any{"Package": pkg})
			if err != nil {
				return err
			}
			if err := writeFile(dbPath, dbBody, false); err != nil {
				return err
			}
			printCreated(cmd, dbPath)
		}
		cmd.Println(color.YellowString("hint:"), "add &"+name+"{} to DatabaseSeeder's Call() list in "+dbPath)
	}
	return nil
}
