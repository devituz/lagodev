package lagogin

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// SpecInfo is the OpenAPI 3.0 info object — minimum metadata for a spec.
type SpecInfo struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version"`
}

// OpenAPI returns an OpenAPI 3.0 spec built from the Resource() registry.
// Each registered resource produces the canonical 5 endpoints plus a JSON
// schema (id-only by default — apps can pass a Models map to enrich it).
//
//	spec := lagogin.OpenAPI(lagogin.SpecInfo{Title: "MyAPI", Version: "1.0"})
//	r.GET("/openapi.json", lagogin.H(func(c *lagogin.Ctx) (any, error) { return spec, nil }))
//
// To customize per-resource schemas (request/response bodies), pass a
// Schemas map keyed by resource name (the plural used in Resource()):
//
//	lagogin.OpenAPI(info, lagogin.WithSchema("posts", PostSchema()))
func OpenAPI(info SpecInfo, opts ...Option) map[string]any {
	cfg := config{Info: info, Schemas: map[string]any{}}
	for _, o := range opts {
		o(&cfg)
	}
	paths := map[string]any{}
	for _, r := range Routes() {
		// The registry stores gin-native paths (`/posts/:id`). OpenAPI
		// requires `{param}` syntax, so convert at generation time.
		specPath := ginToOpenAPIPath(r.Path)
		path, ok := paths[specPath].(map[string]any)
		if !ok {
			path = map[string]any{}
			paths[specPath] = path
		}
		path[strings.ToLower(r.Method)] = operationFor(r, cfg)
	}
	spec := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       cfg.Info.Title,
			"description": cfg.Info.Description,
			"version":     cfg.Info.Version,
		},
		"paths": paths,
	}
	if len(cfg.Schemas) > 0 {
		spec["components"] = map[string]any{"schemas": cfg.Schemas}
	}
	return spec
}

// Option configures OpenAPI().
type Option func(*config)

type config struct {
	Info    SpecInfo
	Schemas map[string]any
}

// WithSchema attaches a JSON schema for the named resource. Keys may be a
// resource name ("posts") or a model name ("Post").
func WithSchema(name string, schema any) Option {
	return func(c *config) { c.Schemas[name] = schema }
}

// WriteOpenAPI serializes the spec to a file at path. Convenient for
// committing a spec alongside the codebase.
func WriteOpenAPI(path string, info SpecInfo, opts ...Option) error {
	spec := OpenAPI(info, opts...)
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// ServeOpenAPI mounts /openapi.json and a simple /docs HTML page on r.
// The HTML uses Swagger UI from a public CDN; for offline environments,
// serve the JSON yourself and host UI assets locally.
func ServeOpenAPI(r IRouter, info SpecInfo, opts ...Option) {
	spec := OpenAPI(info, opts...)
	r.GET("/openapi.json", func(c *gin.Context) {
		c.JSON(http.StatusOK, spec)
	})
	r.GET("/docs", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		_, _ = c.Writer.WriteString(swaggerHTML(info.Title))
	})
}

// ginToOpenAPIPath rewrites gin path params (`/posts/:id`) into the
// OpenAPI `{param}` form (`/posts/{id}`).
func ginToOpenAPIPath(p string) string {
	if !strings.Contains(p, ":") {
		return p
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") && len(s) > 1 {
			segs[i] = "{" + s[1:] + "}"
		}
	}
	return strings.Join(segs, "/")
}

func operationFor(r RouteEntry, _ config) map[string]any {
	summary := strings.Title(r.Op) + " " + r.Resource
	op := map[string]any{
		"summary": summary,
		"tags":    []string{r.Resource},
	}
	if strings.Contains(r.Path, ":id") || strings.Contains(r.Path, "{id}") {
		op["parameters"] = []map[string]any{
			{
				"name":     "id",
				"in":       "path",
				"required": true,
				"schema":   map[string]any{"type": "integer", "format": "int64"},
			},
		}
	}
	responses := map[string]any{}
	switch r.Op {
	case "index":
		responses["200"] = map[string]any{"description": "list"}
	case "show":
		responses["200"] = map[string]any{"description": "single record"}
		responses["404"] = map[string]any{"description": "not found"}
	case "store":
		responses["201"] = map[string]any{"description": "created"}
		responses["422"] = map[string]any{"description": "validation failed"}
	case "update":
		responses["200"] = map[string]any{"description": "updated"}
		responses["404"] = map[string]any{"description": "not found"}
	case "destroy":
		responses["204"] = map[string]any{"description": "deleted"}
	default:
		responses["200"] = map[string]any{"description": "ok"}
	}
	op["responses"] = responses
	return op
}

func swaggerHTML(title string) string {
	return `<!DOCTYPE html>
<html>
<head>
  <title>` + title + ` — API docs</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({ url: '/openapi.json', dom_id: '#swagger-ui' });
  </script>
</body>
</html>`
}
