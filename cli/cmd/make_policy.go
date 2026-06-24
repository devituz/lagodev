package cmd

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// NewMakePolicy returns `make:policy` — generates an authz.Policy struct
// whose methods cover the standard abilities (ViewAny/View/Create/Update/
// Delete). The gate routes ability names to the matching method:
//
//	g := authz.New[User]()
//	authz.Policy[Post](g, PostPolicy{})
//	ok, _ := g.Allows(ctx, "update", currentUser, somePost)
//
//	artisan make:policy PostPolicy --model=Post
//	artisan make:policy PostPolicy --model=Post --user=models.User
//
// Every method returns false by default (deny-by-default); fill each in with
// the project's real rules.
func NewMakePolicy(env *Env) *cobra.Command {
	var (
		dir      string
		modelDir string
		model    string
		user     string
		force    bool
	)
	c := &cobra.Command{
		Use:   "make:policy <Name>",
		Short: "Generate an authorization policy for a model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := inflect.Pascal(args[0])
			if !strings.HasSuffix(name, "Policy") {
				name += "Policy"
			}
			if model == "" {
				model = strings.TrimSuffix(name, "Policy")
			}
			return generatePolicyInDir(cmd, env, dir, modelDir, name, model, user, force)
		},
	}
	c.Flags().StringVar(&dir, "dir", LoadProject().Paths.Policies, "output directory")
	c.Flags().StringVar(&modelDir, "model-dir", LoadProject().Paths.Models, "directory holding the model (for import)")
	c.Flags().StringVar(&model, "model", "", "model name (default: derived from policy)")
	c.Flags().StringVar(&user, "user", "User", "user type alias used by the policy receivers")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}

func generatePolicyInDir(cmd *cobra.Command, _ *Env, dir, modelDir, name, model, user string, force bool) error {
	if user == "" {
		user = "User"
	}
	pkg := pkgFromOutDir(dir)
	importPath, ref := resolveModelImport(dir, modelDir, model)
	path := filepath.Join(dir, inflect.Snake(name)+".go")
	body, err := renderStub("policy", map[string]any{
		"Package":     pkg,
		"Name":        name,
		"UserType":    user,
		"ModelRef":    ref,
		"ModelImport": importPath,
		"LowerModel":  inflect.Snake(model),
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
