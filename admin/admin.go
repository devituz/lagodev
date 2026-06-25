// Package admin provides a Django-admin-style auto-CRUD panel for the lagodev
// framework. You register your domain models against a Panel, and the panel
// serves a self-contained web UI — model index, paginated/searchable list
// views, detail pages, and create/edit/delete forms — entirely from struct
// reflection. It is stdlib-only (net/http, html/template, reflect, sort) and
// returns a plain http.Handler the application mounts wherever it likes
// (conventionally /admin/).
//
// The panel never talks to a database directly. Persistence is abstracted
// behind the DataSource interface, so the same UI works against an in-memory
// slice (SliceSource, shipped for tests and demos), an orm-backed adapter the
// application supplies, or any other store. This keeps the admin package free
// of a hard dependency on a live DB and trivially unit-testable.
//
// Field metadata (columns, labels, editability) is derived by reflecting over
// the registered model's struct tags — `column:"…"` and `orm:"…"`, matching
// the rest of the framework — and may be overridden per resource. Well-known
// fields are recognised automatically: ID is the primary key, CreatedAt /
// UpdatedAt are read-only timestamps, and a DeletedAt field marks the model as
// soft-delete aware.
//
// Basic usage:
//
//	type User struct {
//	    ID        uint64    `column:"id"   orm:"primary"`
//	    Name      string    `column:"name"`
//	    Email     string    `column:"email"`
//	    CreatedAt time.Time `column:"created_at"`
//	}
//
//	src := admin.NewSliceSource("id")
//	src.Seed(map[string]any{"id": 1, "name": "Ada", "email": "ada@example.com"})
//
//	p := admin.New(admin.WithTitle("Acme Admin"))
//	p.Register(User{}, admin.Resource{
//	    Source:     src,
//	    Search:     []string{"name", "email"},
//	    Filters:    []string{"email"},
//	    PerPage:    25,
//	})
//	http.Handle("/admin/", http.StripPrefix("/admin", p.Handler()))
//
// Security: the panel is fail-closed by default. With no Authorizer installed
// it denies every request (HTTP 403) until you configure one via
// WithAuthorizer — or explicitly opt out of the gate with WithInsecureAllowAll
// for local/dev use or when the mount is already protected by upstream auth
// middleware. Every response auto-escapes through html/template, and mutating
// requests (create/update/delete) are POST-only and CSRF-guarded with a
// double-submit token. The Authorizer hook gates every action by (request,
// action, model) and yields 403 on denial.
package admin

import (
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/devituz/lagodev/internal/inflect"
)

// Action enumerates the operations the panel performs on a resource. They are
// the verbs passed to an Authorizer and used internally to gate handlers.
const (
	ActionList   = "list"
	ActionView   = "view"
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
)

// Authorizer decides whether the request may perform action on the named model
// (the model's registered slug). Returning false yields HTTP 403. A nil
// Authorizer allows everything. action is one of the Action* constants; model
// is empty for the top-level index page.
type Authorizer func(r *http.Request, action, model string) bool

// Panel is the registry and HTTP front-end for the admin UI. Register models
// against it, then mount Handler(). A Panel is safe for concurrent use after
// registration; register all resources during setup before serving.
type Panel struct {
	title      string
	basePath   string
	authorizer Authorizer
	// allowAll, when true, lets every action through even though no Authorizer
	// is installed. It is the explicit, opt-in escape hatch for the otherwise
	// fail-closed default (see WithInsecureAllowAll). Without it and without an
	// Authorizer, the panel denies every request.
	allowAll bool

	mu        sync.RWMutex
	resources map[string]*registered // keyed by slug
	order     []string               // registration order for the index
}

// registered is a resource after reflection and defaulting have been applied.
type registered struct {
	slug     string
	label    string
	pkColumn string
	source   DataSource
	fields   []Field
	search   []string
	filters  []string
	perPage  int
	softCol  string // DeletedAt column, "" if model is not soft-delete aware
}

