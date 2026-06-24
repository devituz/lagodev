// Package openapi generates a valid OpenAPI 3.1 document for a lagodev
// application using only the standard library and reflection.
//
// It has three layers:
//
//   - Struct -> JSON Schema: SchemaOf[T] / SchemaFor reflect over a Go type,
//     honouring json and validate struct tags (see schema.go, validate.go).
//   - Spec builder: New(title, version) returns a *Spec onto which operations
//     are declared with Operation / a fluent OperationBuilder. Request and
//     response bodies are described by Go types and registered as reusable
//     component schemas referenced by $ref.
//   - Serve: Spec.JSON renders the document; Spec.Handler and Spec.DocsHandler
//     return net/http handlers serving the JSON and a Swagger-UI page.
//
// # Example
//
//	type CreateUser struct {
//	    Name  string `json:"name"  validate:"required,max=64"`
//	    Email string `json:"email" validate:"required,email"`
//	    Role  string `json:"role"  validate:"required,in=admin|user"`
//	}
//	type User struct {
//	    ID    int    `json:"id"`
//	    Name  string `json:"name"`
//	    Email string `json:"email"`
//	}
//
//	spec := openapi.New("Users API", "1.0.0")
//	spec.AddServer("https://api.example.com", "production")
//	spec.Operation(http.MethodPost, "/users", openapi.OperationConfig{
//	    Summary:     "Create a user",
//	    Tags:        []string{"users"},
//	    RequestBody: openapi.BodyOf[CreateUser](),
//	    Responses: map[int]openapi.Response{
//	        201: openapi.JSONResponse[User]("created"),
//	        422: openapi.TextResponse("validation failed"),
//	    },
//	})
//	json, _ := spec.JSON()
//	http.Handle("/openapi.json", spec.Handler())
//	http.Handle("/docs", spec.DocsHandler())
package openapi

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// itoa is strconv.Itoa, kept local to read naturally at call sites that format
// HTTP status codes as response-object keys.
func itoa(n int) string { return strconv.Itoa(n) }

// jsonUnmarshal is an indirection so schema.go's typeField can unmarshal
// without importing encoding/json there (keeps that file dependency-light).
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// Spec is an in-progress OpenAPI 3.1 document. Build it with New, attach
// operations, then render with JSON or serve with Handler.
type Spec struct {
	Title       string
	Version     string
	Description string

	servers  []server
	paths    map[string]map[string]*operation // path -> lower(method) -> op
	registry *Registry
}

type server struct {
	URL         string
	Description string
}

// New returns an empty Spec for the given API title and version.
func New(title, version string) *Spec {
	return &Spec{
		Title:    title,
		Version:  version,
		paths:    map[string]map[string]*operation{},
		registry: newRegistry(),
	}
}

// AddServer appends a server URL (with optional description) to the document.
func (s *Spec) AddServer(url, description string) *Spec {
	s.servers = append(s.servers, server{URL: url, Description: description})
	return s
}

// Registry exposes the component-schema registry so callers can pre-register
// types or inspect generated schemas.
func (s *Spec) Registry() *Registry { return s.registry }

// Param describes a single path or query parameter.
type Param struct {
	Name        string
	Description string
	Required    bool
	Schema      *Schema // defaults to {type:string} when nil
}

// Body describes a request body: the schema (usually via BodyOf[T]) plus its
// media type (defaults to application/json) and required flag.
type Body struct {
	Description string
	Required    bool
	MediaType   string // default application/json
	typ         reflect.Type
	schema      *Schema
}

// BodyOf builds a request Body from a Go type T. The type is registered as a
// component schema and referenced by $ref.
func BodyOf[T any]() *Body {
	return &Body{Required: true, typ: reflect.TypeOf((*T)(nil)).Elem()}
}

// RawBody builds a request Body from an explicit schema (no Go type).
func RawBody(schema *Schema) *Body {
	return &Body{Required: true, schema: schema}
}

// Response describes one HTTP response: a human description plus an optional
// body schema and media type.
type Response struct {
	Description string
	MediaType   string // default application/json when a schema is present
	typ         reflect.Type
	schema      *Schema
}

// JSONResponse builds a JSON response whose body is described by Go type T.
func JSONResponse[T any](description string) Response {
	return Response{Description: description, typ: reflect.TypeOf((*T)(nil)).Elem()}
}

// ListResponse builds a JSON response whose body is an array of T.
func ListResponse[T any](description string) Response {
	t := reflect.TypeOf((*T)(nil)).Elem()
	return Response{Description: description, typ: reflect.SliceOf(t)}
}

// TextResponse builds a bodyless response carrying only a description (e.g. for
// 204 or error statuses).
func TextResponse(description string) Response {
	return Response{Description: description}
}

// OperationConfig is the declarative form passed to Spec.Operation.
type OperationConfig struct {
	Summary     string
	Description string
	Tags        []string
	OperationID string

	// PathParams documents path parameters. Any {id}/:id segment in the path
	// that is not listed here is auto-added as a required string parameter.
	PathParams []Param
	Query      []Param

	RequestBody *Body
	Responses   map[int]Response
}

// operation is the assembled internal form of an OpenAPI operation object.
type operation struct {
	Summary     string                 `json:"summary,omitempty"`
	Description string                 `json:"description,omitempty"`
	Tags        []string               `json:"tags,omitempty"`
	OperationID string                 `json:"operationId,omitempty"`
	Parameters  []parameter            `json:"parameters,omitempty"`
	RequestBody *requestBody           `json:"requestBody,omitempty"`
	Responses   map[string]responseObj `json:"responses"`
}

