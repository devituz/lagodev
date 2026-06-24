package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// csrfCookie is the name of the double-submit CSRF cookie. POST actions must
// echo its value in the "csrf" form field.
const csrfCookie = "lago_admin_csrf"

// Handler returns the http.Handler exposing every admin route. Mount it under
// the panel's base path:
//
//	mux.Handle("/admin/", http.StripPrefix("/admin", p.Handler()))
//
// Routes (relative to the mount point):
//
//	GET  /                         resource index
//	GET  /{resource}               paginated, searchable list
//	GET  /{resource}/new           create form
//	POST /{resource}/new           create
//	GET  /{resource}/{id}          edit form
//	POST /{resource}/{id}          update
//	POST /{resource}/{id}/delete   delete (soft-delete aware)
func (p *Panel) Handler() http.Handler {
	return http.HandlerFunc(p.route)
}

// route dispatches a request to the appropriate handler by parsing the path
// into its (resource, id, verb) segments. It is the panel's single ServeMux
// substitute: resource slugs are dynamic, so a pattern mux does not fit.
func (p *Panel) route(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	if path == "" {
		p.handleIndex(w, r)
		return
	}

	parts := strings.Split(path, "/")
	slug := parts[0]
	reg, ok := p.resource(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch len(parts) {
	case 1:
		p.handleList(w, r, reg)
	case 2:
		if parts[1] == "new" {
			p.handleNew(w, r, reg)
			return
		}
		p.handleEdit(w, r, reg, parts[1])
	case 3:
		if parts[2] == "delete" {
			p.handleDelete(w, r, reg, parts[1])
			return
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleIndex renders the top-level resource list.
func (p *Panel) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if !p.authorize(r, ActionList, "") {
		forbidden(w)
		return
	}
	var items []struct {
		Slug  string
		Label string
		URL   string
	}
	for _, reg := range p.listResources() {
		items = append(items, struct {
			Slug  string
			Label string
			URL   string
		}{Slug: reg.slug, Label: reg.label, URL: p.url(reg.slug)})
	}
	p.render(w, "index", indexData{
		Base:      p,
		Resources: items,
	})
}

// handleList renders one page of a resource, honouring search, filters, and
// pagination.
func (p *Panel) handleList(w http.ResponseWriter, r *http.Request, reg *registered) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if !p.authorize(r, ActionList, reg.slug) {
		forbidden(w)
		return
	}

	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 1)
	search := strings.TrimSpace(q.Get("q"))

	filters := make(map[string]string)
	for _, f := range reg.filters {
		if v := strings.TrimSpace(q.Get("filter_" + f)); v != "" {
			filters[f] = v
		}
	}

	params := ListParams{
		Page:             page,
		PerPage:          reg.perPage,
		Search:           search,
		SearchFields:     reg.search,
		Filters:          filters,
		SoftDeleteColumn: reg.softCol,
	}
	rows, total, err := reg.source.List(r.Context(), params)
	if err != nil {
		serverError(w, err)
		return
	}

	cols := reg.listColumns()
	tableRows := make([]listRow, 0, len(rows))
	for _, row := range rows {
		cells := make([]string, 0, len(cols))
		for _, c := range cols {
			cells = append(cells, toString(row[c.Column]))
		}
		id := toString(row[reg.pkColumn])
		tableRows = append(tableRows, listRow{ID: id, EditURL: p.url(reg.slug, id), Cells: cells})
	}

	totalPages := (total + reg.perPage - 1) / reg.perPage
	if totalPages < 1 {
		totalPages = 1
	}

	p.render(w, "list", listData{
		Base:       p,
		Slug:       reg.slug,
		Label:      reg.label,
		Columns:    cols,
		Rows:       tableRows,
		Search:     search,
		SearchOn:   len(reg.search) > 0,
		Filters:    reg.filters,
		FilterVals: filters,
		NewURL:     p.url(reg.slug, "new"),
		Page:       page,
		TotalPages: totalPages,
		Total:      total,
		PrevURL:    p.pageURL(reg.slug, search, filters, page-1),
		NextURL:    p.pageURL(reg.slug, search, filters, page+1),
		HasPrev:    page > 1,
		HasNext:    page < totalPages,
		CSRF:       p.issueToken(w, r),
	})
}

// handleNew renders (GET) and processes (POST) the create form.
func (p *Panel) handleNew(w http.ResponseWriter, r *http.Request, reg *registered) {
	if !p.authorize(r, ActionCreate, reg.slug) {
		forbidden(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		p.renderForm(w, r, reg, nil, p.url(reg.slug, "new"), "Create "+reg.label)
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if !p.validToken(r) {
			http.Error(w, "csrf token mismatch", http.StatusForbidden)
			return
		}
		data := p.formValues(reg, r)
		if _, err := reg.source.Create(r.Context(), data); err != nil {
			serverError(w, err)
			return
		}
		http.Redirect(w, r, p.url(reg.slug), http.StatusSeeOther)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// handleEdit renders (GET) and processes (POST) the edit form for one row.
func (p *Panel) handleEdit(w http.ResponseWriter, r *http.Request, reg *registered, id string) {
	switch r.Method {
	case http.MethodGet:
		if !p.authorize(r, ActionView, reg.slug) {
			forbidden(w)
			return
		}
		row, err := reg.source.Get(r.Context(), id)
		if err != nil {
			notFoundOrError(w, err)
			return
		}
		p.renderForm(w, r, reg, row, p.url(reg.slug, id), "Edit "+reg.label)
	case http.MethodPost:
		if !p.authorize(r, ActionUpdate, reg.slug) {
			forbidden(w)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if !p.validToken(r) {
			http.Error(w, "csrf token mismatch", http.StatusForbidden)
			return
		}
		data := p.formValues(reg, r)
		if _, err := reg.source.Update(r.Context(), id, data); err != nil {
			notFoundOrError(w, err)
			return
		}
		http.Redirect(w, r, p.url(reg.slug), http.StatusSeeOther)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// handleDelete processes the POST delete action (soft-delete aware via the
// source).
func (p *Panel) handleDelete(w http.ResponseWriter, r *http.Request, reg *registered, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !p.authorize(r, ActionDelete, reg.slug) {
		forbidden(w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !p.validToken(r) {
		http.Error(w, "csrf token mismatch", http.StatusForbidden)
		return
	}
	if err := reg.source.Delete(r.Context(), id); err != nil {
		notFoundOrError(w, err)
		return
	}
	http.Redirect(w, r, p.url(reg.slug), http.StatusSeeOther)
}

// formValues extracts editable fields from a posted form, coercing each to a
// Go type appropriate for the field kind so the data source stores native
// values rather than raw strings.
func (p *Panel) formValues(reg *registered, r *http.Request) map[string]any {
	data := make(map[string]any)
	for _, f := range reg.editColumns() {
		switch f.Kind {
		case "bool":
			data[f.Column] = r.PostFormValue(f.Column) != ""
		case "number":
			raw := strings.TrimSpace(r.PostFormValue(f.Column))
			if raw == "" {
				data[f.Column] = 0
				continue
			}
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
				data[f.Column] = n
			} else if fl, err := strconv.ParseFloat(raw, 64); err == nil {
				data[f.Column] = fl
			} else {
				data[f.Column] = raw
			}
		default:
			data[f.Column] = r.PostFormValue(f.Column)
		}
	}
	return data
}

// renderForm renders the create/edit form. row is nil for create; otherwise it
// supplies the current values keyed by column.
func (p *Panel) renderForm(w http.ResponseWriter, r *http.Request, reg *registered, row map[string]any, action, title string) {
	fields := make([]formField, 0)
	for _, f := range reg.editColumns() {
		ff := formField{Field: f}
		if row != nil {
			ff.Value = toString(row[f.Column])
			ff.Checked = ff.Value == "true"
		}
		fields = append(fields, ff)
	}
	p.render(w, "form", formData{
		Base:     p,
		Slug:     reg.slug,
		Label:    reg.label,
		Title:    title,
		Action:   action,
		Fields:   fields,
		ListURL:  p.url(reg.slug),
		CSRF:     p.issueToken(w, r),
		IsEdit:   row != nil,
		DeleteAt: p.deleteURL(reg.slug, row),
	})
}

// deleteURL returns the delete action URL for an existing row, or "" for create.
func (p *Panel) deleteURL(slug string, row map[string]any) string {
	if row == nil {
		return ""
	}
	reg, _ := p.resource(slug)
	id := toString(row[reg.pkColumn])
	return p.url(slug, id, "delete")
}

// --- URL helpers -------------------------------------------------------------

// url joins the base path with the supplied segments.
func (p *Panel) url(segments ...string) string {
	base := p.basePath
	if base == "" {
		base = "/"
	}
	parts := append([]string{strings.TrimRight(base, "/")}, segments...)
	return strings.Join(parts, "/")
}

// pageURL builds a list URL carrying the current search/filter state and a page
// number.
func (p *Panel) pageURL(slug, search string, filters map[string]string, page int) string {
	if page < 1 {
		page = 1
	}
	q := make([]string, 0)
	q = append(q, "page="+strconv.Itoa(page))
	if search != "" {
		q = append(q, "q="+urlQueryEscape(search))
	}
	for k, v := range filters {
		q = append(q, "filter_"+k+"="+urlQueryEscape(v))
	}
	return p.url(slug) + "?" + strings.Join(q, "&")
}

// --- response helpers --------------------------------------------------------

func methodNotAllowed(w http.ResponseWriter, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func forbidden(w http.ResponseWriter) { http.Error(w, "forbidden", http.StatusForbidden) }

func serverError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func notFoundOrError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "record not found", http.StatusNotFound)
		return
	}
	serverError(w, err)
}

func (p *Panel) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
	}
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return def
}

// urlQueryEscape percent-encodes a query value. We avoid net/url here only to
// keep the import set tight; the cases that matter for admin (spaces, &, =, #)
// are covered, and template.URLQueryEscaper handles the rest.
func urlQueryEscape(s string) string {
	return template.URLQueryEscaper(s)
}

// --- CSRF: double-submit cookie ---------------------------------------------

// issueToken returns the CSRF token for this client, minting and setting a
// fresh cookie when none is present. The token is embedded in rendered forms
// and must be echoed back in the "csrf" field on POST.
func (p *Panel) issueToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && c.Value != "" {
		return c.Value
	}
	tok := randomToken()
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: false, // readable by the form; double-submit, not session
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(12 * time.Hour),
	})
	return tok
}

// validToken checks the posted "csrf" field against the cookie using a
// constant-time compare.
func (p *Panel) validToken(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	got := r.PostFormValue("csrf")
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(got)) == 1
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
