package cli

import (
	"strings"
	"testing"
)

// RunProjectBinary must be a no-op outside a project and inside the
// re-executed child (otherwise it would recurse forever).
func TestRunProjectBinary_NoOpWithoutEntrypointOrWhenBootstrapped(t *testing.T) {
	t.Chdir(t.TempDir())
	if RunProjectBinary() {
		t.Fatal("handled execution without a project entrypoint")
	}
	t.Setenv(BootstrapEnv, "1")
	if RunProjectBinary() {
		t.Fatal("re-executed inside an already bootstrapped child")
	}
}

// Scaffolding commands run in-process; registry-dependent ones re-execute.
func TestNeedsProject(t *testing.T) {
	cases := map[string]bool{
		"":                     false,
		"make:model Post -mfs": false,
		"init":                 false,
		"env:init":             false,
		"key:generate":         false,
		"gen:client":           false,
		"--help":               false,
		"migrate":              true,
		"migrate:fresh --seed": true,
		"db:seed":              true,
		"my:custom":            true,
	}
	for args, want := range cases {
		if got := needsProject(strings.Fields(args)); got != want {
			t.Errorf("needsProject(%q) = %v, want %v", args, got, want)
		}
	}
}
