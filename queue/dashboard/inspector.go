package dashboard

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// mustPageTemplate parses the shared dashboard document. It panics on a
// malformed template, which can only happen on a developer error since the
// source is a compile-time constant.
func mustPageTemplate() *template.Template {
	return template.Must(template.New("dashboard").Parse(pageTemplate))
}

// PageSize is the number of jobs returned per page by the Inspector-backed
// failed / pending listings.
const PageSize = 50

// JobView is a flat, render-ready row describing one job for the dashboard.
// It is intentionally decoupled from queue.Job so an Inspector backed by a
// database can project only the columns it needs. Every string field is
// auto-escaped by html/template at render time.
type JobView struct {
	ID       string    `json:"id"`
	Queue    string    `json:"queue"`
	Name     string    `json:"name"`
	Err      string    `json:"error,omitempty"`
	Attempts int       `json:"attempts"`
	FailedAt time.Time `json:"failed_at,omitempty"`
}

// StatsView is the snapshot returned by Inspector.Stats: per-queue rows plus
// an aggregate total. It mirrors the JSON shape of the /queues/api/stats
// endpoint.
type StatsView struct {
	Queues []QueueStat `json:"queues"`
	Totals QueueStat   `json:"totals"`
}

// Inspector is the read/act seam the Handler renders. It is satisfied by an
// in-memory adapter (NewInspector), a sqlqueue-backed implementation, or a
// test fake. All methods must be safe for concurrent use.
type Inspector interface {
	// Stats returns the per-queue counters plus aggregate totals.
	Stats() (StatsView, error)
	// Pending lists jobs waiting on the named queue, paginated (1-based).
	Pending(queue string, page int) ([]JobView, error)
	// Failed lists dead-lettered jobs across all queues, paginated (1-based).
	Failed(page int) ([]JobView, error)
	// Retry re-enqueues the failed job with the given id and drops the record.
	Retry(id string) error
	// Forget drops the failed job with the given id without re-enqueueing.
	Forget(id string) error
	// FlushFailed drops every failed job.
	FlushFailed() error
}

// --- in-memory Inspector adapter ---------------------------------------

// memInspector adapts the in-box Stats collector and MemoryFailedJobs store
// to the Inspector interface, so single-process apps get a working dashboard
// with no extra wiring.
type memInspector struct {
	stats  *Stats
	failed *MemoryFailedJobs
}

// NewInspector builds an Inspector over an in-process Stats collector and
// MemoryFailedJobs store. Either may be nil: a nil Stats yields empty
// counters and a nil store yields an empty failed list (and rejects the
// mutating actions).
func NewInspector(stats *Stats, failed *MemoryFailedJobs) Inspector {
	return &memInspector{stats: stats, failed: failed}
}

func (m *memInspector) Stats() (StatsView, error) {
	if m.stats == nil {
		return StatsView{}, nil
	}
	return StatsView{Queues: m.stats.Snapshot(), Totals: m.stats.Totals()}, nil
}

func (m *memInspector) Pending(queue string, page int) ([]JobView, error) {
	// MemoryFailedJobs only tracks dead-lettered jobs; pending jobs live in
	// the live Queue driver, which this adapter does not hold. Return an
	// empty page rather than guessing. A sqlqueue-backed Inspector projects
	// real pending rows.
	return nil, nil
}

func (m *memInspector) Failed(page int) ([]JobView, error) {
	if m.failed == nil {
		return nil, nil
	}
	all, err := m.failed.List(context.Background())
	if err != nil {
		return nil, err
	}
	return paginate(jobViewsFromFailed(all), page), nil
}

func (m *memInspector) Retry(id string) error {
	if m.failed == nil {
		return ErrNotFound
	}
	return m.failed.Retry(context.Background(), id)
}

func (m *memInspector) Forget(id string) error {
	if m.failed == nil {
		return ErrNotFound
	}
	return m.failed.Forget(context.Background(), id)
}

func (m *memInspector) FlushFailed() error {
	if m.failed == nil {
		return nil
	}
	all, err := m.failed.List(context.Background())
	if err != nil {
		return err
	}
	for _, f := range all {
		if err := m.failed.Forget(context.Background(), f.ID); err != nil && err != ErrNotFound {
			return err
		}
	}
	return nil
}

var _ Inspector = (*memInspector)(nil)

