package cmd

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// NewMakeTest returns the `make:test` command.
//
//	artisan make:test User       → tests/user_test.go (TestUser)
//	artisan make:test PaymentFlow → tests/payment_flow_test.go (TestPaymentFlow)
func NewMakeTest(env *Env) *cobra.Command {
	var (
		dir   string
		force bool
	)
	c := &cobra.Command{
		Use:   "make:test <Name>",
		Short: "Generate a test scaffold",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := inflect.Pascal(args[0])
			return generateTestInDir(cmd, env, dir, name, force)
		},
	}
	c.Flags().StringVar(&dir, "dir", "tests", "output directory")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}

func generateTest(cmd *cobra.Command, env *Env, name string, force bool) error {
	return generateTestInDir(cmd, env, "tests", name, force)
}

func generateTestInDir(cmd *cobra.Command, _ *Env, dir, name string, force bool) error {
	pkg := pkgFromOutDir(dir)
	base := strings.TrimSuffix(inflect.Snake(name), "_test") + "_test"
	path := filepath.Join(dir, base+".go")
	body, err := renderStub("test", map[string]any{
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
	return nil
}
