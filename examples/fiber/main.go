// Fiber integration example for lagodev.
//
// Same service shape as the Gin/Echo/Chi examples — only the HTTP layer
// changes. Run with: `go run .` and curl http://localhost:8080/users.
package main

import (
	"context"
	"errors"
	"log"
	"strconv"

	"github.com/gofiber/fiber/v2"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/schema"
)

type User struct {
	orm.Model
	Name  string `json:"name"`
	Email string `json:"email"`
}

func init() {
	migrations.Register(migrations.Define("0001_users",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("users", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.String("email").Unique()
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("users"))
		},
	))
}

type UserService struct{ Conn *database.Connection }

func (s *UserService) List(ctx context.Context) ([]User, error) {
	var u []User
	err := orm.Query[User](s.Conn).OrderBy("id", "desc").Get(ctx, &u)
	return u, err
}
func (s *UserService) Get(ctx context.Context, id uint64) (*User, error) {
	return orm.Query[User](s.Conn).Find(ctx, id)
}
func (s *UserService) Save(ctx context.Context, u *User) error { return orm.Save(ctx, s.Conn, u) }
func (s *UserService) Delete(ctx context.Context, u *User) error {
	return orm.Delete(ctx, s.Conn, u)
}

func main() {
	ctx := context.Background()
	conn, err := database.Global.Open("default", database.Config{
		Driver: "sqlite", DSN: "file::memory:?cache=shared",
	})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := migrations.New(conn, nil, migrations.Options{}).Up(ctx); err != nil {
		log.Fatal(err)
	}
	svc := &UserService{Conn: conn}

	app := fiber.New()

	app.Get("/users", func(c *fiber.Ctx) error {
		u, err := svc.List(c.Context())
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(u)
	})
	app.Get("/users/:id", func(c *fiber.Ctx) error {
		id, _ := strconv.ParseUint(c.Params("id"), 10, 64)
		u, err := svc.Get(c.Context(), id)
		if errors.Is(err, orm.ErrNotFound) {
			return c.Status(404).JSON(fiber.Map{"error": "not found"})
		}
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(u)
	})
	app.Post("/users", func(c *fiber.Ctx) error {
		var u User
		if err := c.BodyParser(&u); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		if err := svc.Save(c.Context(), &u); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.Status(201).JSON(u)
	})
	app.Delete("/users/:id", func(c *fiber.Ctx) error {
		id, _ := strconv.ParseUint(c.Params("id"), 10, 64)
		u, err := svc.Get(c.Context(), id)
		if errors.Is(err, orm.ErrNotFound) {
			return c.Status(404).JSON(fiber.Map{"error": "not found"})
		}
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if err := svc.Delete(c.Context(), u); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.SendStatus(204)
	})

	log.Println("listening on :8080")
	log.Fatal(app.Listen(":8080"))
}
