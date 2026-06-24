package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"
)

// ProjectConfig is the per-project configuration loaded from `lago.json`
// (or the path in LAGO_CONFIG). It lets each project decide where models,
// migrations, factories, seeders, and tests live — so one team can keep
// everything under `internal/`, another at the repo root, and a third in
// `database/`, all with the same artisan binary.
type ProjectConfig struct {
	Paths Paths `json:"paths"`
}

// Paths declares the directories the make:* commands write to.
type Paths struct {
	Models     string `json:"models"`
	Migrations string `json:"migrations"`
	Factories  string `json:"factories"`
	Seeders    string `json:"seeders"`
	Tests      string `json:"tests"`
	Resources  string `json:"resources"`
	Policies   string `json:"policies"`
}

// DefaultPaths returns the conventional layout (Laravel-flavored).
func DefaultPaths() Paths {
	return Paths{
		Models:     "models",
		Migrations: "migrations",
		Factories:  "factories",
		Seeders:    "seeders",
		Tests:      "tests",
		Resources:  "resources",
		Policies:   "policies",
	}
}

var (
	projectOnce sync.Once
	project     *ProjectConfig
)

// LoadProject returns the project config, loading and caching it on first
// call. The cache is process-scoped so commands within one binary share it.
//
// Resolution order:
//
//  1. The path in $LAGO_CONFIG, if set
//  2. `lago.json` in the current directory
//  3. Built-in defaults (see DefaultPaths)
func LoadProject() *ProjectConfig {
	projectOnce.Do(func() {
		project = loadProject()
	})
	return project
}

// resetProjectForTest is exported only via _test files via the unexported
// name; it lets tests reset the cached singleton.
//
//nolint:unused
func resetProjectForTest() {
	projectOnce = sync.Once{}
	project = nil
}

func loadProject() *ProjectConfig {
	cfg := &ProjectConfig{Paths: DefaultPaths()}
	path := os.Getenv("LAGO_CONFIG")
	if path == "" {
		path = "lago.json"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	var loaded ProjectConfig
	if err := json.Unmarshal(b, &loaded); err != nil {
		// Don't crash on a malformed file — surface a warning via stderr and
		// fall back to defaults.
		fmt.Fprintf(os.Stderr, "lago: failed to parse %s: %v (using defaults)\n", path, err)
		return cfg
	}
	if loaded.Paths.Models != "" {
		cfg.Paths.Models = loaded.Paths.Models
	}
	if loaded.Paths.Migrations != "" {
		cfg.Paths.Migrations = loaded.Paths.Migrations
	}
	if loaded.Paths.Factories != "" {
		cfg.Paths.Factories = loaded.Paths.Factories
	}
	if loaded.Paths.Seeders != "" {
		cfg.Paths.Seeders = loaded.Paths.Seeders
	}
	if loaded.Paths.Tests != "" {
		cfg.Paths.Tests = loaded.Paths.Tests
	}
	if loaded.Paths.Resources != "" {
		cfg.Paths.Resources = loaded.Paths.Resources
	}
	if loaded.Paths.Policies != "" {
		cfg.Paths.Policies = loaded.Paths.Policies
	}
	return cfg
}

// NewInit returns `artisan init` — yangi loyihaning standart skeletini
// yaratadi: lago.json, config/, routes/ papkalari va sample fayllar.
func NewInit(_ *Env) *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "init",
		Short: "Scaffold lago.json, config/, routes/ for a new project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// 1. lago.json
			if err := writeIfNew(cmd, "lago.json", mustMarshal(ProjectConfig{Paths: DefaultPaths()}), force); err != nil {
				return err
			}
			// 2. config/ fayllari
			module := readModulePath()
			if err := writeIfNew(cmd, "config/app.go", configAppStub(), force); err != nil {
				return err
			}
			if err := writeIfNew(cmd, "config/database.go", configDatabaseStub(), force); err != nil {
				return err
			}
			// 3. routes/ fayllari
			if err := writeIfNew(cmd, "routes/api.go", routesStub(module), force); err != nil {
				return err
			}
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}

func mustMarshal(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b) + "\n"
}

func writeIfNew(cmd *cobra.Command, path, body string, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	printCreated(cmd, path)
	return nil
}

func configAppStub() string {
	return `// Package config — ilovaning markazlashgan sozlamalari.
package config

import (
	"github.com/devituz/lagodev/config"
)

// AppConfig — server va ilova darajasidagi sozlamalar.
type AppConfig struct {
	Name  string
	Env   string
	Debug bool
	Addr  string
}

// App — env'dan AppConfig'ni quradi.
func App() AppConfig {
	return AppConfig{
		Name:  config.String("APP_NAME", "app"),
		Env:   config.String("APP_ENV", "local"),
		Debug: config.Bool("APP_DEBUG", false),
		Addr:  config.String("APP_ADDR", ":8080"),
	}
}
`
}

func configDatabaseStub() string {
	return `package config

import (
	"github.com/devituz/lagodev/config"
	"github.com/devituz/lagodev/database"
)

// Database — env'dan database.Config'ni quradi.
func Database() database.Config {
	return config.FromEnv()
}
`
}

func routesStub(module string) string {
	if module == "" {
		module = "yourapp"
	}
	return `// Package routes — Laravel uslubidagi marshrut e'lonlari.
package routes

import (
	"github.com/devituz/lagodev/web"
)

// Register — barcha marshrutlarni app'ga ulaydi. main.go shuni chaqiradi.
func Register(app *web.App) {
	app.Get("/health", func(c *web.Context) (any, error) {
		return map[string]string{"status": "ok"}, nil
	})

	// Resource marshrutlari avtomat 5 ta endpoint qo'shadi:
	//   GET    /posts            → Index
	//   GET    /posts/{id}       → Show
	//   POST   /posts            → Store
	//   PUT    /posts/{id}       → Update
	//   PATCH  /posts/{id}       → Update
	//   DELETE /posts/{id}       → Destroy
	//
	// Birinchi controllerni generatsiya qilgandan keyin shu yerga ulang:
	//
	//   import "` + module + `/controllers"
	//   app.Resource("posts", controllers.NewPostController(app.DB()))
	//
	// API guruh va middleware:
	//
	//   app.Group("/api/v1", func(g *web.Router) {
	//       g.Use(web.Auth())
	//       g.Resource("users", controllers.NewUserController(app.DB()))
	//   })
}
`
}
