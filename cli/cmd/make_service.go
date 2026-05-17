package cmd

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// NewMakeService returns `make:service` — generates a framework-agnostic
// CRUD service for a model. The service is the same regardless of whether
// the host app uses Gin, Fiber, Echo, Chi, gRPC, or plain net/http; the
// generated controller (and any custom adapters you write) sits on top.
//
//	artisan make:service PostService --model=Post
func NewMakeService(env *Env) *cobra.Command {
	var (
		dir      string
		modelDir string
		model    string
		force    bool
	)
	c := &cobra.Command{
		Use:   "make:service <Name>",
		Short: "Generate a framework-agnostic CRUD service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := inflect.Pascal(args[0])
			if !strings.HasSuffix(name, "Service") {
				name += "Service"
			}
			if model == "" {
				model = strings.TrimSuffix(name, "Service")
			}
			return generateServiceInDir(cmd, env, dir, modelDir, name, model, force)
		},
	}
	c.Flags().StringVar(&dir, "dir", "services", "output directory")
	c.Flags().StringVar(&modelDir, "model-dir", LoadProject().Paths.Models, "directory holding the model")
	c.Flags().StringVar(&model, "model", "", "model name (default: derived from service)")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}

func generateService(cmd *cobra.Command, env *Env, name, model string, force bool) error {
	return generateServiceInDir(cmd, env, "services", LoadProject().Paths.Models, name, model, force)
}

func generateServiceInDir(cmd *cobra.Command, _ *Env, dir, modelDir, name, model string, force bool) error {
	pkg := pkgFromOutDir(dir)
	importPath, ref := resolveModelImport(dir, modelDir, model)
	path := filepath.Join(dir, inflect.Snake(name)+".go")
	body, err := renderStub("service", map[string]any{
		"Package":     pkg,
		"Name":        name,
		"ModelRef":    ref,
		"ModelImport": importPath,
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
