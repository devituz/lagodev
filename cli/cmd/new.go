package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// NewNew returns `lago new <name>` — scaffolds a fresh project with the
// canonical Laravel-style layout. The --framework flag picks the HTTP
// flavor: `web` (default) uses the built-in web.App; `gin` wires up
// gin-gonic + the lagogin adapter.
//
//	lago new myapp                       # web.App scaffold
//	lago new myapp --framework=gin       # Gin + lagogin scaffold
//	lago new myapp --module=acme/myapp   # explicit module path
func NewNew(_ *Env) *cobra.Command {
	var (
		framework string
		module    string
		force     bool
	)
	c := &cobra.Command{
		Use:   "new <name>",
		Short: "Scaffold a new lagodev project (--framework=web|gin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == "" {
				return fmt.Errorf("project name required")
			}
			switch framework {
			case "", "web":
				framework = "web"
			case "gin":
			default:
				return fmt.Errorf("--framework=%q: only 'web' and 'gin' are supported", framework)
			}
			if module == "" {
				module = name
			}
			return scaffoldProject(cmd, name, module, framework, force)
		},
	}
	c.Flags().StringVar(&framework, "framework", "web", "HTTP flavor: web or gin")
	c.Flags().StringVar(&module, "module", "", "go module path (default: <name>)")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}

func scaffoldProject(cmd *cobra.Command, name, module, framework string, force bool) error {
	if !force {
		if _, err := os.Stat(name); err == nil {
			return fmt.Errorf("directory %q already exists (pass --force)", name)
		}
	}
	root := name

	files := []struct {
		path, body string
	}{
		{filepath.Join(root, "go.mod"), goModStub(module, framework)},
		{filepath.Join(root, ".env"), envStub(name)},
		{filepath.Join(root, ".gitignore"), gitignoreStub()},
		{filepath.Join(root, "lago.json"), lagoJSONStub()},
		{filepath.Join(root, "config", "app.go"), configAppStub()},
		{filepath.Join(root, "config", "database.go"), configDatabaseStub()},
		{filepath.Join(root, "models", ".keep"), ""},
		{filepath.Join(root, "migrations", ".keep"), ""},
		{filepath.Join(root, "factories", ".keep"), ""},
		{filepath.Join(root, "seeders", ".keep"), ""},
		{filepath.Join(root, "tests", ".keep"), ""},
		{filepath.Join(root, "services", ".keep"), ""},
		{filepath.Join(root, "controllers", ".keep"), ""},
	}
	switch framework {
	case "gin":
		files = append(files,
			struct{ path, body string }{filepath.Join(root, "main.go"), mainStubGin(module)},
			struct{ path, body string }{filepath.Join(root, "routes", "api.go"), routesStubGin(module)},
		)
	default:
		files = append(files,
			struct{ path, body string }{filepath.Join(root, "main.go"), mainStubWeb(module)},
			struct{ path, body string }{filepath.Join(root, "routes", "api.go"), routesStub(module)},
		)
	}

	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f.path, []byte(f.body), 0o644); err != nil {
			return err
		}
		printCreated(cmd, f.path)
	}
	cmd.Println(color.GreenString("\n✓ project created."))
	cmd.Printf("  cd %s && go mod tidy && go run .\n", name)
	return nil
}

func goModStub(module, framework string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "module %s\n\ngo 1.25.0\n\nrequire (\n", module)
	fmt.Fprintf(&sb, "\tgithub.com/devituz/lagodev v0.9.0\n")
	if framework == "gin" {
		fmt.Fprintf(&sb, "\tgithub.com/devituz/lagodev/adapters/gin v0.9.0\n")
		fmt.Fprintf(&sb, "\tgithub.com/gin-gonic/gin v1.10.0\n")
	}
	fmt.Fprintf(&sb, ")\n")
	return sb.String()
}

func envStub(name string) string {
	return `APP_NAME=` + name + `
APP_ENV=local
APP_DEBUG=true
APP_ADDR=:8080

# DB_CONNECTION supports: sqlite, postgres, mysql
DB_CONNECTION=sqlite
DB_DATABASE=app.db
# Postgres / MySQL example:
# DB_HOST=127.0.0.1
# DB_PORT=5432
# DB_USERNAME=app
# DB_PASSWORD=secret
# DB_DATABASE=app

JWT_SECRET=please-change-this-to-a-strong-random-secret
`
}

func gitignoreStub() string {
	return `bin/
*.db
*.db-journal
.env.local
.env.*.local
node_modules/
`
}

func lagoJSONStub() string {
	b, _ := json.MarshalIndent(ProjectConfig{Paths: DefaultPaths()}, "", "  ")
	return string(b) + "\n"
}

func mainStubWeb(module string) string {
	return `package main

import (
	"log"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/web"

	"` + module + `/config"
	"` + module + `/routes"
)

func main() {
	cfg := config.App()
	conn, err := database.Global.Open("default", config.Database())
	if err != nil {
		log.Fatal(err)
	}

	app := web.New(
		web.WithDatabase(conn),
		web.WithMigrations(nil),
		web.WithAddr(cfg.Addr),
	)
	routes.Register(app)
	app.MustRun()
}
`
}

func mainStubGin(module string) string {
	return `package main

import (
	"context"
	"log"

	"github.com/gin-gonic/gin"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"

	lagogin "github.com/devituz/lagodev/adapters/gin"

	"` + module + `/config"
	"` + module + `/routes"
)

func main() {
	cfg := config.App()
	ctx := context.Background()

	conn, err := database.Global.Open("default", config.Database())
	if err != nil {
		log.Fatal(err)
	}
	conn = lagogin.Instrument(conn)

	if _, err := migrations.New(conn, nil, migrations.Options{}).Up(ctx); err != nil {
		log.Fatal(err)
	}

	r := gin.Default()
	r.Use(lagogin.CORS("*"), lagogin.QueryLog(conn))

	routes.Register(r, conn)

	log.Printf("listening on %s", cfg.Addr)
	if err := r.Run(cfg.Addr); err != nil {
		log.Fatal(err)
	}
}
`
}

func routesStubGin(module string) string {
	_ = module
	return `// Package routes registers the application's Gin routes.
package routes

import (
	"github.com/gin-gonic/gin"

	"github.com/devituz/lagodev/database"
	lagogin "github.com/devituz/lagodev/adapters/gin"
)

// Register wires every HTTP route. main() calls this exactly once.
func Register(r *gin.Engine, conn *database.Connection) {
	r.GET("/health", lagogin.H(func(c *lagogin.Ctx) (any, error) {
		return map[string]string{"status": "ok"}, nil
	}))

	// Mount your resources here once you've generated controllers:
	//
	//   import "<module>/controllers"
	//   lagogin.Resource(r, "posts", controllers.NewPostController(conn))
	//
	// API group with JWT middleware:
	//
	//   api := r.Group("/api", lagogin.AuthJWT(authManager))
	//   lagogin.Resource(api, "users", controllers.NewUserController(conn))

	_ = conn
}
`
}
