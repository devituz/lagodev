package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// NewMakeController returns `make:controller` — generates a REST-style
// HTTP handler set (Index/Show/Store/Update/Destroy) bound to a model.
// Default output uses stdlib net/http via the `web` package; pass
// `--framework=gin` to emit a Gin-flavored controller that uses the
// lagogin adapter (Paginate + BindAndValidate + Resource compatibility).
//
//	artisan make:controller PostController --model=Post
//	artisan make:controller PostController --framework=gin
func NewMakeController(env *Env) *cobra.Command {
	var (
		dir       string
		modelDir  string
		model     string
		framework string
		force     bool
	)
	c := &cobra.Command{
		Use:   "make:controller <Name>",
		Short: "Generate a REST controller for a model (--framework=web|gin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := inflect.Pascal(args[0])
			if !strings.HasSuffix(name, "Controller") {
				name += "Controller"
			}
			if model == "" {
				model = strings.TrimSuffix(name, "Controller")
			}
			return generateControllerInDirFor(cmd, env, dir, modelDir, name, model, framework, force)
		},
	}
	c.Flags().StringVar(&dir, "dir", "controllers", "output directory")
	c.Flags().StringVar(&modelDir, "model-dir", LoadProject().Paths.Models, "directory holding the model")
	c.Flags().StringVar(&model, "model", "", "model name (default: derived from controller)")
	c.Flags().StringVar(&framework, "framework", "web", "controller flavor: web (default) or gin")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}

func generateController(cmd *cobra.Command, env *Env, name, model string, force bool) error {
	return generateControllerInDirFor(cmd, env, "controllers", LoadProject().Paths.Models, name, model, "web", force)
}

func generateControllerInDir(cmd *cobra.Command, env *Env, dir, modelDir, name, model string, force bool) error {
	return generateControllerInDirFor(cmd, env, dir, modelDir, name, model, "web", force)
}

func generateControllerInDirFor(cmd *cobra.Command, env *Env, dir, modelDir, name, model, framework string, force bool) error {
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
	stubName := "controller"
	switch framework {
	case "", "web":
		stubName = "controller"
	case "gin":
		stubName = "controller_gin"
	default:
		return fmt.Errorf("--framework=%q: only 'web' and 'gin' are supported", framework)
	}
	path := filepath.Join(dir, inflect.Snake(name)+".go")
	body, err := renderStub(stubName, map[string]any{
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
