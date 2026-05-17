package cmd

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// NewMakeController returns `make:controller` — generates a REST-style
// HTTP handler set (Index/Show/Store/Update/Destroy) bound to a model.
// The output uses stdlib net/http signatures, so it adapts cleanly to Gin,
// Fiber, Echo, Chi, or chi-style routers.
//
//	artisan make:controller PostController --model=Post
func NewMakeController(env *Env) *cobra.Command {
	var (
		dir      string
		modelDir string
		model    string
		force    bool
	)
	c := &cobra.Command{
		Use:   "make:controller <Name>",
		Short: "Generate a REST controller for a model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := inflect.Pascal(args[0])
			if !strings.HasSuffix(name, "Controller") {
				name += "Controller"
			}
			if model == "" {
				model = strings.TrimSuffix(name, "Controller")
			}
			return generateControllerInDir(cmd, env, dir, modelDir, name, model, force)
		},
	}
	c.Flags().StringVar(&dir, "dir", "controllers", "output directory")
	c.Flags().StringVar(&modelDir, "model-dir", LoadProject().Paths.Models, "directory holding the model")
	c.Flags().StringVar(&model, "model", "", "model name (default: derived from controller)")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}

func generateController(cmd *cobra.Command, env *Env, name, model string, force bool) error {
	return generateControllerInDir(cmd, env, "controllers", LoadProject().Paths.Models, name, model, force)
}

func generateControllerInDir(cmd *cobra.Command, env *Env, dir, modelDir, name, model string, force bool) error {
	// Always generate the service first so the controller can delegate to it.
	serviceName := model + "Service"
	if err := generateServiceInDir(cmd, env, "services", modelDir, serviceName, model, force); err != nil {
		return err
	}

	pkg := pkgFromOutDir(dir)
	importPath, ref := resolveModelImport(dir, modelDir, model)
	serviceImport, serviceRef := resolveModelImport(dir, "services", serviceName)
	serviceConstructor := "New" + serviceName
	if serviceImport != "" {
		// Different package — qualify both type and constructor with the
		// package name so the controller compiles standalone.
		servicePkg := strings.SplitN(serviceRef, ".", 2)[0]
		serviceConstructor = servicePkg + "." + serviceConstructor
	}
	path := filepath.Join(dir, inflect.Snake(name)+".go")
	body, err := renderStub("controller", map[string]any{
		"Package":            pkg,
		"Name":               name,
		"ModelRef":           ref,
		"ModelImport":        importPath,
		"ServiceRef":         serviceRef,
		"ServiceImport":      serviceImport,
		"ServiceConstructor": serviceConstructor,
		"Resource":           inflect.Table(model),
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
