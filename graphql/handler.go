package graphql

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// httpRequest is the wire shape of a GraphQL HTTP request body: the query
// document plus optional operation name and variables. It mirrors the de-facto
// GraphQL-over-HTTP JSON protocol used by every common client.
type httpRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
}

// maxBodyBytes caps the request body the handler will read, guarding against
// unbounded memory use from oversized or hostile payloads.
const maxBodyBytes = 1 << 20 // 1 MiB

// handlerConfig holds the resolved handler settings. It is built from defaults
// and mutated by the functional Options passed to Handler.
type handlerConfig struct {
	limits      Limits
	maxBodySize int64
}

// Option configures a Handler. Options compose; later options override earlier
// ones. The zero set of options yields DefaultLimits and the default body cap,
// preserving the handler's historical behaviour for normal traffic.
type Option func(*handlerConfig)

// WithLimits sets the execution Limits enforced on every request's document.
// Use DefaultLimits as a base and override individual fields, or pass a custom
// Limits. The zero Limits value disables all structural checks.
func WithLimits(l Limits) Option {
	return func(c *handlerConfig) { c.limits = l }
}

// WithMaxBodyBytes overrides the maximum request-body size (in bytes) the
// handler will read before decoding. Values <= 0 leave the default in place.
// It should be set in concert with Limits.MaxDocumentBytes.
func WithMaxBodyBytes(n int64) Option {
	return func(c *handlerConfig) {
		if n > 0 {
			c.maxBodySize = n
		}
	}
}

// WithoutIntrospection disables introspection meta-fields (__typename, __schema,
// __type) for the handler, a common production hardening step.
func WithoutIntrospection() Option {
	return func(c *handlerConfig) { c.limits.DisableIntrospection = true }
}

// Handler returns an http.Handler that executes GraphQL requests against cs and
// writes the standard {"data":...,"errors":[...]} JSON envelope.
//
// It speaks the conventional GraphQL-over-HTTP protocol:
//
//   - POST with Content-Type application/json and a body of
//     {"query":..., "operationName":..., "variables":{...}}.
//   - GET with the query in the ?query= parameter (and optional
//     ?operationName= and ?variables= as a JSON-encoded object), suitable for
//     simple read-only requests.
//
// The request context is threaded into Execute, so resolver cancellation and
// deadlines follow the connection. Transport-level problems (wrong method,
// unreadable body, malformed JSON) yield a JSON error envelope; resolver and
// validation errors surface through the normal response "errors" array with a
// 200 status, per GraphQL convention.
func Handler(cs *CompiledSchema, opts ...Option) http.Handler {
	cfg := handlerConfig{limits: DefaultLimits(), maxBodySize: maxBodyBytes}
	for _, opt := range opts {
		opt(&cfg)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		switch r.Method {
		case http.MethodPost:
			parsed, ok := decodePost(w, r, cfg.maxBodySize)
			if !ok {
				return
			}
			req = parsed
		case http.MethodGet:
			parsed, ok := decodeGet(w, r)
			if !ok {
				return
			}
			req = parsed
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed, "graphql: method not allowed")
			return
		}

		res := ExecuteWithLimits(r.Context(), cs, req, cfg.limits)
		writeJSON(w, http.StatusOK, res)
	})
}

// decodePost reads and decodes a JSON POST body into a Request. It reports
// ok=false (after writing an error response) on any transport-level failure.
func decodePost(w http.ResponseWriter, r *http.Request, maxBody int64) (Request, bool) {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mt := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]); mt != "" && mt != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "graphql: expected application/json")
			return Request{}, false
		}
	}
	if maxBody <= 0 {
		maxBody = maxBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "graphql: cannot read request body")
		return Request{}, false
	}
	var hr httpRequest
	if err := json.Unmarshal(body, &hr); err != nil {
		writeError(w, http.StatusBadRequest, "graphql: invalid JSON body: "+err.Error())
		return Request{}, false
	}
	return Request{
		Query:         hr.Query,
		OperationName: hr.OperationName,
		Variables:     hr.Variables,
	}, true
}

// decodeGet builds a Request from the URL query parameters. The variables
// parameter, when present, must be a JSON-encoded object.
func decodeGet(w http.ResponseWriter, r *http.Request) (Request, bool) {
	q := r.URL.Query()
	req := Request{
		Query:         q.Get("query"),
		OperationName: q.Get("operationName"),
	}
	if raw := q.Get("variables"); raw != "" {
		var vars map[string]any
		if err := json.Unmarshal([]byte(raw), &vars); err != nil {
			writeError(w, http.StatusBadRequest, "graphql: invalid variables parameter: "+err.Error())
			return Request{}, false
		}
		req.Variables = vars
	}
	return req, true
}

// writeJSON marshals v and writes it with the given status. A marshal failure
// (which should not happen for a Response) degrades to a plain 500.
func writeJSON(w http.ResponseWriter, status int, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "graphql: cannot encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

// writeError emits a GraphQL error envelope ({"errors":[{"message":...}]}) with
// a transport status code, used for problems that prevent execution entirely.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, Response{Errors: []GqlError{{Message: msg}}})
}
