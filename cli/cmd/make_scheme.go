package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/devituz/lagodev/internal/inflect"
	"github.com/spf13/cobra"
)

// NewMakeScheme returns `lago make:scheme <Name>` — generates a DTO
// (request / response schema) struct under the project's `schemes/`
// directory. The Name is normalised to PascalCase; the file name is
// snake_case.
//
//	lago make:scheme StorePost
//	# → schemes/store_post.go
//
// The generated DTO has validate-friendly tags so callers can plug it
// straight into `c.BindAndValidate(&dst)`.
func NewMakeScheme(_ *Env) *cobra.Command {
	var (
		force  bool
		outDir string
	)
	c := &cobra.Command{
		Use:   "make:scheme <Name>",
		Short: "Generate a request/response DTO under schemes/",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw := strings.TrimSpace(args[0])
			if raw == "" {
				return fmt.Errorf("scheme name required")
			}
			structName := inflect.Pascal(raw)
			fileName := inflect.Snake(structName) + ".go"
			pkgDir := outDir
			if pkgDir == "" {
				pkgDir = "schemes"
			}
			full := filepath.Join(pkgDir, fileName)
			if !force {
				if _, err := os.Stat(full); err == nil {
					return fmt.Errorf("%s already exists (pass --force)", full)
				}
			}
			if err := os.MkdirAll(pkgDir, 0o755); err != nil {
				return err
			}
			body := renderSchemeStub(filepath.Base(pkgDir), structName)
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				return err
			}
			printCreated(cmd, full)
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	c.Flags().StringVar(&outDir, "dir", "", "output directory (default: lago.json paths.schemes or 'schemes')")
	return c
}

func renderSchemeStub(pkg, name string) string {
	return `// Package ` + pkg + ` holds request/response DTOs for the HTTP layer.
//
// Each DTO carries struct-tag validation rules; controllers consume
// them via ` + "`c.BindAndValidate(&dst)`" + `. Failures auto-map to HTTP
// 422 with ` + "`{\"errors\": {field: message}}`" + `.
package ` + pkg + `

// ` + name + ` is the request body for the corresponding controller
// action. Add fields with ` + "`json`" + ` + ` + "`validate`" + ` tags as needed.
//
// Available validation rules (see web/validate.go):
//
//	required, min=N, max=N, gt=N, lt=N, email, url,
//	oneof=a b c, alpha, alphanumeric, uuid, numeric, integer, ip
type ` + name + ` struct {
	// Example field — replace with real ones.
	Name string ` + "`json:\"name\" validate:\"required,min=2,max=100\"`" + `
}
`
}
