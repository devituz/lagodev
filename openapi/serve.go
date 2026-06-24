package openapi

import (
	"net/http"
)

// Handler returns an http.Handler that serves the OpenAPI document as JSON. It
// renders the spec on first request and caches the bytes; mount it at a path
// such as /openapi.json. Only GET/HEAD are answered (405 otherwise).
//
//	http.Handle("/openapi.json", spec.Handler())
func (s *Spec) Handler() http.Handler {
	b, err := s.JSON()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			http.Error(w, "openapi: render: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(b)
	})
}

// DocsHandler returns an http.Handler that serves a Swagger-UI HTML page
// pointing at specURL (the path where Handler is mounted, e.g. "/openapi.json").
// The page loads Swagger UI assets from a public CDN; for offline use, host the
// assets yourself and adapt SwaggerHTML.
//
//	http.Handle("/openapi.json", spec.Handler())
//	http.Handle("/docs", spec.DocsHandler("/openapi.json"))
func (s *Spec) DocsHandler(specURL string) http.Handler {
	if specURL == "" {
		specURL = "/openapi.json"
	}
	page := []byte(SwaggerHTML(s.Title, specURL))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(page)
	})
}

// SwaggerHTML returns a minimal, self-contained Swagger-UI page (CDN-backed)
// titled with title and loading the spec from specURL.
func SwaggerHTML(title, specURL string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>` + htmlEscape(title) + ` — API docs</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js" crossorigin></script>
  <script>
    window.addEventListener('load', function () {
      window.ui = SwaggerUIBundle({
        url: ` + jsString(specURL) + `,
        dom_id: '#swagger-ui',
        deepLinking: true
      });
    });
  </script>
</body>
</html>`
}

// htmlEscape minimally escapes a string for inclusion in HTML text/attributes.
func htmlEscape(s string) string {
	repl := map[rune]string{'&': "&amp;", '<': "&lt;", '>': "&gt;", '"': "&quot;", '\'': "&#39;"}
	var b []rune
	for _, r := range s {
		if e, ok := repl[r]; ok {
			b = append(b, []rune(e)...)
			continue
		}
		b = append(b, r)
	}
	return string(b)
}

// jsString quotes s as a JS/JSON string literal for safe embedding in a script.
func jsString(s string) string {
	b := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\n':
			b = append(b, '\\', 'n')
		case '<':
			b = append(b, '\\', 'u', '0', '0', '3', 'c')
		case '>':
			b = append(b, '\\', 'u', '0', '0', '3', 'e')
		default:
			b = append(b, string(r)...)
		}
	}
	return string(append(b, '"'))
}
