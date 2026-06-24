package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devituz/lagodev/internal/inflect"
)

// NewGenClient returns `gen:client` — generates a typed Go HTTP client from an
// OpenAPI 3.x document produced by the openapi package (or any compliant spec).
//
//	artisan gen:client --spec openapi.json --dir client --package client
//
// The generator reads the JSON spec, turns every component schema into a Go
// struct, and emits one method per operation on a Client struct. Each method
// has typed path/query parameters, a typed request body (when present) and a
// typed response struct, with JSON encode/decode and HTTP error handling.
func NewGenClient(_ *Env) *cobra.Command {
	var (
		specPath string
		dir      string
		pkg      string
		baseURL  string
		force    bool
	)
	c := &cobra.Command{
		Use:   "gen:client",
		Short: "Generate a typed Go HTTP client from an OpenAPI spec",
		Long: `Reads an OpenAPI 3.x JSON document (as produced by the openapi package)
and generates a typed Go client: a Client struct holding a base URL and an
*http.Client, one method per operation with typed parameters and response
structs, plus component-schema structs. JSON bodies are encoded/decoded and
non-2xx responses are returned as errors.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if specPath == "" {
				return fmt.Errorf("gen:client: --spec is required")
			}
			data, err := os.ReadFile(specPath)
			if err != nil {
				return fmt.Errorf("gen:client: read spec: %w", err)
			}
			doc, err := parseOpenAPI(data)
			if err != nil {
				return fmt.Errorf("gen:client: parse spec: %w", err)
			}
			if pkg == "" {
				pkg = pkgFromOutDir(dir)
			}
			src, err := generateClient(doc, pkg, baseURL)
			if err != nil {
				return fmt.Errorf("gen:client: %w", err)
			}
			path := filepath.Join(dir, "client.go")
			if err := writeFile(path, src, force); err != nil {
				return err
			}
			printCreated(cmd, path)
			return nil
		},
	}
	c.Flags().StringVar(&specPath, "spec", "", "path to the OpenAPI JSON document (required)")
	c.Flags().StringVar(&dir, "dir", "client", "output directory")
	c.Flags().StringVar(&pkg, "package", "", "package name (default: derived from --dir)")
	c.Flags().StringVar(&baseURL, "base-url", "", "default base URL (default: first server in spec)")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return c
}

// ---------------------------------------------------------------------------
// OpenAPI document model (the subset the generator consumes).
//
// This mirrors the JSON shape emitted by the openapi package's Spec.JSON; only
// the fields the client generator needs are modelled. Schemas use $ref into
// components, so a resolver walks refs back to named component types.
// ---------------------------------------------------------------------------

type oaDoc struct {
	Servers    []oaServer                 `json:"servers"`
	Paths      map[string]map[string]oaOp `json:"paths"`
	Components struct {
		Schemas map[string]*oaSchema `json:"schemas"`
	} `json:"components"`
}

type oaServer struct {
	URL string `json:"url"`
}

type oaOp struct {
	OperationID string            `json:"operationId"`
	Summary     string            `json:"summary"`
	Tags        []string          `json:"tags"`
	Parameters  []oaParam         `json:"parameters"`
	RequestBody *oaReqBody        `json:"requestBody"`
	Responses   map[string]oaResp `json:"responses"`
}

type oaParam struct {
	Name     string    `json:"name"`
	In       string    `json:"in"`
	Required bool      `json:"required"`
	Schema   *oaSchema `json:"schema"`
}

type oaReqBody struct {
	Required bool               `json:"required"`
	Content  map[string]oaMedia `json:"content"`
}

type oaResp struct {
	Content map[string]oaMedia `json:"content"`
}

type oaMedia struct {
	Schema *oaSchema `json:"schema"`
}

type oaSchema struct {
	Ref                  string               `json:"$ref"`
	Type                 typeOrArray          `json:"type"`
	Format               string               `json:"format"`
	Properties           map[string]*oaSchema `json:"properties"`
	Required             []string             `json:"required"`
	Items                *oaSchema            `json:"items"`
	AdditionalProperties *oaSchema            `json:"additionalProperties"`
}

// typeOrArray decodes an OpenAPI "type" that may be a bare string or, under the
// 3.1 nullable idiom, an array such as ["string","null"].
type typeOrArray []string

func (t *typeOrArray) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		*t = nil
		return nil
	}
	if s[0] == '[' {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*t = arr
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*t = typeOrArray{one}
	return nil
}

// primary returns the first non-"null" type, or "" when none.
func (t typeOrArray) primary() string {
	for _, x := range t {
		if x != "null" {
			return x
		}
	}
	return ""
}

func parseOpenAPI(data []byte) (*oaDoc, error) {
	var doc oaDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Paths) == 0 {
		return nil, fmt.Errorf("spec has no paths")
	}
	return &doc, nil
}

// ---------------------------------------------------------------------------
// Code generation.
// ---------------------------------------------------------------------------

// genMethod is the resolved shape of one client method.
type genMethod struct {
	Name        string     // exported Go method name
	Comment     string     // doc comment body (summary or default)
	HTTPMethod  string     // http.MethodGet, ...
	PathFmt     string     // path with %s placeholders for path params
	PathArgs    []string   // Go expressions feeding the fmt.Sprintf
	PathParams  []genParam // typed path params (method args)
	QueryParams []genParam // typed query params (method args)
	HasBody     bool
	BodyType    string // Go type for the request body argument
	RespType    string // Go type for the decoded response, "" when none
}

type genParam struct {
	GoName string // method argument name (camelCase)
	GoType string // Go type
	WName  string // wire name (path placeholder / query key)
}

func generateClient(doc *oaDoc, pkg, baseURL string) (string, error) {
	if baseURL == "" && len(doc.Servers) > 0 {
		baseURL = doc.Servers[0].URL
	}

	// Resolve component schemas into Go struct declarations, in stable order.
	structNames := make([]string, 0, len(doc.Components.Schemas))
	for name := range doc.Components.Schemas {
		structNames = append(structNames, name)
	}
	sort.Strings(structNames)

	var b strings.Builder
	fmt.Fprintf(&b, "// Code generated by lago gen:client. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("import (\n")
	b.WriteString("\t\"bytes\"\n")
	b.WriteString("\t\"context\"\n")
	b.WriteString("\t\"encoding/json\"\n")
	b.WriteString("\t\"fmt\"\n")
	b.WriteString("\t\"io\"\n")
	b.WriteString("\t\"net/http\"\n")
	b.WriteString("\t\"net/url\"\n")
	b.WriteString("\t\"strings\"\n")
	b.WriteString(")\n\n")

	// Client struct + constructor + helpers.
	fmt.Fprintf(&b, "// DefaultBaseURL is the base URL baked in from the spec's first server.\n")
	fmt.Fprintf(&b, "const DefaultBaseURL = %q\n\n", baseURL)
	b.WriteString(`// Client is a typed HTTP client for the API.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// New returns a Client targeting baseURL (DefaultBaseURL when empty) using
// http.DefaultClient unless overridden via the Client fields.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTPClient: http.DefaultClient}
}

// APIError is returned for non-2xx responses.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api: unexpected status %d: %s", e.StatusCode, e.Body)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// doRequest performs the request, decoding a 2xx JSON body into out (when
// non-nil) and returning *APIError otherwise.
func (c *Client) doRequest(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if out != nil {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Body: string(data)}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

`)

	// Component schema structs.
	for _, name := range structNames {
		writeStruct(&b, name, doc.Components.Schemas[name])
		b.WriteString("\n")
	}

	// Operation methods, in stable (path, method) order.
	methods := resolveMethods(doc)
	for _, m := range methods {
		writeMethod(&b, m)
		b.WriteString("\n")
	}

	return b.String(), nil
}

// writeStruct emits a Go struct declaration for a component schema. Non-object
// schemas are emitted as a named type alias to keep references valid.
func writeStruct(b *strings.Builder, name string, s *oaSchema) {
	goName := inflect.Pascal(name)
	if s == nil || s.Type.primary() != "object" || len(s.Properties) == 0 {
		fmt.Fprintf(b, "// %s is a generated type for the %q schema.\n", goName, name)
		fmt.Fprintf(b, "type %s %s\n", goName, goType(s))
		return
	}
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	props := make([]string, 0, len(s.Properties))
	for p := range s.Properties {
		props = append(props, p)
	}
	sort.Strings(props)

	fmt.Fprintf(b, "// %s is a generated type for the %q schema.\n", goName, name)
	fmt.Fprintf(b, "type %s struct {\n", goName)
	for _, p := range props {
		field := inflect.Pascal(p)
		typ := goType(s.Properties[p])
		tag := p
		if !required[p] {
			tag += ",omitempty"
		}
		fmt.Fprintf(b, "\t%s %s `json:%q`\n", field, typ, tag)
	}
	b.WriteString("}\n")
}

// goType maps an OpenAPI schema to a Go type expression.
func goType(s *oaSchema) string {
	if s == nil {
		return "any"
	}
	if s.Ref != "" {
		return inflect.Pascal(refName(s.Ref))
	}
	switch s.Type.primary() {
	case "string":
		return "string"
	case "integer":
		if s.Format == "int32" {
			return "int32"
		}
		return "int64"
	case "number":
		if s.Format == "float" {
			return "float32"
		}
		return "float64"
	case "boolean":
		return "bool"
	case "array":
		return "[]" + goType(s.Items)
	case "object":
		if s.AdditionalProperties != nil {
			return "map[string]" + goType(s.AdditionalProperties)
		}
		return "map[string]any"
	default:
		return "any"
	}
}

func refName(ref string) string {
	i := strings.LastIndex(ref, "/")
	if i < 0 {
		return ref
	}
	return ref[i+1:]
}

// resolveMethods turns the document's paths/operations into resolved method
// descriptors in a deterministic order.
func resolveMethods(doc *oaDoc) []genMethod {
	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var out []genMethod
	for _, path := range paths {
		ops := doc.Paths[path]
		verbs := make([]string, 0, len(ops))
		for v := range ops {
			verbs = append(verbs, v)
		}
		sort.Strings(verbs)
		for _, verb := range verbs {
			out = append(out, resolveOp(path, verb, ops[verb]))
		}
	}
	return out
}

func resolveOp(path, verb string, op oaOp) genMethod {
	m := genMethod{
		Name:       methodName(op.OperationID, path, verb),
		Comment:    op.Summary,
		HTTPMethod: httpMethodConst(verb),
	}
	if m.Comment == "" {
		m.Comment = strings.ToUpper(verb) + " " + path
	}

	// Path: replace each {placeholder} with %s and feed the matching param.
	pathFmt := path
	for _, p := range op.Parameters {
		switch p.In {
		case "path":
			gp := genParam{GoName: inflect.Camel(p.Name), GoType: paramGoType(p.Schema), WName: p.Name}
			m.PathParams = append(m.PathParams, gp)
			pathFmt = strings.ReplaceAll(pathFmt, "{"+p.Name+"}", "%s")
			m.PathArgs = append(m.PathArgs, pathArgExpr(gp))
		case "query":
			gp := genParam{GoName: inflect.Camel(p.Name), GoType: paramGoType(p.Schema), WName: p.Name}
			m.QueryParams = append(m.QueryParams, gp)
		}
	}
	m.PathFmt = pathFmt

	if op.RequestBody != nil {
		if media, ok := op.RequestBody.Content["application/json"]; ok && media.Schema != nil {
			m.HasBody = true
			m.BodyType = goType(media.Schema)
		}
	}

	m.RespType = successRespType(op.Responses)
	return m
}

// successRespType picks the JSON body type of the lowest 2xx response.
func successRespType(responses map[string]oaResp) string {
	codes := make([]string, 0, len(responses))
	for code := range responses {
		if strings.HasPrefix(code, "2") {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	for _, code := range codes {
		if media, ok := responses[code].Content["application/json"]; ok && media.Schema != nil {
			return goType(media.Schema)
		}
	}
	return ""
}

func paramGoType(s *oaSchema) string {
	if s == nil {
		return "string"
	}
	if t := goType(s); t != "any" {
		return t
	}
	return "string"
}

// pathArgExpr renders a path param value as a string expression for Sprintf.
func pathArgExpr(p genParam) string {
	switch p.GoType {
	case "string":
		return p.GoName
	default:
		return fmt.Sprintf("fmt.Sprintf(\"%%v\", %s)", p.GoName)
	}
}

// writeMethod emits a single client method.
func writeMethod(b *strings.Builder, m genMethod) {
	fmt.Fprintf(b, "// %s — %s\n", m.Name, m.Comment)

	// Signature.
	args := []string{"ctx context.Context"}
	for _, p := range m.PathParams {
		args = append(args, p.GoName+" "+p.GoType)
	}
	for _, p := range m.QueryParams {
		args = append(args, p.GoName+" "+p.GoType)
	}
	if m.HasBody {
		args = append(args, "body "+m.BodyType)
	}

	rets := "error"
	if m.RespType != "" {
		rets = "(" + m.RespType + ", error)"
	}
	fmt.Fprintf(b, "func (c *Client) %s(%s) %s {\n", m.Name, strings.Join(args, ", "), rets)

	// Zero value for the error returns when there is a response type.
	zero := ""
	if m.RespType != "" {
		fmt.Fprintf(b, "\tvar out %s\n", m.RespType)
		zero = "out, "
	}

	// Path.
	if len(m.PathArgs) > 0 {
		fmt.Fprintf(b, "\tpath := fmt.Sprintf(%q, %s)\n", m.PathFmt, strings.Join(m.PathArgs, ", "))
	} else {
		fmt.Fprintf(b, "\tpath := %q\n", m.PathFmt)
	}

	// Query.
	if len(m.QueryParams) > 0 {
		b.WriteString("\tquery := url.Values{}\n")
		for _, p := range m.QueryParams {
			fmt.Fprintf(b, "\tquery.Set(%q, %s)\n", p.WName, queryValueExpr(p))
		}
	} else {
		b.WriteString("\tvar query url.Values\n")
	}

	// Body / out args.
	bodyArg := "nil"
	if m.HasBody {
		bodyArg = "body"
	}
	outArg := "nil"
	if m.RespType != "" {
		outArg = "&out"
	}

	if m.RespType != "" {
		fmt.Fprintf(b, "\terr := c.doRequest(ctx, %s, path, query, %s, %s)\n", m.HTTPMethod, bodyArg, outArg)
		fmt.Fprintf(b, "\treturn %serr\n", zero)
	} else {
		fmt.Fprintf(b, "\treturn c.doRequest(ctx, %s, path, query, %s, %s)\n", m.HTTPMethod, bodyArg, outArg)
	}
	b.WriteString("}\n")
}

// queryValueExpr renders a query param value as a string expression.
func queryValueExpr(p genParam) string {
	switch p.GoType {
	case "string":
		return p.GoName
	default:
		return fmt.Sprintf("fmt.Sprintf(\"%%v\", %s)", p.GoName)
	}
}

// methodName derives an exported Go method name from operationId, falling back
// to verb + path segments when no operationId is present.
func methodName(operationID, path, verb string) string {
	if operationID != "" {
		return inflect.Pascal(sanitizeIdent(operationID))
	}
	parts := []string{verb}
	for _, seg := range strings.Split(path, "/") {
		seg = strings.Trim(seg, "/")
		if seg == "" {
			continue
		}
		seg = strings.Trim(seg, "{}")
		parts = append(parts, seg)
	}
	return inflect.Pascal(strings.Join(parts, "_"))
}

// sanitizeIdent replaces characters illegal in Go identifiers with underscores
// so an operationId like "users.list" or "get-user" becomes a valid base.
func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func httpMethodConst(verb string) string {
	switch strings.ToLower(verb) {
	case "get":
		return "http.MethodGet"
	case "post":
		return "http.MethodPost"
	case "put":
		return "http.MethodPut"
	case "patch":
		return "http.MethodPatch"
	case "delete":
		return "http.MethodDelete"
	case "head":
		return "http.MethodHead"
	case "options":
		return "http.MethodOptions"
	default:
		return fmt.Sprintf("%q", strings.ToUpper(verb))
	}
}
