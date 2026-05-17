# Blog — the lagodev showcase

End-to-end blog API demonstrating **every** lagodev feature in one project:

- 3 models with foreign keys + soft deletes
- 3 timestamped migrations (with `down`)
- 3 factories (faker-powered)
- 3 seeders with explicit dependencies (topological order)
- A framework-agnostic `PostService` (CRUD + eager loading + transactions)
- A net/http controller that wraps the service (drop-in compatible with
  Gin/Fiber/Echo — see sibling examples)
- A per-project `lago.json` mapping `models/`, `migrations/`, `factories/`,
  `seeders/`, `tests/` to custom paths

## Run

```bash
cd examples/blog
go mod tidy
go run .
```

The binary applies migrations and runs seeders at startup, so you get a
populated database immediately.

```bash
curl http://localhost:8080/posts            # 15 seeded posts with author
curl http://localhost:8080/posts/1          # auto-increments view counter
curl -X POST http://localhost:8080/posts \
    -d '{"user_id":1,"title":"Hi","slug":"hi","body":"Hello"}'
curl -X DELETE http://localhost:8080/posts/3   # soft-delete
```

## Layout

```
blog/
├── main.go                 entry point
├── lago.json               per-project paths (read by `lago` CLI)
├── models/
│   ├── user.go             User + TableName
│   ├── post.go             Post (FK to user, slug unique, views, pinned)
│   └── comment.go          Comment (FK to post + user)
├── migrations/             Up + Down for each table, registered in init()
├── factories/              gofakeit-powered builders
├── seeders/                run order: UserSeeder → PostSeeder → CommentSeeder
├── services/post_service.go  framework-agnostic CRUD + IncrementViews
└── controllers/post_controller.go  net/http wrappers
```

## Want to generate the same scaffold yourself?

```bash
go install github.com/devituz/lagodev/cmd/lago@latest

lago init
lago make:model User -mfsc --fields="name:string,email:string:unique"
lago make:model Post -mfsc --fields="user_id:bigint,title:string,slug:string:unique,body:text,views:int:default(0),pinned:bool:default(false)"
lago make:model Comment -mfsc --fields="post_id:bigint,user_id:bigint,body:text"
```

Now you have **exactly** this project structure, with field types in
lockstep between every artifact.
