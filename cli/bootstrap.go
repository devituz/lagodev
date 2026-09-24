package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BootstrapEnv is set in the environment of a re-executed project-local CLI
// so it does not bootstrap again (infinite recursion). Setting it manually
// disables the indirection.
const BootstrapEnv = "LAGO_BOOTSTRAPPED"

// projectEntrypoints are the project-local CLI mains the global binaries look
// for, in order. `lago init` / `lago new` scaffold cmd/lago.
var projectEntrypoints = []string{"cmd/lago", "cmd/artisan"}

// RunProjectBinary re-executes the project-local CLI via `go run` when the
// working directory contains one (cmd/lago/main.go or cmd/artisan/main.go).
// That binary blank-imports the project's migrations and seeders packages, so
// their init() functions populate the registries — a globally installed
// binary cannot see them and would report "nothing to migrate". It returns
// true when it handled execution; the caller must then return. On failure it
// exits with the child's status.
//
// Scaffolding commands (init, new, env*, key:generate, make:*, gen:*) never
// need the project's registries and run in-process, so they keep working in a
// fresh project whose go.sum cannot build the local entrypoint yet.
func RunProjectBinary() bool {
	if os.Getenv(BootstrapEnv) == "1" || !needsProject(os.Args[1:]) {
		return false
	}
	for _, dir := range projectEntrypoints {
		if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
			continue
		}
		c := exec.Command("go", append([]string{"run", "./" + dir}, os.Args[1:]...)...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		c.Env = append(os.Environ(), BootstrapEnv+"=1")
		if err := c.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				os.Exit(ee.ExitCode())
			}
			fmt.Fprintln(os.Stderr, "lago: bootstrap failed:", err)
			os.Exit(1)
		}
		return true
	}
	return false
}

// needsProject reports whether the command named by args may depend on the
// project's registered migrations, seeders or custom commands.
func needsProject(args []string) bool {
	name := ""
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			name = a
			break
		}
	}
	switch {
	case name == "", name == "help", name == "completion", name == "version",
		name == "init", name == "new", name == "env", name == "key:generate",
		strings.HasPrefix(name, "env:"), strings.HasPrefix(name, "make:"),
		strings.HasPrefix(name, "gen:"):
		return false
	}
	return true
}
