package cmd

import (
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// SetStubFS lets the cli package (or applications) install the embedded stub
// filesystem. It is called automatically by cli.New(); applications that
// embed their own stub directory can replace it.
//
// We avoid making this package responsible for the //go:embed directive
// because relative embed paths cannot escape upward, and we want stubs to
// live at cli/stubs/ rather than cli/cmd/stubs/ (the latter would shadow
// the implementation package).
func SetStubFS(filesystem fs.FS) {
	stubMu.Lock()
	defer stubMu.Unlock()
	stubFS = filesystem
}

var (
	stubMu    sync.RWMutex
	stubFS    fs.FS
	overrides = map[string]string{}
)

// SetStubOverride redirects a specific stub to a file path on disk. Useful
// for projects that ship branded code generators.
func SetStubOverride(stub, path string) {
	stubMu.Lock()
	defer stubMu.Unlock()
	overrides[stub] = path
}

func readStub(name string) (string, error) {
	stubMu.RLock()
	override, ok := overrides[name]
	fsys := stubFS
	stubMu.RUnlock()
	if ok {
		b, err := os.ReadFile(override)
		if err == nil {
			return string(b), nil
		}
	}
	if fsys == nil {
		return "", fmt.Errorf("cli: stub filesystem not configured")
	}
	b, err := fs.ReadFile(fsys, name+".stub")
	if err != nil {
		return "", fmt.Errorf("cli: stub %q not found: %w", name, err)
	}
	return string(b), nil
}

func renderStub(name string, data any) (string, error) {
	body, err := readStub(name)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New(name).Parse(body)
	if err != nil {
		return "", fmt.Errorf("cli: parse stub %s: %w", name, err)
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("cli: render stub %s: %w", name, err)
	}
	return sb.String(), nil
}

func writeFile(path, content string, force bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("file already exists: %s (use --force to overwrite)", path)
		}
	}
	// gofmt Go files so generated code is identical to what a human would
	// commit. If formatting fails (e.g. for non-Go stubs or syntax errors),
	// fall back to the raw template output so the user can fix it.
	out := []byte(content)
	if strings.HasSuffix(path, ".go") {
		if formatted, err := format.Source(out); err == nil {
			out = formatted
		}
	}
	return os.WriteFile(path, out, 0o644)
}

func printCreated(cmd *cobra.Command, path string) {
	cmd.Println(color.GreenString("✓ created"), path)
}

// timestamp returns a Laravel-style "YYYY_MM_DD_HHMMSS" prefix used in
// migration filenames. The underscores make the components readable at a
// glance and match the convention every Artisan-bred Go developer expects.
func timestamp() string { return time.Now().UTC().Format("2006_01_02_150405") }

func pkgFromOutDir(dir string) string {
	base := filepath.Base(filepath.Clean(dir))
	if base == "." || base == "" {
		return "main"
	}
	return inflect.Snake(base)
}

// readModulePath returns the host project's module path from go.mod, or ""
// if go.mod is missing or unparseable. We use this to wire fully-qualified
// import paths into generated factories (which live in a different package
// than the model they build).
func readModulePath() string {
	b, err := os.ReadFile("go.mod")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module"))
		}
	}
	return ""
}

// resolveModelImport computes the import path and qualified reference for a
// generated factory. If the factory and model live in the same directory we
// return empty import and a bare model name; otherwise we return a fully
// qualified <pkg>.<Model> reference and the import path it requires.
func resolveModelImport(factoryDir, modelDir, modelName string) (importPath, ref string) {
	cleanFactory := filepath.Clean(factoryDir)
	cleanModel := filepath.Clean(modelDir)
	if cleanFactory == cleanModel {
		return "", modelName
	}
	module := readModulePath()
	if module == "" {
		// Without a module path we cannot construct an import. Fall back to a
		// bare reference and let the user wire the import manually.
		return "", modelName
	}
	imp := module + "/" + filepath.ToSlash(cleanModel)
	pkg := pkgFromOutDir(cleanModel)
	return imp, pkg + "." + modelName
}
