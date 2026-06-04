package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

// NewNew returns `lago new <name>` — scaffolds a fresh project with the
// canonical Laravel-style layout, an interactive infrastructure picker,
// and a complete Docker / Kubernetes deploy stack.
//
//	lago new myapp                            # interactive prompts
//	lago new myapp --yes                      # non-interactive defaults
//	lago new myapp --framework=gin --db=postgres --cache=redis --storage=minio
//	lago new myapp --module=acme/myapp        # explicit module path
func NewNew(_ *Env) *cobra.Command {
	var (
		framework string
		module    string
		db        string
		cache     string
		storage   string
		force     bool
		yes       bool
	)
	c := &cobra.Command{
		Use:   "new <name>",
		Short: "Scaffold a new lagodev project (interactive)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == "" {
				return fmt.Errorf("project name required")
			}
			if module == "" {
				module = name
			}

			// Friendly preflight — surface missing host tools but never block.
			preflight(cmd.ErrOrStderr())

			interactive := !yes && (db == "" && cache == "" && storage == "")
			opts := ScaffoldOptions{
				Name:      name,
				Module:    module,
				Framework: framework,
				Database:  db,
				Cache:     cache,
				Storage:   storage,
			}
			if interactive {
				if err := promptInfra(&opts); err != nil {
					return err
				}
			} else {
				if opts.Database == "" {
					opts.Database = "sqlite"
				}
				if opts.Cache == "" {
					opts.Cache = "none"
				}
				if opts.Storage == "" {
					opts.Storage = "none"
				}
			}

			switch opts.Framework {
			case "", "web":
				opts.Framework = "web"
			case "gin":
			default:
				return fmt.Errorf("--framework=%q: only 'web' and 'gin' are supported", opts.Framework)
			}
			switch opts.Database {
			case "postgres", "mysql", "sqlite", "none":
			default:
				return fmt.Errorf("--db=%q: only 'postgres', 'mysql', 'sqlite', 'none' are supported", opts.Database)
			}
			switch opts.Cache {
			case "redis", "none":
			default:
				return fmt.Errorf("--cache=%q: only 'redis' or 'none'", opts.Cache)
			}
			switch opts.Storage {
			case "minio", "none":
			default:
				return fmt.Errorf("--storage=%q: only 'minio' or 'none'", opts.Storage)
			}
			return scaffoldProject(cmd, opts, force)
		},
	}
	c.Flags().StringVar(&framework, "framework", "web", "HTTP flavor: web or gin")
	c.Flags().StringVar(&module, "module", "", "go module path (default: <name>)")
	c.Flags().StringVar(&db, "db", "", "primary database: postgres | mysql | sqlite | none")
	c.Flags().StringVar(&cache, "cache", "", "cache / queue: redis | none")
	c.Flags().StringVar(&storage, "storage", "", "object storage: minio | none")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	c.Flags().BoolVar(&yes, "yes", false, "non-interactive — use defaults / flag values")
	return c
}

// preflight checks for common host-side tools and prints a hint when
// any are missing. Failures are warnings, never blocking.
func preflight(out io.Writer) {
	tools := []struct{ name, hint string }{
		{"go", "needed to build the scaffold (https://go.dev/dl/)"},
		{"git", "recommended for version control"},
		{"docker", "needed for docker-compose / Dockerfile workflows"},
		{"docker-compose", "needed by the generated docker-compose.yml"},
		{"kubectl", "needed to apply the generated k8s/ manifests"},
	}
	missing := []string{}
	for _, t := range tools {
		if _, err := exec.LookPath(t.name); err != nil {
			missing = append(missing, t.name)
		}
	}
	if len(missing) == 0 {
		return
	}
	fmt.Fprintf(out, "%s missing host tools: %s\n",
		color.YellowString("warning:"), strings.Join(missing, ", "))
	fmt.Fprintln(out, "  (the project still scaffolds; install them when you need that workflow)")
}