// Resource declares how a model is exposed in the admin. Every field is
// optional except Source: sensible defaults are reflected from the model's
// struct tags when a field is left zero.
type Resource struct {
	// Source is the persistence backend for this model. Required.
	Source DataSource
	// Slug overrides the URL segment for the model. Defaults to the snake_case
	// plural-ish form of the type name (e.g. "User" -> "user").
	Slug string
	// Label overrides the human-readable model name shown in the UI. Defaults
	// to the humanized (Title Case) type name, e.g. "BlogPost" -> "Blog Post".
	Label string
	// Columns names the fields shown in the list table, in order. Defaults to
	// every non-relation field reflected from the model.
	Columns []string
	// Editable names the fields rendered as inputs on the create/edit form.
	// Defaults to every field except the primary key and read-only timestamps
	// (CreatedAt / UpdatedAt / DeletedAt).
	Editable []string
	// Search names the fields matched (case-insensitive substring) by the list
	// view's search box. Empty disables search.
	Search []string
	// Filters names the fields offered as exact-match filter inputs on the
	// list view. Empty disables filtering.
	Filters []string
	// PerPage is the list page size. Defaults to DefaultPerPage when < 1.
	PerPage int
}

// DefaultPerPage is the list page size used when Resource.PerPage is unset.
const DefaultPerPage = 20

// Option configures a Panel at construction.
type Option func(*Panel)

// WithTitle sets the panel title shown in the header and page titles.
func WithTitle(title string) Option {
	return func(p *Panel) { p.title = title }
}

// WithBasePath sets the URL prefix the panel is mounted under (used to build
// links). It defaults to "/admin". Mount the Handler() under the matching
// prefix, e.g. http.StripPrefix("/admin", p.Handler()).
func WithBasePath(base string) Option {
	return func(p *Panel) { p.basePath = normalizeBase(base) }
}

// WithAuthorizer installs an RBAC gate consulted before every action.
// Installing one is the recommended way to make the panel reachable; the
// panel denies every request until either an Authorizer or
// WithInsecureAllowAll is configured.
func WithAuthorizer(a Authorizer) Option {
	return func(p *Panel) { p.authorizer = a }
}

// WithInsecureAllowAll disables the panel's fail-closed default, allowing
// every action through with no authorization check. It exists only for local
// development, tests, and demos, or for deployments that gate the mount
// entirely with external auth middleware. NEVER enable it on a route reachable
// without an upstream auth layer: it exposes full CRUD over every registered
// model to anyone who can hit the path. Prefer WithAuthorizer in production.
func WithInsecureAllowAll() Option {
	return func(p *Panel) { p.allowAll = true }
}

