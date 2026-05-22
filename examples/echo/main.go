// Echo integration example for lagodev.
//
// Run: `go run .` then curl http://localhost:8080/users.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

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

	e := echo.New()

	e.GET("/users", func(c echo.Context) error {
		u, err := svc.List(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, u)
	})
	e.GET("/users/:id", func(c echo.Context) error {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		u, err := svc.Get(c.Request().Context(), id)
		if errors.Is(err, orm.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, u)
	})
	e.POST("/users", func(c echo.Context) error {
		var u User
		if err := c.Bind(&u); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		if err := svc.Save(c.Request().Context(), &u); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusCreated, u)
	})
	e.DELETE("/users/:id", func(c echo.Context) error {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		u, err := svc.Get(c.Request().Context(), id)
		if errors.Is(err, orm.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		if err := svc.Delete(c.Request().Context(), u); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.NoContent(http.StatusNoContent)
	})

	log.Println("listening on :8080")
	log.Fatal(e.Start(":8080"))
}
