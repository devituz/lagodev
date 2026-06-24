package cmd

import (
	"bytes"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devituz/lagodev/openapi"
)

// buildSpecJSON constructs a small OpenAPI spec via the openapi package and
// renders it to JSON, mirroring what `lago gen openapi` would produce.
func buildSpecJSON(t *testing.T) []byte {
	t.Helper()

	type CreateUser struct {
		Name  string `json:"name" validate:"required"`
		Email string `json:"email" validate:"required"`
	}
	type User struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	spec := openapi.New("Users API", "1.0.0")
	spec.AddServer("https://api.example.com", "production")

	spec.Operation(http.MethodGet, "/users", openapi.OperationConfig{
		Summary:     "List users",
		OperationID: "listUsers",
		Query: []openapi.Param{
			{Name: "page", Required: false},
		},
		Responses: map[int]openapi.Response{
			200: openapi.ListResponse[User]("ok"),
		},
	})
	spec.Operation(http.MethodPost, "/users", openapi.OperationConfig{
		Summary:     "Create user",
		OperationID: "createUser",
		RequestBody: openapi.BodyOf[CreateUser](),
		Responses: map[int]openapi.Response{
			201: openapi.JSONResponse[User]("created"),
		},
	})
	spec.Operation(http.MethodGet, "/users/{id}", openapi.OperationConfig{
		Summary:     "Show user",
		OperationID: "getUser",
		Responses: map[int]openapi.Response{
			200: openapi.JSONResponse[User]("ok"),
			404: openapi.TextResponse("not found"),
		},
	})

	data, err := spec.JSON()
	if err != nil {
		t.Fatalf("spec.JSON: %v", err)
	}
	return data
}

func runGenClient(t *testing.T, args ...string) string {
	t.Helper()
	c := NewGenClient(nil)
	c.SetArgs(args)
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput: %s", err, buf.String())
	}
	return buf.String()
}

func TestGenClient_GeneratesValidGoClient(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "openapi.json")
	if err := os.WriteFile(specPath, buildSpecJSON(t), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	out := filepath.Join(dir, "client")

	_ = runGenClient(t, "--spec", specPath, "--dir", out, "--package", "apiclient")

	path := filepath.Join(out, "client.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated client: %v", err)
	}
	body := string(data)

	for _, want := range []string{
		"package apiclient",
		"type Client struct",
		"func New(baseURL string) *Client",
		"const DefaultBaseURL = \"https://api.example.com\"",
		"type APIError struct",
		"func (c *Client) doRequest(",
		// one method per operation, named from operationId
		"func (c *Client) ListUsers(",
		"func (c *Client) CreateUser(",
		"func (c *Client) GetUser(",
		// typed request body + path param
		"body CreateUser)",
		"id string",
		// response struct from component schema
		"type User struct",
		"type CreateUser struct",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("client missing %q:\n%s", want, body)
		}
	}

	// Prove the generated source is syntactically valid Go.
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, path, data, parser.AllErrors); err != nil {
		t.Fatalf("generated client does not parse:\n%v\n\n%s", err, body)
	}
}

func TestGenClient_RequiresSpec(t *testing.T) {
	c := NewGenClient(nil)
	c.SetArgs([]string{"--dir", t.TempDir()})
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	if err := c.Execute(); err == nil {
		t.Fatal("expected error when --spec is missing")
	}
}

func TestGenClient_RefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "openapi.json")
	if err := os.WriteFile(specPath, buildSpecJSON(t), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	out := filepath.Join(dir, "client")
	_ = runGenClient(t, "--spec", specPath, "--dir", out)

	c := NewGenClient(nil)
	c.SetArgs([]string{"--spec", specPath, "--dir", out})
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	if err := c.Execute(); err == nil {
		t.Fatal("expected error overwriting without --force")
	}

	// --force succeeds.
	_ = runGenClient(t, "--spec", specPath, "--dir", out, "--force")
}