// New constructs an empty Panel. Register models on it, then serve Handler().
func New(opts ...Option) *Panel {
	p := &Panel{
		title:     "Admin",
		basePath:  "/admin",
		resources: make(map[string]*registered),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Register adds a model to the panel. model is a zero/sample value of the
// struct type (e.g. User{} or &User{}); its tags drive field discovery. cfg
// supplies the DataSource and any overrides. Register panics on misuse
// (non-struct model, missing Source, duplicate slug) because these are
// programmer errors surfaced at startup, matching the framework's other
// registries.
func (p *Panel) Register(model any, cfg Resource) {
	if cfg.Source == nil {
		panic("admin: Resource.Source is required")
	}
	t := indirectType(reflect.TypeOf(model))
	if t == nil || t.Kind() != reflect.Struct {
		panic("admin: model must be a struct or pointer to struct")
	}

	fields := reflectFields(t)
	if len(fields) == 0 {
		panic(fmt.Sprintf("admin: model %s has no exported fields", t.Name()))
	}

	reg := &registered{
		slug:    defaultString(cfg.Slug, inflect.Snake(t.Name())),
		label:   defaultString(cfg.Label, humanize(t.Name())),
		fields:  fields,
		search:  cfg.Search,
		filters: cfg.Filters,
		perPage: cfg.PerPage,
	}
	if reg.perPage < 1 {
		reg.perPage = DefaultPerPage
	}

	// Resolve primary key and soft-delete columns from reflected metadata.
	for _, f := range fields {
		if f.IsPrimary && reg.pkColumn == "" {
			reg.pkColumn = f.Column
		}
		if f.IsDeletedAt {
			reg.softCol = f.Column
		}
	}
	if reg.pkColumn == "" {
		// Fall back to the first field as identifier so the panel still works
		// for models without a conventional ID.
		reg.pkColumn = fields[0].Column
	}

	// Apply column visibility / editability overrides against reflected fields.
	applyColumnSelection(reg, cfg)

	reg.source = cfg.Source

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, dup := p.resources[reg.slug]; dup {
		panic(fmt.Sprintf("admin: duplicate model slug %q", reg.slug))
	}
	p.resources[reg.slug] = reg
	p.order = append(p.order, reg.slug)
}

// applyColumnSelection marks each reflected field's List/Editable visibility
// from the Resource overrides, falling back to defaults when an override is
// empty.
func applyColumnSelection(reg *registered, cfg Resource) {
	colSet := toSet(cfg.Columns)
	editSet := toSet(cfg.Editable)
	for i := range reg.fields {
		f := &reg.fields[i]
		if len(cfg.Columns) > 0 {
			f.InList = colSet[f.Column] || colSet[f.Name]
		} else {
			f.InList = !f.IsRelation
		}
		if len(cfg.Editable) > 0 {
			f.Editable = editSet[f.Column] || editSet[f.Name]
		} else {
			// Default: editable unless it is the PK or a managed timestamp.
			f.Editable = !f.IsPrimary && !f.IsCreatedAt && !f.IsUpdatedAt && !f.IsDeletedAt && !f.IsRelation
		}
	}
}

// resource looks up a registered resource by slug under read lock.
func (p *Panel) resource(slug string) (*registered, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.resources[slug]
	return r, ok
}

// listResources returns the registered resources in registration order.
func (p *Panel) listResources() []*registered {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*registered, 0, len(p.order))
	for _, slug := range p.order {
		out = append(out, p.resources[slug])
	}
	return out
}

// listColumns returns the fields shown in the list table.
func (r *registered) listColumns() []Field {
	var out []Field
	for _, f := range r.fields {
		if f.InList {
			out = append(out, f)
		}
	}
	return out
}

// editColumns returns the fields rendered on the create/edit form.
func (r *registered) editColumns() []Field {
	var out []Field
	for _, f := range r.fields {
		if f.Editable {
			out = append(out, f)
		}
	}
	return out
}

// fieldByColumn returns the reflected field for a column name.
func (r *registered) fieldByColumn(col string) (Field, bool) {
	for _, f := range r.fields {
		if f.Column == col {
			return f, true
		}
	}
	return Field{}, false
}

// authorize consults the panel Authorizer. The default is fail-closed: with no
// Authorizer installed, every action is denied unless WithInsecureAllowAll was
// set at construction. This prevents an admin panel mounted without an explicit
// gate from being reachable unauthenticated — a production footgun.
func (p *Panel) authorize(r *http.Request, action, model string) bool {
	if p.authorizer == nil {
		return p.allowAll
	}
	return p.authorizer(r, action, model)
}

// --- small helpers -----------------------------------------------------------

func indirectType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, it := range items {
		s[it] = true
	}
	return s
}

// normalizeBase trims a trailing slash from a mount path, keeping a leading
// slash. "" and "/" both normalize to "/admin" callers' expectation of a
// non-empty prefix; an empty result means root mount.
func normalizeBase(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	return strings.TrimRight(base, "/")
}

// sortedKeys returns the keys of m sorted, for deterministic rendering.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
