package openapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/devituz/lagodev/openapi"
)

// ExampleSpec demonstrates building an OpenAPI 3.1 document from Go types and
// serving it. Request/response bodies are described by structs whose json and
// validate tags drive the generated JSON Schema.
func ExampleSpec() {
	type CreateUser struct {
		Name  string `json:"name"  validate:"required,max=64"`
		Email string `json:"email" validate:"required,email"`
		Role  string `json:"role"  validate:"required,in=admin|user"`
	}
	type User struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	spec := openapi.New("Users API", "1.0.0")
	spec.AddServer("https://api.example.com", "production")

	spec.Operation(http.MethodPost, "/users", openapi.OperationConfig{
		Summary:     "Create a user",
		Tags:        []string{"users"},
		RequestBody: openapi.BodyOf[CreateUser](),
		Responses: map[int]openapi.Response{
			201: openapi.JSONResponse[User]("created"),
			422: openapi.TextResponse("validation failed"),
		},
	})
	spec.Operation(http.MethodGet, "/users/{id}", openapi.OperationConfig{
		Summary: "Get a user",
		Tags:    []string{"users"},
		Responses: map[int]openapi.Response{
			200: openapi.JSONResponse[User]("the user"),
			404: openapi.TextResponse("not found"),
		},
	})

	// Mount the JSON document and a Swagger-UI docs page.
	mux := http.NewServeMux()
	mux.Handle("/openapi.json", spec.Handler())
	mux.Handle("/docs", spec.DocsHandler("/openapi.json"))

	b, _ := spec.JSON()
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	fmt.Println("openapi:", doc["openapi"])
	comps := doc["components"].(map[string]any)["schemas"].(map[string]any)
	_, hasUser := comps["User"]
	_, hasCreate := comps["CreateUser"]
	fmt.Println("has User component:", hasUser)
	fmt.Println("has CreateUser component:", hasCreate)

	// Output:
	// openapi: 3.1.0
	// has User component: true
	// has CreateUser component: true
}
