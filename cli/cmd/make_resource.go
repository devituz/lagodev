package cmd

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// NewMakeResource returns `make:resource` — generates an API Resource /
// serializer that maps a model to a clean JSON representation via the
// resource package (resource.Func + resource.Fields).
//
//	artisan make:resource UserResource --model=User
//	artisan make:resource UserResource --model=User \
//	    --fields="name:string,email:string"
//
// Fields declared here become the serialised map; sensitive columns
// (password, token, …) are simply left out. The generated value plugs into
// resource.Item / resource.Collection for every endpoint.
func NewMakeResource(env *Env) *cobra.Command {
	var (
		dir      string
		modelDir string
		model    string
		fields   string
		force    bool
	)
	c := &cobra.Command{
		Use:   "make:resource <Name>",
		Short: "Generate an API resource (serializer) for a model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := inflect.Pascal(args[0])
			if !strings.HasSuffix(name, "Resource") {
				name += "Resource"
			}
			if model == "" {
				model = strings.TrimSuffix(name, "Resource")
			}
			parsed, err := ParseFields(fields)
			if err != nil {
				return err
			}
			return generateResourceWithFields(cmd, env, dir, modelDir, name, model, parsed, force)
		},
	}
	c.Flags().StringVar(&dir, "dir", LoadProject().Paths.Resources, "output directory")
	c.Flags().StringVar(&modelDir, "model-dir", LoadProject().Paths.Models, "directory holding the model (for import)")
	c.Flags().StringVar(&model, "model", "", "model name (default: derived from resource)")
	c.Flags().StringVar(&fields, "fields", "", "field spec, e.g. name:string,email:string")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}

func generateResourceWithFields(cmd *cobra.Command, _ *Env, dir, modelDir, name, model string, fields []FieldSpec, force bool) error {
	pkg := pkgFromOutDir(dir)
	importPath, ref := resolveModelImport(dir, modelDir, model)
	path := filepath.Join(dir, inflect.Snake(name)+".go")
	body, err := renderStub("resource", map[string]any{
		"Package":     pkg,
		"Name":        name,
		"VarName":     name,
		"ModelRef":    ref,
		"ModelImport": importPath,
		"Fields":      fields,
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
