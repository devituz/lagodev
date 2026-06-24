package telescope

// pageTemplate holds every named template the dashboard renders. All dynamic
// values are auto-escaped by html/template; no raw HTML is interpolated.
const pageTemplate = `
{{define "layout"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root{color-scheme:light dark}
body{font:14px/1.5 system-ui,sans-serif;margin:0;background:#f6f7f9;color:#1b1f24}
header{background:#1b1f24;color:#fff;padding:12px 20px;display:flex;align-items:center;gap:16px;flex-wrap:wrap}
header h1{font-size:16px;margin:0;font-weight:600}
nav{display:flex;gap:4px;flex-wrap:wrap}
nav a{color:#cbd2da;text-decoration:none;padding:4px 10px;border-radius:6px;font-size:13px}
nav a.active{background:#2d333b;color:#fff}
main{padding:20px;max-width:1100px;margin:0 auto}
table{width:100%;border-collapse:collapse;background:#fff;border-radius:8px;overflow:hidden;box-shadow:0 1px 2px rgba(0,0,0,.06)}
th,td{text-align:left;padding:8px 12px;border-bottom:1px solid #eceef0;vertical-align:top}
th{background:#fafbfc;font-size:12px;text-transform:uppercase;letter-spacing:.04em;color:#6a737d}
tr:last-child td{border-bottom:none}
td.title{font-family:ui-monospace,Menlo,monospace;font-size:12px;word-break:break-all}
a{color:#0969da}
.tag{display:inline-block;background:#eceef0;color:#444;border-radius:10px;padding:1px 8px;font-size:11px;margin-right:4px}
.tag.np{background:#fbe9e7;color:#b71c1c}
.cards{display:flex;gap:12px;flex-wrap:wrap;margin-bottom:20px}
.card{background:#fff;border-radius:8px;padding:12px 16px;min-width:110px;box-shadow:0 1px 2px rgba(0,0,0,.06);text-decoration:none;color:inherit}
.card .n{font-size:22px;font-weight:600}
.card .t{font-size:12px;color:#6a737d;text-transform:capitalize}
.meta{color:#6a737d;font-size:12px;margin-bottom:16px}
pre{background:#1b1f24;color:#e6edf3;padding:14px;border-radius:8px;overflow:auto;font-size:12px}
form.clear{margin-left:auto}
button{background:#b71c1c;color:#fff;border:0;border-radius:6px;padding:5px 12px;cursor:pointer;font-size:13px}
dl{display:grid;grid-template-columns:max-content 1fr;gap:4px 16px;margin:0 0 16px}
dt{color:#6a737d}
.empty{color:#6a737d;padding:24px;text-align:center}
</style>
</head>
<body>
<header>
<h1>{{.Title}}</h1>
<nav>
<a href="./" {{if eq .Active "overview"}}class="active"{{end}}>Overview</a>
{{$active := .Active}}{{range .Types}}<a href="./{{.}}" {{if eq $active (printf "%s" .)}}class="active"{{end}}>{{.}}</a>{{end}}
</nav>
<form class="clear" method="post" action="./clear"><button type="submit">Clear</button></form>
</header>
<main>
{{template "body" .}}
</main>
</body>
</html>{{end}}

{{define "rows"}}
<table>
<thead><tr><th>Type</th><th>Title</th><th>Tags</th><th>Time</th></tr></thead>
<tbody>
{{range .}}
<tr>
<td>{{.Type}}</td>
<td class="title"><a href="./entry/{{.ID}}">{{.Title}}</a></td>
<td>{{range .Tags}}<span class="tag{{if eq . "n+1"}} np{{end}}">{{.}}</span>{{end}}</td>
<td>{{.Time}}</td>
</tr>
{{else}}
<tr><td class="empty" colspan="4">No entries.</td></tr>
{{end}}
</tbody>
</table>
{{end}}

{{define "overview"}}{{template "layout" .}}{{end}}
{{define "list"}}{{template "layout" .}}{{end}}
{{define "detail"}}{{template "layout" .}}{{end}}

{{define "body"}}
{{if eq .Active "overview"}}
<div class="cards">
{{range .Types}}<a class="card" href="./{{.}}"><div class="n">{{index $.Counts .}}</div><div class="t">{{.}}</div></a>{{end}}
</div>
<div class="meta">{{.Total}} entries stored (capacity {{.Capacity}}), showing most recent.</div>
{{template "rows" .Entries}}
{{else if .Entry.ID}}
<p class="meta"><a href="./{{.Entry.Type}}">&larr; {{.Entry.Type}}</a></p>
<h2 style="font-size:15px;font-family:ui-monospace,Menlo,monospace;word-break:break-all">{{.Entry.Title}}</h2>
<dl>
<dt>ID</dt><dd>{{.Entry.ID}}</dd>
<dt>Type</dt><dd>{{.Entry.Type}}</dd>
<dt>Time</dt><dd>{{.Entry.Time}}</dd>
{{if .Entry.RequestID}}<dt>Request</dt><dd>{{.Entry.RequestID}}</dd>{{end}}
{{if .Entry.Tags}}<dt>Tags</dt><dd>{{range .Entry.Tags}}<span class="tag{{if eq . "n+1"}} np{{end}}">{{.}}</span>{{end}}</dd>{{end}}
</dl>
{{if .Pretty}}<pre>{{.Pretty}}</pre>{{end}}
{{if .Related}}<h3 style="font-size:13px">Same request</h3>{{template "rows" .Related}}{{end}}
{{else}}
<div class="meta">{{.Total}} {{.Filter}} {{if eq .Total 1}}entry{{else}}entries{{end}}.</div>
{{template "rows" .Entries}}
{{end}}
{{end}}
`
