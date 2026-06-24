package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeInspector is an in-memory Inspector for handler tests. It records which
// mutating methods were invoked so the redirect tests can assert the call
// reached the seam.
type fakeInspector struct {
	mu sync.Mutex

	stats StatsView
	pages map[int][]JobView // failed pages, 1-based
	pend  []JobView

	retried  []string
	forgot   []string
	flushed  int
	failErr  error // returned by Retry/Forget/Flush when set
	notFound map[string]bool
}

func newFakeInspector() *fakeInspector {
	return &fakeInspector{
		pages:    map[int][]JobView{},
		notFound: map[string]bool{},
	}
}

func (f *fakeInspector) Stats() (StatsView, error) { return f.stats, nil }

func (f *fakeInspector) Pending(queue string, page int) ([]JobView, error) {
	return f.pend, nil
}

func (f *fakeInspector) Failed(page int) ([]JobView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pages[page], nil
}

func (f *fakeInspector) Retry(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notFound[id] {
		return ErrNotFound
	}
	if f.failErr != nil {
		return f.failErr
	}
	f.retried = append(f.retried, id)
	return nil
}

func (f *fakeInspector) Forget(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notFound[id] {
		return ErrNotFound
	}
	if f.failErr != nil {
		return f.failErr
	}
	f.forgot = append(f.forgot, id)
	return nil
}

func (f *fakeInspector) FlushFailed() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return f.failErr
	}
	f.flushed++
	return nil
}

var _ Inspector = (*fakeInspector)(nil)

// getWithCSRF performs a GET against the handler and returns the body plus the
// CSRF token/cookie the page issued, so follow-up POSTs can echo them.
func getWithCSRF(t *testing.T, h http.Handler, path string) (body string, token string, cookie *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %q", path, rec.Code, rec.Body.String())
	}
	res := rec.Result()
	for _, c := range res.Cookies() {
		if c.Name == csrfCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("GET %s: no CSRF cookie issued", path)
	}
	return rec.Body.String(), cookie.Value, cookie
}

func postForm(h http.Handler, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestOverviewRendersStats(t *testing.T) {
	f := newFakeInspector()
	f.stats = StatsView{
		Queues: []QueueStat{{Queue: "emails", Pushed: 10, Processed: 7, Failed: 1, Pending: 2, Throughput: 1.5}},
		Totals: QueueStat{Pushed: 10, Processed: 7, Failed: 1, Pending: 2, Throughput: 1.5},
	}
	h := Handler(f)
	body, _, _ := getWithCSRF(t, h, "/queues/")
	for _, want := range []string{"emails", "Pushed", "Processed", "Pending", "1.50"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview missing %q\n%s", want, body)
		}
	}
}

func TestFailedListRendersActions(t *testing.T) {
	f := newFakeInspector()
	f.pages[1] = []JobView{
		{ID: "job-1", Queue: "emails", Name: "SendWelcome", Err: "smtp down", FailedAt: time.Now()},
	}
	h := Handler(f)
	body, _, _ := getWithCSRF(t, h, "/queues/failed")
	for _, want := range []string{
		"job-1", "SendWelcome", "smtp down",
		`action="./failed/job-1/retry"`,
		`action="./failed/job-1/forget"`,
		`action="./failed/flush"`,
		"csrf",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("failed list missing %q\n%s", want, body)
		}
	}
}

