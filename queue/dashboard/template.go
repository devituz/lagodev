package dashboard

// pageTemplate is the single HTML document rendered for every dashboard
// view (overview and failed list). It is parsed once by html/template, so
// every dynamic field below is contextually auto-escaped at render time —
// no field reaches the page without escaping.
//
// The template model is pageData (handler.go). The {{if}} on .Active drives
// which body section renders, keeping both the legacy Dashboard handlers and
// the Inspector-based Handler on one document.
const pageTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root{--bg:#0f1419;--panel:#1a212b;--line:#2a333f;--fg:#e6edf3;--muted:#8b97a7;--accent:#3b82f6;--ok:#22c55e;--warn:#f59e0b;--bad:#ef4444}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif}
header{display:flex;align-items:center;gap:24px;padding:16px 24px;border-bottom:1px solid var(--line);background:var(--panel)}
header h1{font-size:16px;margin:0;font-weight:600}
nav a{color:var(--muted);text-decoration:none;margin-right:16px;padding-bottom:2px}
nav a.active{color:var(--fg);border-bottom:2px solid var(--accent)}
main{padding:24px;max-width:1100px;margin:0 auto}
.cards{display:flex;gap:16px;flex-wrap:wrap;margin-bottom:24px}
.card{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:16px 20px;min-width:140px}
.card .v{font-size:24px;font-weight:600}
.card .k{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.04em}
table{width:100%;border-collapse:collapse;background:var(--panel);border:1px solid var(--line);border-radius:8px;overflow:hidden}
th,td{text-align:left;padding:10px 14px;border-bottom:1px solid var(--line)}
th{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.04em;font-weight:600}
tr:last-child td{border-bottom:none}
td.num{text-align:right;font-variant-numeric:tabular-nums}
.empty{color:var(--muted);padding:24px;text-align:center}
button{font:inherit;cursor:pointer;border:1px solid var(--line);background:#222c38;color:var(--fg);padding:5px 12px;border-radius:6px}
button.retry{border-color:var(--accent);color:#bfdbfe}
button.forget{border-color:var(--bad);color:#fca5a5}
button.flush{border-color:var(--bad);color:#fca5a5}
form.inline{display:inline}
.err{color:var(--bad);font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px}
.toolbar{display:flex;justify-content:flex-end;margin-bottom:16px}
.pager{display:flex;gap:8px;margin-top:16px;align-items:center;color:var(--muted)}
.pager a{color:var(--accent);text-decoration:none}
</style>
</head>
<body>
<header>
<h1>Queue Dashboard</h1>
<nav>
<a href="./"{{if eq .Active "overview"}} class="active"{{end}}>Overview</a>
<a href="./failed"{{if eq .Active "failed"}} class="active"{{end}}>Failed</a>
</nav>
</header>
<main>
{{if eq .Active "overview"}}
<div class="cards">
<div class="card"><div class="v">{{.Totals.Pushed}}</div><div class="k">Pushed</div></div>
<div class="card"><div class="v">{{.Totals.Processed}}</div><div class="k">Processed</div></div>
<div class="card"><div class="v">{{.Totals.Pending}}</div><div class="k">Pending</div></div>
<div class="card"><div class="v">{{.Totals.Failed}}</div><div class="k">Failed</div></div>
<div class="card"><div class="v">{{printf "%.2f" .Totals.Throughput}}</div><div class="k">Jobs/sec</div></div>
</div>
<table>
<thead><tr><th>Queue</th><th class="num">Pushed</th><th class="num">Processed</th><th class="num">Pending</th><th class="num">Failed</th><th class="num">Retried</th><th class="num">Jobs/sec</th></tr></thead>
<tbody>
{{range .Stats}}
<tr>
<td>{{.Queue}}</td>
<td class="num">{{.Pushed}}</td>
<td class="num">{{.Processed}}</td>
<td class="num">{{.Pending}}</td>
<td class="num">{{.Failed}}</td>
<td class="num">{{.Retried}}</td>
<td class="num">{{printf "%.2f" .Throughput}}</td>
</tr>
{{else}}
<tr><td colspan="7" class="empty">No queue activity recorded.</td></tr>
{{end}}
</tbody>
</table>
{{else}}
{{if .Failed}}
<div class="toolbar">
<form class="inline" method="post" action="./failed/flush" onsubmit="return confirm('Flush all failed jobs?')">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<button type="submit" class="flush">Flush all</button>
</form>
</div>
{{end}}
<table>
<thead><tr><th>ID</th><th>Queue</th><th>Job</th><th>Error</th><th>Failed at</th><th>Actions</th></tr></thead>
<tbody>
{{range .Failed}}
<tr>
<td>{{.ID}}</td>
<td>{{.Queue}}</td>
<td>{{.Name}}</td>
<td class="err">{{.Err}}</td>
<td>{{.FailedAt}}</td>
<td>
<form class="inline" method="post" action="./failed/{{.ID}}/retry">
<input type="hidden" name="csrf" value="{{$.CSRF}}">
<input type="hidden" name="id" value="{{.ID}}">
<button type="submit" class="retry">Retry</button>
</form>
<form class="inline" method="post" action="./failed/{{.ID}}/forget">
<input type="hidden" name="csrf" value="{{$.CSRF}}">
<input type="hidden" name="id" value="{{.ID}}">
<button type="submit" class="forget">Forget</button>
</form>
</td>
</tr>
{{else}}
<tr><td colspan="6" class="empty">No failed jobs.</td></tr>
{{end}}
</tbody>
</table>
{{if .HasPager}}
<div class="pager">
{{if gt .Page 1}}<a href="./failed?page={{.PrevPage}}">&larr; Prev</a>{{end}}
<span>Page {{.Page}}</span>
{{if .HasNext}}<a href="./failed?page={{.NextPage}}">Next &rarr;</a>{{end}}
</div>
{{end}}
{{end}}
</main>
</body>
</html>
`
