// Package lagogin is the official Gin adapter for lagodev.
//
// It mirrors the Laravel-style DX of the `web` package — `(any, error)`
// handler signature, automatic status mapping, Resource() one-liner,
// JWT middleware, pagination, validation, and OpenAPI generation — but
// builds on top of github.com/gin-gonic/gin instead of net/http.
//
// Quick start:
//
//	r := gin.Default()
//	r.Use(lagogin.QueryLog(conn)) // X-DB-Query-Count header
//
//	lagogin.Resource(r, "posts", &PostController{Conn: conn})
//
//	api := r.Group("/api", lagogin.AuthJWT(authManager))
//	api.GET("/me", lagogin.H(meHandler))
//
//	r.GET("/posts", lagogin.H(func(c *lagogin.Ctx) (any, error) {
//	    return c.Paginate(orm.Query[Post](conn).OrderBy("id", "desc"))
//	}))
//
// The handler signature `func(*lagogin.Ctx) (any, error)` removes the
// repetitive `if err != nil { c.JSON(...) }` boilerplate: orm.ErrNotFound
// is automatically mapped to 404, other errors to 500, nil to 204, and
// any returned value is JSON-encoded with 200 (or whatever Status() set).
package lagogin
