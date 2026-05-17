# Framework integration

lagodev does **not** ship a router. It plugs into whatever HTTP/RPC
framework you already use. The integration is always the same shape:

```
your handler  →  *Service struct  →  *database.Connection  →  database
```

The service returns plain Go values + errors. The handler does the
framework-specific marshalling. That's it.

## Generate the layout

```bash
lago make:model Post -mfsc --fields="title:string,body:text,published:bool:default(false)"
```

You get:

```
models/post.go              // type Post struct { orm.Model; … }
migrations/<ts>_*.go        // schema.Create("posts", …)
factories/post_factory.go   // factory.New[Post]
seeders/post_seeder.go      // optional fixture data
services/post_service.go    // List/Get/Create/Update/Delete with context+error
controllers/post_controller.go  // net/http handlers wrapping the service
```

Below we wire the **same service** into five different frameworks.

## Gin

```go
import "github.com/gin-gonic/gin"

svc := services.NewPostService(conn)
r := gin.Default()

r.GET("/posts", func(c *gin.Context) {
    posts, err := svc.List(c.Request.Context())
    if err != nil { c.JSON(500, gin.H{"error": err.Error()}); return }
    c.JSON(200, posts)
})

r.POST("/posts", func(c *gin.Context) {
    var p models.Post
    if err := c.ShouldBindJSON(&p); err != nil { c.JSON(400, gin.H{"error": err.Error()}); return }
    if err := svc.Create(c.Request.Context(), &p); err != nil { c.JSON(500, gin.H{"error": err.Error()}); return }
    c.JSON(201, p)
})

r.Run(":8080")
```

Full runnable example: [`examples/gin/`](../examples/gin).

## Fiber

```go
import "github.com/gofiber/fiber/v2"

app := fiber.New()
app.Get("/posts", func(c *fiber.Ctx) error {
    posts, err := svc.List(c.Context())
    if err != nil { return c.Status(500).JSON(fiber.Map{"error": err.Error()}) }
    return c.JSON(posts)
})
app.Listen(":8080")
```

Full example: [`examples/fiber/`](../examples/fiber).

## Echo

```go
import "github.com/labstack/echo/v4"

e := echo.New()
e.GET("/posts", func(c echo.Context) error {
    posts, err := svc.List(c.Request().Context())
    if err != nil { return c.JSON(500, map[string]string{"error": err.Error()}) }
    return c.JSON(200, posts)
})
e.Start(":8080")
```

Full example: [`examples/echo/`](../examples/echo).

## Chi

```go
import "github.com/go-chi/chi/v5"

r := chi.NewRouter()
r.Get("/posts", func(w http.ResponseWriter, r *http.Request) {
    posts, err := svc.List(r.Context())
    if err != nil { writeErr(w, 500, err); return }
    writeJSON(w, 200, posts)
})
http.ListenAndServe(":8080", r)
```

Full example: [`examples/chi/`](../examples/chi).

## Plain net/http (Go 1.22+)

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /posts", func(w http.ResponseWriter, r *http.Request) {
    posts, err := svc.List(r.Context())
    // ...
})
mux.HandleFunc("GET /posts/{id}", postsCtrl.Show)
http.ListenAndServe(":8080", mux)
```

The generated controllers ALREADY use `net/http` signatures, so this
pattern is the most direct: `mux.HandleFunc("GET /posts", ctrl.Index)`.

## gRPC

```go
type PostServer struct {
    pb.UnimplementedPostServiceServer
    svc *services.PostService
}

func (s *PostServer) ListPosts(ctx context.Context, _ *pb.ListPostsRequest) (*pb.ListPostsResponse, error) {
    items, err := s.svc.List(ctx)
    if err != nil { return nil, err }
    out := &pb.ListPostsResponse{Posts: make([]*pb.Post, len(items))}
    for i, p := range items { out.Posts[i] = toProto(&p) }
    return out, nil
}
```

Full example: [`examples/grpc/`](../examples/grpc).

## GraphQL (gqlgen / 99designs)

```go
type Resolver struct { Posts *services.PostService }

func (r *queryResolver) Posts(ctx context.Context) ([]*model.Post, error) {
    items, err := r.Posts.List(ctx)
    if err != nil { return nil, err }
    out := make([]*model.Post, len(items))
    for i := range items { out[i] = toGQL(&items[i]) }
    return out, nil
}
```

## Background jobs / cron / queue workers

The service is just as happy in a long-running goroutine:

```go
for job := range queue {
    if err := svc.Process(ctx, job); err != nil { log.Println(err) }
}
```

See [`examples/microservice/`](../examples/microservice) for a
locking-aware queue worker.

## When you really do need framework-specific code in the service

You don't. If a request needs file uploads, multipart, or pagination
metadata, push the framework-specific decoding into the handler and call
the service with the parsed values:

```go
// handler (Gin):
r.POST("/upload", func(c *gin.Context) {
    file, _ := c.FormFile("avatar")
    open, _ := file.Open()
    defer open.Close()
    err := svc.UploadAvatar(c.Request.Context(), userID, open, file.Size)
    if err != nil { c.JSON(500, gin.H{"error": err.Error()}); return }
    c.Status(204)
})

// service (UploadAvatar takes io.Reader + size — no Gin types):
func (s *UserService) UploadAvatar(ctx context.Context, id uint64, r io.Reader, size int64) error { ... }
```

That's the discipline that keeps your business logic portable.
