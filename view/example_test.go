package view_test

import (
	"bytes"
	"fmt"
	"html/template"
	"testing/fstest"

	"github.com/devituz/lagodev/view"
)

// Example renders a page that declares a layout, pulls in a reusable button
// component via {{template ... (dict ...)}}, and uses a custom template func
// ("badge") injected through Options.Funcs. The FS is an in-memory fstest.MapFS
// so the output is fully deterministic.
func Example() {
	files := fstest.MapFS{
		// Layout: yields to the child view through {{block "content"}}.
		"templates/layouts/app.html": &fstest.MapFile{Data: []byte(
			`<html><body><main>{{block "content" .}}{{end}}</main></body></html>`,
		)},
		// Reusable component, addressed by name and fed values via dict.
		"templates/components/button.html": &fstest.MapFile{Data: []byte(
			`<button class="{{.kind}}">{{.label}}</button>`,
		)},
		// Page: declares its layout, redefines "content", calls the component
		// and the custom "badge" func.
		"templates/pages/home.html": &fstest.MapFile{Data: []byte(
			`{{layout "layouts/app"}}` +
				`{{define "content"}}` +
				`<h1>{{upper .Name}}</h1>` +
				`{{badge .Role}}` +
				`{{template "components/button" (dict "label" "Save" "kind" "primary")}}` +
				`{{end}}`,
		)},
	}

	eng, err := view.New(files, view.Options{
		Root: "templates",
		Funcs: template.FuncMap{
			// Custom func: renders a small role badge (trusted markup).
			"badge": func(role string) template.HTML {
				return template.HTML(`<span class="badge">` + role + `</span>`)
			},
		},
	})
	if err != nil {
		panic(err)
	}

	var buf bytes.Buffer
	if err := eng.Render(&buf, "pages/home", map[string]any{
		"Name": "ada",
		"Role": "admin",
	}); err != nil {
		panic(err)
	}

	fmt.Println(buf.String())
	// Output:
	// <html><body><main><h1>ADA</h1><span class="badge">admin</span><button class="primary">Save</button></main></body></html>
}