func promptInfra(opts *ScaffoldOptions) error {
	if opts.Framework == "" {
		fw, err := selectPrompt("HTTP framework",
			[]string{"web (built-in)", "gin (adapters/gin)"})
		if err != nil {
			return err
		}
		opts.Framework = "web"
		if strings.HasPrefix(fw, "gin") {
			opts.Framework = "gin"
		}
	}
	if opts.Database == "" {
		dbChoice, err := selectPrompt("Primary database",
			[]string{"postgres", "mysql", "sqlite", "none"})
		if err != nil {
			return err
		}
		opts.Database = dbChoice
	}
	if opts.Cache == "" {
		cChoice, err := selectPrompt("Cache / queue / broadcasting",
			[]string{"redis", "none"})
		if err != nil {
			return err
		}
		opts.Cache = cChoice
	}
	if opts.Storage == "" {
		sChoice, err := selectPrompt("Object storage",
			[]string{"minio", "none"})
		if err != nil {
			return err
		}
		opts.Storage = sChoice
	}
	return nil
}

func selectPrompt(label string, items []string) (string, error) {
	p := promptui.Select{
		Label: label,
		Items: items,
		Templates: &promptui.SelectTemplates{
			Selected: fmt.Sprintf("%s {{ . | green }}", promptui.IconGood),
		},
	}
	_, choice, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("prompt: %w", err)
	}
	return choice, nil
}

func scaffoldProject(cmd *cobra.Command, opts ScaffoldOptions, force bool) error {
	if !force {
		if _, err := os.Stat(opts.Name); err == nil {
			return fmt.Errorf("directory %q already exists (pass --force)", opts.Name)
		}
	}
	root := opts.Name

	files := []struct {
		path, body string
	}{
		{filepath.Join(root, "go.mod"), goModStub(opts.Module, opts.Framework)},
		{filepath.Join(root, ".env"), envStubWithInfra(opts)},
		{filepath.Join(root, ".gitignore"), gitignoreStub()},
		{filepath.Join(root, ".dockerignore"), dockerIgnore},
		{filepath.Join(root, "lago.json"), lagoJSONStub()},
		{filepath.Join(root, "config", "app.go"), configAppStub()},
		{filepath.Join(root, "config", "database.go"), configDatabaseStub()},
		{filepath.Join(root, "models", "doc.go"), pkgDocStub("models", "Application models — embed orm.Model.")},
		{filepath.Join(root, "migrations", "doc.go"), pkgDocStub("migrations", "Schema migrations. Generated files call migrations.Register in init().")},
		{filepath.Join(root, "factories", "doc.go"), pkgDocStub("factories", "Faker-powered model factories.")},
		{filepath.Join(root, "seeders", "doc.go"), pkgDocStub("seeders", "Seeders register themselves in init() via seeder.Register.")},
		{filepath.Join(root, "tests", ".keep"), ""},
		{filepath.Join(root, "services", "doc.go"), pkgDocStub("services", "Framework-agnostic CRUD services.")},
		{filepath.Join(root, "controllers", "doc.go"), pkgDocStub("controllers", "HTTP controllers (web or lagogin flavor).")},
		{filepath.Join(root, "schemes", "doc.go"), pkgDocStub("schemes", "DTOs used for request validation / response shaping.")},
	}
	switch opts.Framework {
	case "gin":
		files = append(files,
			struct{ path, body string }{filepath.Join(root, "main.go"), mainStubGin(opts.Module)},
			struct{ path, body string }{filepath.Join(root, "routes", "api.go"), routesStubGin(opts.Module)},
		)
	default:
		files = append(files,
			struct{ path, body string }{filepath.Join(root, "main.go"), mainStubWeb(opts.Module)},
			struct{ path, body string }{filepath.Join(root, "routes", "api.go"), routesStub(opts.Module)},
		)
	}

	// Deploy stack — always generate Dockerfile + docker-compose.yml +
	// k8s/. Even projects that won't deploy today benefit from having
	// the recipe in-repo for "later".
	rendered, err := renderDeployArtifacts(opts)
	if err != nil {
		return err
	}
	for _, da := range rendered {
		files = append(files, struct{ path, body string }{filepath.Join(root, da.path), da.body})
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
	cmd.Println(color.GreenString("\n✓ project created"))
	cmd.Printf("\n  cd %s\n", opts.Name)
	cmd.Println("  go mod tidy")
	cmd.Println("  lago key:generate     # writes APP_KEY into .env")
	cmd.Println("  go run .              # boot the app")
	cmd.Println("\nDeploy:")
	cmd.Println("  docker compose up --build")
	cmd.Println("  kubectl apply -k k8s/")
	return nil
}

type deployArtifact struct{ path, body string }

func renderDeployArtifacts(opts ScaffoldOptions) ([]deployArtifact, error) {
	pairs := []struct{ name, tmpl, path string }{
		{"Dockerfile", dockerfileTmpl, "Dockerfile"},
		{"compose", composeTmpl, "docker-compose.yml"},
		{"k8s-deployment", k8sDeploymentTmpl, "k8s/deployment.yaml"},
		{"k8s-service", k8sServiceTmpl, "k8s/service.yaml"},
		{"k8s-configmap", k8sConfigMapTmpl, "k8s/configmap.yaml"},
		{"k8s-secret", k8sSecretTmpl, "k8s/secret.yaml"},
		{"k8s-kustomization", k8sKustomizationTmpl, "k8s/kustomization.yaml"},
	}
	out := make([]deployArtifact, 0, len(pairs))
	for _, p := range pairs {
		body, err := renderTemplate(p.name, p.tmpl, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, deployArtifact{path: p.path, body: body})
	}
	return out, nil
}

func goModStub(module, framework string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "module %s\n\ngo 1.25.0\n\nrequire (\n", module)
	fmt.Fprintf(&sb, "\tgithub.com/devituz/lagodev v0.18.1\n")
	if framework == "gin" {
		fmt.Fprintf(&sb, "\tgithub.com/devituz/lagodev/adapters/gin v0.18.1\n")
		fmt.Fprintf(&sb, "\tgithub.com/gin-gonic/gin v1.10.0\n")
	}
	fmt.Fprintf(&sb, ")\n")
	return sb.String()
}

