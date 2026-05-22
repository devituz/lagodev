// Chi integration example for lagodev.
//
// Run: `go run .` then curl http://localhost:8080/users.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

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

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/users", func(w http.ResponseWriter, r *http.Request) {
		u, err := svc.List(r.Context())
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 200, u)
	})
	r.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
		u, err := svc.Get(r.Context(), id)
		if errors.Is(err, orm.ErrNotFound) {
			writeErr(w, 404, err)
			return
		}
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 200, u)
	})
	r.Post("/users", func(w http.ResponseWriter, r *http.Request) {
		var u User
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			writeErr(w, 400, err)
			return
		}
		if err := svc.Save(r.Context(), &u); err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 201, u)
	})

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
