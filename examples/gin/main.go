// Gin integration example for lagodev — refreshed for v0.10 with the
// lagogin adapter. The whole CRUD surface now fits in ~80 lines without
// any of the `if err != nil { c.JSON(...) }` boilerplate the original
// example had.
//
// Highlights:
//
//   - lagogin.Resource(r, "users", ctrl) registers 5 REST routes
//   - lagogin.H wraps `(any, error)` handlers — orm.ErrNotFound → 404,
//     validation errors → 422, other errors → 500
//   - lagogin.Paginate adds ?page= / ?per_page= envelopes for free
//   - lagogin.QueryLog emits X-DB-Query-Count and warns on N+1
//   - lagogin.AuthJWT verifies Bearer tokens in one line
//   - lagogin.ServeOpenAPI exposes /openapi.json + /docs
//
// Run:
//
//	go run .
//	curl http://localhost:8080/users
//	curl http://localhost:8080/users?page=2
//	curl -X POST http://localhost:8080/users -d '{"name":"Ada","email":"ada@x.com"}'
//	open http://localhost:8080/docs
package main

import (
	"context"
	"log"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/devituz/lagodev/auth"
	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/schema"

	lagogin "github.com/devituz/lagodev/adapters/gin"
)

// --- Model — minimal, just like a migration ------------------------------
//
// Column names are derived automatically: Name→name, PasswordHash→password_hash.
// TableName() overrides the inferred plural; without it the table would be
// inferred as "users" anyway, so it's optional here.

type User struct {
	orm.Model

	Email        string
	PasswordHash string
	Name         string
	Role         string
}

func (User) TableName() string { return "users" }

// --- Migration -----------------------------------------------------------

func init() {
	migrations.Register(migrations.Define("0001_users",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("users", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.String("email").Unique()
				t.String("password_hash")
				t.String("role").Default("user")
				t.Timestamps()
				t.SoftDeletes()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("users"))
		},
	))
}

// --- Service (framework-agnostic) ----------------------------------------

type UserService struct{ Conn *database.Connection }

func NewUserService(conn *database.Connection) *UserService { return &UserService{Conn: conn} }

func (s *UserService) Get(ctx context.Context, id uint64) (*User, error) {
	return orm.Query[User](s.Conn).Find(ctx, id)
}
func (s *UserService) Create(ctx context.Context, u *User) error { return orm.Save(ctx, s.Conn, u) }
func (s *UserService) Update(ctx context.Context, u *User) error { return orm.Save(ctx, s.Conn, u) }
func (s *UserService) Delete(ctx context.Context, u *User) error { return orm.Delete(ctx, s.Conn, u) }
func (s *UserService) Query() *orm.Builder[User]                 { return orm.Query[User](s.Conn) }

// --- Controller — exactly Laravel shape ---------------------------------

type UserController struct{ Service *UserService }

func NewUserController(conn *database.Connection) *UserController {
	return &UserController{Service: NewUserService(conn)}
}

func (c *UserController) Index(ctx *lagogin.Ctx) (any, error) {
	return lagogin.Paginate[User](ctx, c.Service.Query().OrderBy("id", "desc"))
}
func (c *UserController) Show(ctx *lagogin.Ctx) (any, error) {
	return c.Service.Get(ctx.Ctx(), ctx.ParamUint("id"))
}
func (c *UserController) Store(ctx *lagogin.Ctx) (any, error) {
	var u User
	if err := ctx.Bind(&u); err != nil {
		return nil, err
	}
	return ctx.Created(u), c.Service.Create(ctx.Ctx(), &u)
}
func (c *UserController) Update(ctx *lagogin.Ctx) (any, error) {
	u, err := c.Service.Get(ctx.Ctx(), ctx.ParamUint("id"))
	if err != nil {
		return nil, err
	}
	if err := ctx.Bind(u); err != nil {
		return nil, err
	}
	return u, c.Service.Update(ctx.Ctx(), u)
}
func (c *UserController) Destroy(ctx *lagogin.Ctx) (any, error) {
	u, err := c.Service.Get(ctx.Ctx(), ctx.ParamUint("id"))
	if err != nil {
		return nil, err
	}
	return nil, c.Service.Delete(ctx.Ctx(), u)
}

// --- main ----------------------------------------------------------------

func main() {
	ctx := context.Background()
	conn, err := database.Global.Open("default", database.Config{
		Driver: "sqlite",
		DSN:    "file::memory:?cache=shared",
	})
	if err != nil {
		log.Fatal(err)
	}
	conn = lagogin.Instrument(conn)
	if _, err := migrations.New(conn, nil, migrations.Options{}).Up(ctx); err != nil {
		log.Fatal(err)
	}

	authMgr, _ := auth.New(auth.Config{Secret: "dev-secret", AccessTTL: time.Hour})

	r := gin.Default()
	r.Use(lagogin.CORS("*"), lagogin.QueryLog(conn))

	// Public routes
	lagogin.Resource(r, "users", NewUserController(conn))

	// /api/* — JWT-protected group
	api := r.Group("/api", lagogin.AuthJWT(authMgr))
	api.GET("/me", lagogin.H(func(c *lagogin.Ctx) (any, error) {
		return map[string]any{"user_id": c.UserID(), "role": c.Role()}, nil
	}))

	// OpenAPI spec + Swagger UI
	lagogin.ServeOpenAPI(r, lagogin.SpecInfo{
		Title:       "Users API",
		Description: "lagogin demo — generated automatically from Resource()",
		Version:     "1.0",
	})

	log.Println("listening on :8080 — open http://localhost:8080/docs")
	log.Fatal(r.Run(":8080"))
}