func TestRetryInvokesInspectorAndRedirects(t *testing.T) {
	f := newFakeInspector()
	f.pages[1] = []JobView{{ID: "abc", Queue: "q", Name: "N"}}
	h := Handler(f)
	_, token, cookie := getWithCSRF(t, h, "/queues/failed")

	rec := postForm(h, "/queues/failed/abc/retry", url.Values{"csrf": {token}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("retry: want 303, got %d (%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "failed") {
		t.Errorf("retry: redirect to %q, want failed list", loc)
	}
	if len(f.retried) != 1 || f.retried[0] != "abc" {
		t.Errorf("retry not forwarded to inspector: %v", f.retried)
	}
}

func TestForgetInvokesInspectorAndRedirects(t *testing.T) {
	f := newFakeInspector()
	f.pages[1] = []JobView{{ID: "xyz", Queue: "q", Name: "N"}}
	h := Handler(f)
	_, token, cookie := getWithCSRF(t, h, "/queues/failed")

	rec := postForm(h, "/queues/failed/xyz/forget", url.Values{"csrf": {token}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("forget: want 303, got %d", rec.Code)
	}
	if len(f.forgot) != 1 || f.forgot[0] != "xyz" {
		t.Errorf("forget not forwarded: %v", f.forgot)
	}
}

func TestFlushInvokesInspectorAndRedirects(t *testing.T) {
	f := newFakeInspector()
	h := Handler(f)
	_, token, cookie := getWithCSRF(t, h, "/queues/failed")

	const flushURL = "/queues/failed/flush"
	rec := postForm(h, flushURL, url.Values{"csrf": {token}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("flush: want 303, got %d", rec.Code)
	}
	if f.flushed != 1 {
		t.Errorf("flush not forwarded: count=%d", f.flushed)
	}
	// The redirect is relative; a browser resolves it against the POST URL.
	// It must land back on the failed list ("/queues/failed"), not a bogus
	// "/queues/failed/failed" path that the subtree handler would 404.
	base, _ := url.Parse(flushURL)
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("flush: bad Location %q: %v", rec.Header().Get("Location"), err)
	}
	if got := base.ResolveReference(loc).Path; got != "/queues/failed" {
		t.Errorf("flush: redirect resolves to %q, want %q", got, "/queues/failed")
	}
}

func TestRetryNotFound(t *testing.T) {
	f := newFakeInspector()
	f.notFound["ghost"] = true
	h := Handler(f)
	_, token, cookie := getWithCSRF(t, h, "/queues/failed")

	rec := postForm(h, "/queues/failed/ghost/retry", url.Values{"csrf": {token}}, cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("retry missing: want 404, got %d", rec.Code)
	}
}

func TestAPIStatsShape(t *testing.T) {
	f := newFakeInspector()
	f.stats = StatsView{
		Queues: []QueueStat{{Queue: "emails", Pushed: 3, Processed: 2, Throughput: 0.5}},
		Totals: QueueStat{Pushed: 3, Processed: 2, Throughput: 0.5},
	}
	h := Handler(f)
	req := httptest.NewRequest(http.MethodGet, "/queues/api/stats", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("api/stats: status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("api/stats: content-type %q", ct)
	}
	var payload struct {
		Queues []QueueStat `json:"queues"`
		Totals QueueStat   `json:"totals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("api/stats: bad JSON: %v\n%s", err, rec.Body.String())
	}
	if len(payload.Queues) != 1 || payload.Queues[0].Queue != "emails" {
		t.Errorf("api/stats: queues = %+v", payload.Queues)
	}
	if payload.Totals.Pushed != 3 || payload.Totals.Throughput != 0.5 {
		t.Errorf("api/stats: totals = %+v", payload.Totals)
	}
}

func TestCSRFRejectionWithoutToken(t *testing.T) {
	f := newFakeInspector()
	h := Handler(f)
	_, _, cookie := getWithCSRF(t, h, "/queues/failed")

	// Cookie present but no csrf form field -> reject.
	rec := postForm(h, "/queues/failed/abc/retry", url.Values{"id": {"abc"}}, cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing csrf field: want 403, got %d", rec.Code)
	}

	// Wrong token -> reject.
	rec = postForm(h, "/queues/failed/abc/retry", url.Values{"csrf": {"wrong"}}, cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong csrf: want 403, got %d", rec.Code)
	}

	// No cookie at all -> reject.
	rec = postForm(h, "/queues/failed/flush", url.Values{"csrf": {"whatever"}}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no cookie: want 403, got %d", rec.Code)
	}

	if len(f.retried) != 0 || f.flushed != 0 {
		t.Errorf("inspector mutated despite CSRF failure: retried=%v flushed=%d", f.retried, f.flushed)
	}
}

func TestGetMethodNotAllowedOnPostRoutes(t *testing.T) {
	f := newFakeInspector()
	h := Handler(f)
	req := httptest.NewRequest(http.MethodGet, "/queues/failed/flush", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET flush: want 405, got %d", rec.Code)
	}
}

// TestSamplerThroughputDeterministic drives nowFunc and Stats so the derived
// jobs/min rate is exact, with no sleeping.
func TestSamplerThroughputDeterministic(t *testing.T) {
	orig := nowFunc
	defer func() { nowFunc = orig }()

	base := time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)
	var clock time.Time
	clock = base
	nowFunc = func() time.Time { return clock }

	f := newFakeInspector()
	f.stats = StatsView{Queues: []QueueStat{{Queue: "emails", Processed: 0}}}

	// interval 10s, window 1m. perMin = 60/10 = 6.
	s := NewSampler(f, 10*time.Second, time.Minute)
	defer s.Stop()

	s.Sample() // baseline: processed=0
	if got := s.Rate("emails"); got != 0 {
		t.Fatalf("baseline rate: want 0, got %v", got)
	}

	// +5 processed over the 10s interval -> 5 * 6 = 30 jobs/min.
	f.stats.Queues[0].Processed = 5
	clock = base.Add(10 * time.Second)
	s.Sample()
	if got := s.Rate("emails"); got != 30 {
		t.Fatalf("rate after +5: want 30, got %v", got)
	}

	// +2 more -> 2 * 6 = 12 jobs/min.
	f.stats.Queues[0].Processed = 7
	clock = base.Add(20 * time.Second)
	s.Sample()
	if got := s.Rate("emails"); got != 12 {
		t.Fatalf("rate after +2: want 12, got %v", got)
	}

	// Window prunes samples older than 1m. Advance well past the window and
	// take a no-delta sample; old readings must be gone.
	clock = base.Add(5 * time.Minute)
	s.Sample()
	if w := s.Window("emails"); len(w) != 1 {
		t.Fatalf("window prune: want 1 retained sample, got %d", len(w))
	}
}

// TestSamplerStopNoLeak verifies Stop terminates the goroutine: the second
// Stop returns promptly and a watchdog catches a hang.
func TestSamplerStopNoLeak(t *testing.T) {
	f := newFakeInspector()
	s := NewSampler(f, time.Millisecond, time.Minute).Start()

	done := make(chan struct{})
	go func() {
		s.Stop()
		s.Stop() // idempotent, must not panic or block
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Sampler.Stop did not return — goroutine leak")
	}
}
