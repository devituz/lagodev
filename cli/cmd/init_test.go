package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// `lago init` did not scaffold a project-local CLI entrypoint, so the global
// `lago migrate` never saw the project's migrations ("nothing to migrate").
func TestInit_ScaffoldsProjectCLI(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("go.mod", []byte("module github.com/you/myapp\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewInit(nil)
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs(nil)
	if err := c.Execute(); err != nil {
		t.Fatalf("init: %v\n%s", err, buf.String())
	}

	main, err := os.ReadFile("cmd/lago/main.go")
	if err != nil {
		t.Fatalf("cmd/lago/main.go not created: %v", err)
	}
	for _, want := range []string{
		`_ "github.com/you/myapp/migrations"`,
		`_ "github.com/you/myapp/seeders"`,
		`cli.New(cli.Options{ProjectName: "lago"}).Execute()`,
	} {
		if !strings.Contains(string(main), want) {
			t.Errorf("cmd/lago/main.go missing %q", want)
		}
	}
	for _, f := range []string{"migrations/doc.go", "seeders/doc.go"} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s not created (the blank imports would not compile): %v", f, err)
		}
	}
}

// Existing migrations/seeders files are left alone.
func TestInit_KeepsExistingPackageDocs(t *testing.T) {
	t.Chdir(t.TempDir())
	_ = os.WriteFile("go.mod", []byte("module example.com/app\n"), 0o644)
	_ = os.MkdirAll("migrations", 0o755)
	_ = os.WriteFile("migrations/doc.go", []byte("package migrations // mine\n"), 0o644)
	c := NewInit(nil)
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetArgs(nil)
	if err := c.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}
	b, _ := os.ReadFile("migrations/doc.go")
	if string(b) != "package migrations // mine\n" {
		t.Fatalf("existing migrations/doc.go overwritten: %q", b)
	}
}
