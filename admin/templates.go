package admin

import "html/template"

// View-model structs passed to the templates. Every dynamic string field is
// auto-escaped by html/template at render time.

// indexData backs the resource index page.
type indexData struct {
	Base      *Panel
	Resources []struct {
		Slug  string
		Label string
		URL   string
	}
}

// listRow is a single rendered table row.
type listRow struct {
	ID      string
	EditURL string
	Cells   []string
}

// listData backs the paginated list view.
type listData struct {
	Base       *Panel
	Slug       string
	Label      string
	Columns    []Field
	Rows       []listRow
	Search     string
	SearchOn   bool
	Filters    []string
	FilterVals map[string]string
	NewURL     string
	Page       int
	TotalPages int
	Total      int
	PrevURL    string
	NextURL    string
	HasPrev    bool
	HasNext    bool
	CSRF       string
}

// formField pairs a reflected Field with its current value for the form.
type formField struct {
	Field   Field
	Value   string
	Checked bool
}

// formData backs the create/edit form.
type formData struct {
	Base     *Panel
	Slug     string
	Label    string
	Title    string
	Action   string
	Fields   []formField
	ListURL  string
	CSRF     string
	IsEdit   bool
	DeleteAt string
}

// Title exposes the panel title to templates.
func (p *Panel) Title() string { return p.title }

// HomeURL exposes the index URL to templates.
func (p *Panel) HomeURL() string { return p.url() }

// tmpl is the parsed template set, shared across all panels (the templates are
// data-driven and carry no per-panel state). html/template auto-escapes every
// interpolation, defending against XSS in user-supplied row data.
var tmpl = template.Must(template.New("admin").Parse(layoutTemplate))

const layoutTemplate = `
{{define "head"}}<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Base.Title}}</title>
<style>
body{font-family:system-ui,sans-serif;margin:0;background:#f5f6f8;color:#1b1f23}
header{background:#1b1f23;color:#fff;padding:12px 20px;display:flex;align-items:center;gap:16px}
header a{color:#9ecbff;text-decoration:none}
main{max-width:960px;margin:24px auto;padding:0 20px}
table{border-collapse:collapse;width:100%;background:#fff;box-shadow:0 1px 2px rgba(0,0,0,.08)}
th,td{text-align:left;padding:8px 12px;border-bottom:1px solid #e1e4e8}
th{background:#fafbfc;font-size:13px;text-transform:uppercase;letter-spacing:.04em;color:#586069}
a.btn,button.btn{display:inline-block;padding:6px 12px;border:1px solid #d1d5da;border-radius:6px;background:#fff;cursor:pointer;text-decoration:none;color:#1b1f23;font-size:14px}
a.btn.primary{background:#2188ff;border-color:#2188ff;color:#fff}
button.danger{background:#d73a49;border-color:#d73a49;color:#fff}
.bar{display:flex;justify-content:space-between;align-items:center;margin-bottom:16px;gap:12px;flex-wrap:wrap}
.field{margin-bottom:14px;display:flex;flex-direction:column;gap:4px}
.field label{font-size:13px;color:#586069}
.field input[type=text],.field input[type=number]{padding:8px;border:1px solid #d1d5da;border-radius:6px;font-size:14px}
.pager{margin-top:16px;display:flex;gap:8px;align-items:center}
.muted{color:#586069;font-size:13px}
form.inline{display:inline}
</style>
</head><body>
<header><a href="{{.Base.HomeURL}}">{{.Base.Title}}</a></header>
<main>{{end}}

{{define "foot"}}</main></body></html>{{end}}

{{define "index"}}{{template "head" .}}
<h1>Resources</h1>
<table>
<thead><tr><th>Model</th><th></th></tr></thead>
<tbody>
{{range .Resources}}<tr><td>{{.Label}}</td><td><a class="btn" href="{{.URL}}">Browse</a></td></tr>{{end}}
</tbody>
</table>
{{template "foot" .}}{{end}}

{{define "list"}}{{template "head" .}}
<div class="bar">
  <h1>{{.Label}}</h1>
  <a class="btn primary" href="{{.NewURL}}">New {{.Label}}</a>
</div>
{{if or .SearchOn .Filters}}
<form method="get" class="bar">
  {{if .SearchOn}}<input type="text" name="q" value="{{.Search}}" placeholder="Search…">{{end}}
  {{range .Filters}}<input type="text" name="filter_{{.}}" value="{{index $.FilterVals .}}" placeholder="{{.}}">{{end}}
  <button class="btn" type="submit">Filter</button>
</form>
{{end}}
<table>
<thead><tr>{{range .Columns}}<th>{{.Label}}</th>{{end}}<th></th></tr></thead>
<tbody>
{{range .Rows}}<tr>
  {{range .Cells}}<td>{{.}}</td>{{end}}
  <td>
    <a class="btn" href="{{.EditURL}}">Edit</a>
    <form class="inline" method="post" action="{{.EditURL}}/delete" onsubmit="return confirm('Delete?')">
      <input type="hidden" name="csrf" value="{{$.CSRF}}">
      <button class="btn danger" type="submit">Delete</button>
    </form>
  </td>
</tr>{{else}}<tr><td colspan="{{len .Columns}}" class="muted">No records.</td><td></td></tr>{{end}}
</tbody>
</table>
<div class="pager">
  {{if .HasPrev}}<a class="btn" href="{{.PrevURL}}">← Prev</a>{{end}}
  <span class="muted">Page {{.Page}} of {{.TotalPages}} · {{.Total}} total</span>
  {{if .HasNext}}<a class="btn" href="{{.NextURL}}">Next →</a>{{end}}
</div>
{{template "foot" .}}{{end}}

{{define "form"}}{{template "head" .}}
<div class="bar"><h1>{{.Title}}</h1><a class="btn" href="{{.ListURL}}">← Back</a></div>
<form method="post" action="{{.Action}}">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  {{range .Fields}}
  <div class="field">
    <label for="f_{{.Field.Column}}">{{.Field.Label}}</label>
    {{if eq .Field.Kind "bool"}}
      <input id="f_{{.Field.Column}}" type="checkbox" name="{{.Field.Column}}" value="true"{{if .Checked}} checked{{end}}>
    {{else if eq .Field.Kind "number"}}
      <input id="f_{{.Field.Column}}" type="number" name="{{.Field.Column}}" value="{{.Value}}">
    {{else}}
      <input id="f_{{.Field.Column}}" type="text" name="{{.Field.Column}}" value="{{.Value}}">
    {{end}}
  </div>
  {{end}}
  <button class="btn primary" type="submit">Save</button>
</form>
{{if .IsEdit}}
<form method="post" action="{{.DeleteAt}}" style="margin-top:16px" onsubmit="return confirm('Delete?')">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <button class="btn danger" type="submit">Delete</button>
</form>
{{end}}
{{template "foot" .}}{{end}}
`
