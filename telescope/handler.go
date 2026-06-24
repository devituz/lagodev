package telescope

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HandlerOptions configures the dashboard handler.
type HandlerOptions struct {
	// Title overrides the dashboard page title. Defaults to "Telescope".
	Title string
	// DefaultLimit caps how many entries the list and JSON API return when
	// the request does not specify ?limit=. Defaults to 200. A value <= 0
	// means no default limit.
	DefaultLimit int
}

// knownTypes is the fixed set of entry types shown in the dashboard nav, in
// display order.
var knownTypes = []Type{
	TypeRequest, TypeQuery, TypeJob, TypeCache, TypeMail, TypeException, TypeLog,
}

// Handler returns an http.Handler serving the dashboard and its JSON API.
// Mount it under a prefix, typically with http.StripPrefix:
//
//	mux.Handle("/telescope/", http.StripPrefix("/telescope",
//		rec.Handler(telescope.HandlerOptions{})))
//
// Routes (relative to the mount point):
//
//	GET  /              HTML overview: counts per type + recent entries
//	GET  /{type}        HTML list of entries of one type
//	GET  /entry/{id}    HTML detail view with a pretty-printed payload
//	GET  /api/entries   JSON list (honours ?type= and ?limit=)
//	POST /clear         clears every stored entry, then redirects to /
//
// The Handler is safe for concurrent use.
func (r *Recorder) Handler(opts HandlerOptions) http.Handler {
	if opts.Title == "" {
		opts.Title = "Telescope"
	}
	if opts.DefaultLimit == 0 {
		opts.DefaultLimit = 200
	}
	h := &dashboard{rec: r, opts: opts, tmpl: dashboardTemplates()}
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.route)
	mux.HandleFunc("/clear", h.handleClear)
	mux.HandleFunc("/api/entries", h.handleAPIEntries)
	return mux
}

type dashboard struct {
	rec  *Recorder
	opts HandlerOptions
	tmpl *template.Template
}

// route dispatches the HTML pages that live under "/": the overview, the
// per-type list "/{type}" and the detail "/entry/{id}".
func (h *dashboard) route(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.Trim(req.URL.Path, "/")
	switch {
	case path == "":
		h.handleOverview(w, req)
	case strings.HasPrefix(path, "entry/"):
		id := strings.TrimPrefix(path, "entry/")
		h.handleDetail(w, req, id)
	default:
		typ := Type(path)
		if !typ.Valid() {
			http.NotFound(w, req)
			return
		}
		h.handleList(w, req, typ)
	}
}

func (h *dashboard) handleOverview(w http.ResponseWriter, req *http.Request) {
	all := h.rec.Filter("", h.limit(req))
	counts := make(map[Type]int)
	for _, t := range knownTypes {
		counts[t] = 0
	}
	// Count across the full buffer, not just the limited slice.
	for _, e := range h.rec.Filter("", 0) {
		counts[e.Type]++
	}
	data := pageModel{
		Title:    h.opts.Title,
		Active:   "overview",
		Types:    knownTypes,
		Counts:   counts,
		Entries:  toRows(all),
		Capacity: h.rec.Capacity(),
		Total:    len(h.rec.Filter("", 0)),
	}
	h.render(w, "overview", data)
}

func (h *dashboard) handleList(w http.ResponseWriter, req *http.Request, typ Type) {
	entries := h.rec.Filter(typ, h.limit(req))
	data := pageModel{
		Title:    h.opts.Title,
		Active:   string(typ),
		Types:    knownTypes,
		Filter:   typ,
		Entries:  toRows(entries),
		Capacity: h.rec.Capacity(),
		Total:    len(entries),
	}
	h.render(w, "list", data)
}

func (h *dashboard) handleDetail(w http.ResponseWriter, req *http.Request, id string) {
	e, ok := h.rec.Find(id)
	if !ok {
		http.NotFound(w, req)
		return
	}
	var related []entryRow
	if e.RequestID != "" {
		for _, c := range h.rec.Filter("", 0) {
			if c.ID != e.ID && c.RequestID == e.RequestID {
				related = append(related, toRow(c))
			}
		}
	}
	data := pageModel{
		Title:   h.opts.Title,
		Active:  string(e.Type),
		Types:   knownTypes,
		Entry:   toRow(e),
		Pretty:  e.PrettyPayload(),
		Related: related,
	}
	h.render(w, "detail", data)
}

func (h *dashboard) handleClear(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.rec.Reset()
	http.Redirect(w, req, "./", http.StatusSeeOther)
}

func (h *dashboard) handleAPIEntries(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	typ := Type(strings.TrimSpace(req.URL.Query().Get("type")))
	if typ != "" && !typ.Valid() {
		http.Error(w, "unknown type", http.StatusBadRequest)
		return
	}
	entries := h.rec.Filter(typ, h.limit(req))
	if entries == nil {
		entries = []Entry{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(struct {
		Type    Type    `json:"type,omitempty"`
		Count   int     `json:"count"`
		Entries []Entry `json:"entries"`
	}{Type: typ, Count: len(entries), Entries: entries}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// limit reads ?limit= falling back to the configured default. A value <= 0
// in the query disables the limit for that request.
func (h *dashboard) limit(req *http.Request) int {
	if raw := req.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	}
	return h.opts.DefaultLimit
}

func (h *dashboard) render(w http.ResponseWriter, name string, data pageModel) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
	}
}

// --- view model -------------------------------------------------------

// pageModel is the template model. Every dynamic field is auto-escaped by
// html/template at render time.
type pageModel struct {
	Title    string
	Active   string // drives nav highlighting
	Types    []Type
	Counts   map[Type]int
	Filter   Type
	Entries  []entryRow
	Entry    entryRow
	Pretty   string
	Related  []entryRow
	Capacity int
	Total    int
}

// entryRow is a flattened, template-friendly view of an Entry.
type entryRow struct {
	ID        string
	Type      Type
	Title     string
	Time      string
	RequestID string
	Tags      []string
	NPlusOne  bool
}

func toRow(e Entry) entryRow {
	return entryRow{
		ID:        e.ID,
		Type:      e.Type,
		Title:     e.Title(),
		Time:      e.Time.Format(time.RFC3339),
		RequestID: e.RequestID,
		Tags:      e.Tags,
		NPlusOne:  e.hasTag("n+1"),
	}
}

func toRows(entries []Entry) []entryRow {
	out := make([]entryRow, len(entries))
	for i, e := range entries {
		out[i] = toRow(e)
	}
	return out
}

func dashboardTemplates() *template.Template {
	return template.Must(template.New("telescope").Parse(pageTemplate))
}