// jobViewsFromFailed flattens stored FailedJob records into render-ready
// JobView rows.
func jobViewsFromFailed(in []FailedJob) []JobView {
	out := make([]JobView, 0, len(in))
	for _, f := range in {
		out = append(out, JobView{
			ID:       f.ID,
			Queue:    f.Queue,
			Name:     f.Job.Name,
			Err:      f.Err,
			Attempts: f.Job.Attempts,
			FailedAt: f.FailedAt,
		})
	}
	return out
}

// paginate returns the 1-based page slice of size PageSize. Out-of-range
// pages yield an empty slice.
func paginate(rows []JobView, page int) []JobView {
	if page < 1 {
		page = 1
	}
	start := (page - 1) * PageSize
	if start >= len(rows) {
		return nil
	}
	end := start + PageSize
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end]
}

// --- Inspector-backed Handler ------------------------------------------

// inspectorHandler renders the dashboard over an Inspector. It shares the
// page template and CSRF machinery with the legacy Dashboard.
type inspectorHandler struct {
	insp Inspector
	dash *Dashboard // reused only for tmpl + CSRF helpers
}

// Handler returns an http.Handler exposing the full dashboard over insp.
// Mount it under a prefix with http.StripPrefix; routes are absolute under
// that prefix:
//
//	GET  /queues/                       overview (stats + throughput)
//	GET  /queues/failed                 failed list with retry/forget/flush
//	POST /queues/failed/{id}/retry      re-enqueue + drop  (csrf)
//	POST /queues/failed/{id}/forget     drop               (csrf)
//	POST /queues/failed/flush           drop all           (csrf)
//	GET  /queues/api/stats              JSON stats snapshot
//
// All POST routes are CSRF-guarded via the double-submit cookie issued on
// the rendered pages. The handler is safe for concurrent use.
func Handler(insp Inspector) http.Handler {
	h := &inspectorHandler{
		insp: insp,
		dash: &Dashboard{tmpl: mustPageTemplate()},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/queues/", h.handleOverview)
	mux.HandleFunc("/queues/failed", h.handleFailed)
	mux.HandleFunc("/queues/failed/flush", h.handleFlush)
	mux.HandleFunc("/queues/failed/", h.handleFailedAction)
	mux.HandleFunc("/queues/api/stats", h.handleAPIStats)
	return mux
}

func (h *inspectorHandler) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/queues/" {
		http.NotFound(w, r)
		return
	}
	data := pageData{Title: "Queue Overview", Active: "overview", CSRF: h.dash.issueToken(w, r)}
	sv, err := h.insp.Stats()
	if err != nil {
		http.Error(w, "stats: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data.Stats = sv.Queues
	data.Totals = sv.Totals
	h.render(w, data)
}

func (h *inspectorHandler) handleFailed(w http.ResponseWriter, r *http.Request) {
	page := pageParam(r)
	rows, err := h.insp.Failed(page)
	if err != nil {
		http.Error(w, "failed list: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := pageData{
		Title:  "Failed Jobs",
		Active: "failed",
		CSRF:   h.dash.issueToken(w, r),
		Failed: rows,
		Page:   page,
	}
	// A full page implies there may be a next one; pager shows when paging
	// is meaningful (beyond page 1 or a full first page).
	data.HasNext = len(rows) == PageSize
	data.PrevPage = page - 1
	data.NextPage = page + 1
	data.HasPager = page > 1 || data.HasNext
	h.render(w, data)
}

func (h *inspectorHandler) handleFlush(w http.ResponseWriter, r *http.Request) {
	if !h.guardPost(w, r) {
		return
	}
	if err := h.insp.FlushFailed(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The POST URL is ".../failed/flush"; a browser resolves a relative
	// redirect against ".../failed/", so "./failed" would land on
	// ".../failed/failed". Step up one segment to reach the list.
	http.Redirect(w, r, "../failed", http.StatusSeeOther)
}

// handleFailedAction dispatches POST /queues/failed/{id}/{retry|forget}.
func (h *inspectorHandler) handleFailedAction(w http.ResponseWriter, r *http.Request) {
	// Path here is "/queues/failed/<id>/<action>".
	const prefix = "/queues/failed/"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	id, action, ok := strings.Cut(rest, "/")
	if !ok || id == "" || action == "" {
		http.NotFound(w, r)
		return
	}
	if !h.guardPost(w, r) {
		return
	}
	var err error
	switch action {
	case "retry":
		err = h.insp.Retry(id)
	case "forget":
		err = h.insp.Forget(id)
	default:
		http.NotFound(w, r)
		return
	}
	switch {
	case err == nil:
		http.Redirect(w, r, "../../failed", http.StatusSeeOther)
	case err == ErrNotFound:
		http.Error(w, "job not found", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *inspectorHandler) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	sv, err := h.insp.Stats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(sv); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// guardPost enforces POST + CSRF for the mutating routes, writing the error
// response itself and reporting whether the caller may proceed.
func (h *inspectorHandler) guardPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return false
	}
	if !h.dash.validToken(r) {
		http.Error(w, "csrf token mismatch", http.StatusForbidden)
		return false
	}
	return true
}

func (h *inspectorHandler) render(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.dash.tmpl.Execute(w, data); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
	}
}

func pageParam(r *http.Request) int {
	p, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

// --- throughput sampler ------------------------------------------------

// ThroughputSample is one reading of the jobs/min rate for a queue, taken at
// a fixed cadence by Sampler.
type ThroughputSample struct {
	At    time.Time `json:"at"`
	Queue string    `json:"queue"`
	Rate  float64   `json:"rate"` // jobs processed per minute over the trailing window
}

// Sampler periodically reads an Inspector's processed counts and derives a
// jobs/min rate per queue over a sliding window. It runs a single background
// goroutine that must be stopped with Stop to avoid a leak.
type Sampler struct {
	insp     Inspector
	interval time.Duration
	window   time.Duration

	mu      sync.Mutex
	prev    map[string]uint64 // last-seen processed count per queue
	samples map[string][]ThroughputSample

	runMu    sync.Mutex // guards started/stop lifecycle
	started  bool
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewSampler builds a Sampler reading insp every interval and reporting a
// jobs/min rate averaged over window. Call Start to launch the goroutine and
// Stop to shut it down. interval and window default to 5s / 1m when <= 0.
func NewSampler(insp Inspector, interval, window time.Duration) *Sampler {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if window <= 0 {
		window = time.Minute
	}
	return &Sampler{
		insp:     insp,
		interval: interval,
		window:   window,
		prev:     map[string]uint64{},
		samples:  map[string][]ThroughputSample{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start launches the sampling goroutine. It returns the receiver for
// chaining. Start is idempotent: a second call is a no-op.
func (s *Sampler) Start() *Sampler {
	s.runMu.Lock()
	if !s.started {
		s.started = true
		go s.loop()
	}
	s.runMu.Unlock()
	return s
}

func (s *Sampler) loop() {
	defer close(s.done)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	s.sample() // prime baseline immediately
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.sample()
		}
	}
}

// sample reads current processed counts and records the per-interval delta
// scaled to jobs/min, pruning samples older than the window.
func (s *Sampler) sample() {
	sv, err := s.insp.Stats()
	if err != nil {
		return
	}
	now := nowFunc()
	perMin := float64(time.Minute) / float64(s.interval)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range sv.Queues {
		prev, seen := s.prev[q.Queue]
		s.prev[q.Queue] = q.Processed
		if !seen {
			continue // first reading establishes the baseline only
		}
		delta := float64(int64(q.Processed) - int64(prev))
		if delta < 0 {
			delta = 0
		}
		sample := ThroughputSample{At: now, Queue: q.Queue, Rate: delta * perMin}
		kept := s.samples[q.Queue][:0]
		cutoff := now.Add(-s.window)
		for _, old := range s.samples[q.Queue] {
			if old.At.After(cutoff) {
				kept = append(kept, old)
			}
		}
		s.samples[q.Queue] = append(kept, sample)
	}
}

// Sample forces an immediate reading. It is exported so tests can drive the
// sampler deterministically without waiting on the ticker.
func (s *Sampler) Sample() { s.sample() }

// Rate returns the most recent jobs/min reading for queue, or 0 if none has
// been recorded.
func (s *Sampler) Rate(queue string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.samples[queue]
	if len(ss) == 0 {
		return 0
	}
	return ss[len(ss)-1].Rate
}

// Window returns a copy of the retained samples for queue (oldest first).
func (s *Sampler) Window(queue string) []ThroughputSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.samples[queue]
	out := make([]ThroughputSample, len(src))
	copy(out, src)
	return out
}

// Stop signals the sampling goroutine to exit and blocks until it has. It is
// safe to call multiple times and safe to call when Start was never invoked
// (it then returns promptly instead of blocking on a goroutine that will
// never close done).
func (s *Sampler) Stop() {
	s.runMu.Lock()
	started := s.started
	s.runMu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
	if started {
		<-s.done
	}
}
