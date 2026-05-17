package cmd

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// NewMakeFactory returns the `make:factory` command.
//
//	artisan make:factory UserFactory --model=User
//	artisan make:factory UserFactory --model=User \
//	    --fields="name:string,email:string"
//
// Field types determine which faker method is invoked in the generated
// factory; well-known column names (name, email, phone, …) get the most
// natural call automatically.
func NewMakeFactory(env *Env) *cobra.Command {
	var (
		dir      string
		modelDir string
		model    string
		fields   string
		force    bool
	)
	c := &cobra.Command{
		Use:   "make:factory <Name>",
		Short: "Generate a factory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := inflect.Pascal(args[0])
			if !strings.HasSuffix(name, "Factory") {
				name += "Factory"
			}
			if model == "" {
				model = strings.TrimSuffix(name, "Factory")
			}
			parsed, err := ParseFields(fields)
			if err != nil {
				return err
			}
			return generateFactoryWithFields(cmd, env, dir, modelDir, name, model, parsed, force)
		},
	}
	c.Flags().StringVar(&dir, "dir", LoadProject().Paths.Factories, "output directory")
	c.Flags().StringVar(&modelDir, "model-dir", LoadProject().Paths.Models, "directory holding the model (for import)")
	c.Flags().StringVar(&model, "model", "", "model name (default: derived from factory)")
	c.Flags().StringVar(&fields, "fields", "", "field spec, e.g. name:string,email:string")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}

func generateFactoryWithFields(cmd *cobra.Command, _ *Env, dir, modelDir, name, model string, fields []FieldSpec, force bool) error {
	pkg := pkgFromOutDir(dir)
	importPath, ref := resolveModelImport(dir, modelDir, model)
	path := filepath.Join(dir, inflect.Snake(name)+".go")
	body, err := renderStub("factory", map[string]any{
		"Package":         pkg,
		"Name":            name,
		"ModelRef":        ref,
		"ModelImport":     importPath,
		"Fields":          fields,
		"NeedsTimeImport": needsTimeImport(fields),
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