type parameter struct {
	Name        string  `json:"name"`
	In          string  `json:"in"`
	Description string  `json:"description,omitempty"`
	Required    bool    `json:"required"`
	Schema      *Schema `json:"schema,omitempty"`
}

type requestBody struct {
	Description string               `json:"description,omitempty"`
	Required    bool                 `json:"required,omitempty"`
	Content     map[string]mediaType `json:"content"`
}

type responseObj struct {
	Description string               `json:"description"`
	Content     map[string]mediaType `json:"content,omitempty"`
}

type mediaType struct {
	Schema *Schema `json:"schema,omitempty"`
}

// Operation declares an operation on the spec. Path params are auto-derived
// from {id}/:id segments and merged with explicit PathParams docs. Request and
// response bodies are resolved to component $refs via the registry.
func (s *Spec) Operation(method, path string, cfg OperationConfig) *Spec {
	op := &operation{
		Summary:     cfg.Summary,
		Description: cfg.Description,
		Tags:        cfg.Tags,
		OperationID: cfg.OperationID,
		Responses:   map[string]responseObj{},
	}

	// Path parameters: explicit docs first, then auto-fill any path placeholder
	// not already documented.
	documented := map[string]bool{}
	for _, p := range cfg.PathParams {
		op.Parameters = append(op.Parameters, toParameter(p, "path", true))
		documented[p.Name] = true
	}
	for _, name := range PathParams(path) {
		if !documented[name] {
			op.Parameters = append(op.Parameters, parameter{
				Name: name, In: "path", Required: true,
				Schema: &Schema{Type: typeField{"string"}},
			})
		}
	}
	for _, q := range cfg.Query {
		op.Parameters = append(op.Parameters, toParameter(q, "query", q.Required))
	}

	if b := cfg.RequestBody; b != nil {
		op.RequestBody = s.buildRequestBody(b)
	}

	// Default an empty response set to a 200 so the operation is valid.
	if len(cfg.Responses) == 0 {
		op.Responses["200"] = responseObj{Description: "OK"}
	}
	for code, r := range cfg.Responses {
		op.Responses[itoa(code)] = s.buildResponse(r)
	}

	specPath := NormalizePath(path)
	if s.paths[specPath] == nil {
		s.paths[specPath] = map[string]*operation{}
	}
	s.paths[specPath][strings.ToLower(method)] = op
	return s
}

func (s *Spec) buildRequestBody(b *Body) *requestBody {
	schema := b.schema
	if schema == nil && b.typ != nil {
		schema = schemaFor(b.typ, s.registry)
	}
	media := b.MediaType
	if media == "" {
		media = "application/json"
	}
	return &requestBody{
		Description: b.Description,
		Required:    b.Required,
		Content:     map[string]mediaType{media: {Schema: schema}},
	}
}

func (s *Spec) buildResponse(r Response) responseObj {
	out := responseObj{Description: r.Description}
	if out.Description == "" {
		out.Description = "OK"
	}
	schema := r.schema
	if schema == nil && r.typ != nil {
		schema = schemaFor(r.typ, s.registry)
	}
	if schema != nil {
		media := r.MediaType
		if media == "" {
			media = "application/json"
		}
		out.Content = map[string]mediaType{media: {Schema: schema}}
	}
	return out
}

func toParameter(p Param, in string, required bool) parameter {
	sch := p.Schema
	if sch == nil {
		sch = &Schema{Type: typeField{"string"}}
	}
	return parameter{
		Name:        p.Name,
		In:          in,
		Description: p.Description,
		Required:    required,
		Schema:      sch,
	}
}

// document is the top-level marshalled shape of an OpenAPI 3.1 document.
type document struct {
	OpenAPI    string                           `json:"openapi"`
	Info       infoObj                          `json:"info"`
	Servers    []serverObj                      `json:"servers,omitempty"`
	Paths      map[string]map[string]*operation `json:"paths"`
	Components *componentsObj                   `json:"components,omitempty"`
}

type infoObj struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version"`
}

type serverObj struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

type componentsObj struct {
	Schemas map[string]*Schema `json:"schemas"`
}

// build assembles the marshalled document. Paths and methods are emitted as
// maps (JSON object order is not significant); component schemas are included
// whenever the registry recorded any.
func (s *Spec) build() document {
	doc := document{
		OpenAPI: "3.1.0",
		Info: infoObj{
			Title:       s.Title,
			Description: s.Description,
			Version:     s.Version,
		},
		Paths: s.paths,
	}
	for _, sv := range s.servers {
		doc.Servers = append(doc.Servers, serverObj{URL: sv.URL, Description: sv.Description})
	}
	if names := s.registry.names(); len(names) > 0 {
		schemas := make(map[string]*Schema, len(names))
		for _, n := range names {
			schemas[n] = s.registry.schemas[n]
		}
		doc.Components = &componentsObj{Schemas: schemas}
	}
	return doc
}

// Tags returns the distinct operation tags across the spec, sorted. Useful for
// building a tags section or a docs index.
func (s *Spec) Tags() []string { return s.sortedTags() }

// JSON renders the document as indented JSON bytes (valid OpenAPI 3.1).
func (s *Spec) JSON() ([]byte, error) {
	return json.MarshalIndent(s.build(), "", "  ")
}

// Map returns the document as a generic map (via a JSON round-trip), useful for
// callers that want to merge or post-process before serving.
func (s *Spec) Map() (map[string]any, error) {
	b, err := s.JSON()
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// sortedTags collects the distinct tags used across all operations, sorted.
func (s *Spec) sortedTags() []string {
	set := map[string]struct{}{}
	for _, methods := range s.paths {
		for _, op := range methods {
			for _, t := range op.Tags {
				set[t] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
