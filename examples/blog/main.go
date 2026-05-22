// Blog: the canonical lagodev showcase.
//
// It wires up:
//
//   - 3 models (User, Post, Comment) with foreign keys
//   - 3 migrations (with up + down)
//   - 3 factories powered by faker
//   - 3 seeders with explicit dependencies and a topologically-sorted runner
//   - 1 framework-agnostic service (PostService) plus a net/http controller
//     that wraps it — drop the service into Gin/Fiber/Echo to compare
//   - A migration + seed step at startup so the binary is self-contained
//
// Try it:
//
//	cd examples/blog
//	go run .
//	curl http://localhost:8080/posts
//	curl http://localhost:8080/posts/1
//	curl -X POST -d '{"user_id":1,"title":"Hello","slug":"hello","body":"Hi"}' http://localhost:8080/posts
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/seeder"

	"github.com/devituz/lagodev/examples/blog/controllers"
	_ "github.com/devituz/lagodev/examples/blog/migrations" // registers migrations
	_ "github.com/devituz/lagodev/examples/blog/seeders"    // registers seeders
)

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
	if err := seeder.NewRunner(conn, nil, seeder.Options{}).Run(ctx); err != nil {
		log.Fatal(err)
	}

	posts := controllers.NewPostController(conn)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /posts", posts.Index)
	mux.HandleFunc("GET /posts/{id}", posts.Show)
	mux.HandleFunc("POST /posts", posts.Store)
	mux.HandleFunc("DELETE /posts/{id}", posts.Destroy)

	log.Println("blog ready on http://localhost:8080/posts")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
