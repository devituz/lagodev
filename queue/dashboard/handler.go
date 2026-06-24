package dashboard

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"time"
)

// csrfCookie is the name of the double-submit CSRF cookie. POST actions
// must echo its value in the "csrf" form field.
const csrfCookie = "lago_queue_csrf"

// Dashboard serves the operational view. Construct it with New, mount its
// Handler under a path prefix (typically with http.StripPrefix), and feed
// the Stats and FailedJobs from your worker wiring.
//
// Routes (relative to the mount point):
//
//	GET  /            overview: per-queue stats + throughput
//	GET  /failed      failed-job list with retry / forget buttons
//	POST /failed/retry   form: id, csrf  — re-enqueues and removes a job
//	POST /failed/forget  form: id, csrf  — drops a job
//	GET  /api/stats   JSON snapshot of per-queue stats + totals
//	GET  /api/failed  JSON list of failed jobs
//
// The Handler is safe for concurrent use.
type Dashboard struct {
	stats  *Stats
	failed FailedJobs
	tmpl   *template.Template
}

// New builds a Dashboard over the given collector and failed-job store.
// Either may be nil — a nil Stats renders an empty overview and a nil
// FailedJobs renders an empty failed list (and rejects POST actions).
func New(stats *Stats, failed FailedJobs) *Dashboard {
	return &Dashboard{
		stats:  stats,
		failed: failed,
		tmpl:   mustPageTemplate(),
	}
}

// Handler returns the http.Handler exposing every dashboard route. Mount
// it under a prefix:
//
//	mux.Handle("/queue/", http.StripPrefix("/queue", dash.Handler()))
func (d *Dashboard) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", d.handleOverview)
	mux.HandleFunc("/failed", d.handleFailed)
	mux.HandleFunc("/failed/retry", d.handleRetry)
	mux.HandleFunc("/failed/forget", d.handleForget)
	mux.HandleFunc("/api/stats", d.handleAPIStats)
	mux.HandleFunc("/api/failed", d.handleAPIFailed)
	return mux
}

// pageData is the template model shared by the legacy Dashboard handlers
// and the Inspector-based Handler. Every string field is auto-escaped by
// html/template at render time.
type pageData struct {
	Title  string
	Active string // "overview" | "failed", drives nav highlighting
	CSRF   string
	Stats  []QueueStat
	Totals QueueStat
	Failed []JobView

	// Pagination (failed view). HasPager gates rendering of the pager.
	Page     int
	PrevPage int
	NextPage int
	HasNext  bool
	HasPager bool
}

func (d *Dashboard) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data := pageData{Title: "Queue Overview", Active: "overview", CSRF: d.issueToken(w, r)}
	if d.stats != nil {
		data.Stats = d.stats.Snapshot()
		data.Totals = d.stats.Totals()
	}
	d.render(w, data)
}

func (d *Dashboard) handleFailed(w http.ResponseWriter, r *http.Request) {
	data := pageData{Title: "Failed Jobs", Active: "failed", CSRF: d.issueToken(w, r)}
	if d.failed != nil {
		jobs, err := d.failed.List(r.Context())
		if err != nil {
			http.Error(w, "list failed jobs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		data.Failed = jobViewsFromFailed(jobs)
	}
	d.render(w, data)
}

func (d *Dashboard) handleRetry(w http.ResponseWriter, r *http.Request) {
	d.action(w, r, func(id string) error { return d.failed.Retry(r.Context(), id) })
}

func (d *Dashboard) handleForget(w http.ResponseWriter, r *http.Request) {
	d.action(w, r, func(id string) error { return d.failed.Forget(r.Context(), id) })
}

// action is the shared retry/forget POST path: method guard, CSRF check,
// store mutation, then redirect back to the failed list (PRG).
func (d *Dashboard) action(w http.ResponseWriter, r *http.Request, fn func(id string) error) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !d.validToken(r) {
		http.Error(w, "csrf token mismatch", http.StatusForbidden)
		return
	}
	if d.failed == nil {
		http.Error(w, "no failed-job store configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PostFormValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	switch err := fn(id); {
	case err == nil:
		http.Redirect(w, r, "failed", http.StatusSeeOther)
	case err == ErrNotFound:
		http.Error(w, "job not found", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (d *Dashboard) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	payload := struct {
		Queues []QueueStat `json:"queues"`
		Totals QueueStat   `json:"totals"`
	}{}
	if d.stats != nil {
		payload.Queues = d.stats.Snapshot()
		payload.Totals = d.stats.Totals()
	}
	writeJSON(w, payload)
}

func (d *Dashboard) handleAPIFailed(w http.ResponseWriter, r *http.Request) {
	var jobs []FailedJob
	if d.failed != nil {
		var err error
		jobs, err = d.failed.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, struct {
		Failed []FailedJob `json:"failed"`
	}{Failed: jobs})
}

func (d *Dashboard) render(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Template execution writes directly; on error the partial output is
	// already committed, so surface the failure in the log path via the
	// header status not yet sent. We execute into the ResponseWriter and
	// rely on html/template's auto-escaping for all dynamic fields.
	if err := d.tmpl.Execute(w, data); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// --- CSRF: double-submit cookie ----------------------------------------

// issueToken returns the CSRF token for this client, minting and setting a
// fresh cookie when none is present. The token is embedded in rendered
// forms and must be echoed back in the "csrf" field on POST.
func (d *Dashboard) issueToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && c.Value != "" {
		return c.Value
	}
	tok := randomToken()
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: false, // readable by the form; this is double-submit, not session
		SameSite: http.SameSiteLaxMode,
		Expires:  nowFunc().Add(12 * time.Hour),
	})
	return tok
}

// validToken checks the posted "csrf" field against the cookie using a
// constant-time compare.
func (d *Dashboard) validToken(r *http.Request) bool {
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
		// rand.Read on a healthy host does not fail; fall back to a
		// time-derived token rather than panicking inside a handler.
		return hex.EncodeToString([]byte(nowFunc().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