func gitignoreStub() string {
	return `bin/
*.db
*.db-journal
.env.local
.env.*.local
node_modules/
tmp/
build-errors.log
`
}

func pkgDocStub(pkg, description string) string {
	return "// Package " + pkg + " — " + description + "\npackage " + pkg + "\n"
}

func lagoJSONStub() string {
	b, _ := json.MarshalIndent(ProjectConfig{Paths: DefaultPaths()}, "", "  ")
	return string(b) + "\n"
}

func mainStubWeb(module string) string {
	return `package main

import (
	"context"
	"log"
	"os"

	lagocfg "github.com/devituz/lagodev/config"
	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/web"

	"` + module + `/config"
	_ "` + module + `/migrations" // registers schema migrations via init()
	"` + module + `/routes"
	_ "` + module + `/seeders" // registers seeders via init()
)

func loadEnv() {
	_ = lagocfg.LoadEnv(".env")
	_ = lagocfg.LoadEnv(".env.local")
	if env := os.Getenv("APP_ENV"); env != "" {
		_ = lagocfg.LoadEnv(".env." + env)
	}
}

func main() {
	loadEnv()
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
	app.Use(web.RequestID(), web.SecurityHeaders())
	app.Health()                            // GET /healthz
	app.Ready(web.HealthCheck{              // GET /readyz
		Name: "db",
		Probe: func(ctx context.Context) error { return conn.DB.PingContext(ctx) },
	})

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
	"os"

	"github.com/gin-gonic/gin"

	lagocfg "github.com/devituz/lagodev/config"
	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"

	lagogin "github.com/devituz/lagodev/adapters/gin"

	"` + module + `/config"
	_ "` + module + `/migrations" // registers schema migrations via init()
	"` + module + `/routes"
	_ "` + module + `/seeders" // registers seeders via init()
)

func loadEnv() {
	_ = lagocfg.LoadEnv(".env")
	_ = lagocfg.LoadEnv(".env.local")
	if env := os.Getenv("APP_ENV"); env != "" {
		_ = lagocfg.LoadEnv(".env." + env)
	}
}

func main() {
	loadEnv()
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

	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	r.GET("/readyz", func(c *gin.Context) {
		if err := conn.DB.PingContext(c.Request.Context()); err != nil {
			c.JSON(503, gin.H{"status": "not_ready", "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"status": "ready"})
	})

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
