// Gin integration example for lagodev.
//
// Demonstrates how to drop the lagodev ORM into a Gin server. The service
// layer below (UserService) is what `lago make:service` generates — fully
// framework-agnostic. Gin handlers are thin wrappers around it.
//
// Run:
//
//	go run .
//	curl -X POST http://localhost:8080/users \
//	    -d '{"name":"Ada","email":"ada@example.com"}'
//	curl http://localhost:8080/users
package main

import (
	"context"
	"errors"
	"log"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/schema"
)

// --- Model ---------------------------------------------------------------

type User struct {
	orm.Model
	Name  string `json:"name"`
	Email string `json:"email"`
}

// --- Migration -----------------------------------------------------------

func init() {
	migrations.Register(migrations.Define("0001_users",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("users", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.String("email").Unique()
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

func (s *UserService) List(ctx context.Context) ([]User, error) {
	var users []User
	err := orm.Query[User](s.Conn).OrderBy("id", "desc").Get(ctx, &users)
	return users, err
}

func (s *UserService) Get(ctx context.Context, id uint64) (*User, error) {
	return orm.Query[User](s.Conn).Find(ctx, id)
}

func (s *UserService) Create(ctx context.Context, u *User) error {
	return orm.Save(ctx, s.Conn, u)
}

func (s *UserService) Update(ctx context.Context, u *User) error {
	return orm.Save(ctx, s.Conn, u)
}

func (s *UserService) Delete(ctx context.Context, u *User) error {
	return orm.Delete(ctx, s.Conn, u)
}

// --- Gin handlers (the only framework-specific layer) --------------------

func main() {
	ctx := context.Background()
	conn, err := database.Global.Open("default", database.Config{
		Driver: "sqlite",
		DSN:    "file::memory:?cache=shared",
	})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := migrations.New(conn, nil, migrations.Options{}).Up(ctx); err != nil {
		log.Fatal(err)
	}
	svc := NewUserService(conn)

	r := gin.Default()
	r.GET("/users", func(c *gin.Context) {
		users, err := svc.List(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, users)
	})
	r.GET("/users/:id", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		u, err := svc.Get(c.Request.Context(), id)
		if errors.Is(err, orm.ErrNotFound) {
			c.JSON(404, gin.H{"error": "not found"})
			return
		}
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, u)
	})
	r.POST("/users", func(c *gin.Context) {
		var u User
		if err := c.ShouldBindJSON(&u); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if err := svc.Create(c.Request.Context(), &u); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(201, u)
	})
	r.PATCH("/users/:id", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		u, err := svc.Get(c.Request.Context(), id)
		if errors.Is(err, orm.ErrNotFound) {
			c.JSON(404, gin.H{"error": "not found"})
			return
		}
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		if err := c.ShouldBindJSON(u); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if err := svc.Update(c.Request.Context(), u); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, u)
	})
	r.DELETE("/users/:id", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		u, err := svc.Get(c.Request.Context(), id)
		if errors.Is(err, orm.ErrNotFound) {
			c.JSON(404, gin.H{"error": "not found"})
			return
		}
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		if err := svc.Delete(c.Request.Context(), u); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.Status(204)
	})

	log.Println("listening on :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatal(err)
	}
}
