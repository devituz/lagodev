package telescope

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// xssPayload is an adversarial string that, if interpolated into HTML without
// escaping, would execute as script (stored XSS). Every assertion below feeds
// it through a recorded entry and demands it never appears verbatim in the
// rendered dashboard.
const xssPayload = `<script>alert('xss')</script>`

// renderAll drives every HTML route of the dashboard for the given recorder and
// returns the concatenated bodies, so a single scan can prove escaping across
// the overview, per-type list and detail pages.
func renderAll(t *testing.T, rec *Recorder) string {
	t.Helper()
	h := http.StripPrefix("/telescope", rec.Handler(HandlerOptions{}))

	var sb strings.Builder
	paths := []string{"/telescope/", "/telescope/request", "/telescope/query", "/telescope/exception", "/telescope/log"}
	for _, e := range rec.Entries() {
		paths = append(paths, "/telescope/entry/"+e.ID)
	}
	for _, p := range paths {
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, p, nil))
		if rw.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", p, rw.Code)
		}
		sb.WriteString(rw.Body.String())
	}
	return sb.String()
}

// TestDashboardEscapesAdversarialEntries is the stored-XSS regression guard. It
// records request bodies, query strings, exception messages, log context and a
// header-derived field that all carry a <script> payload, renders every HTML
// route, and asserts the raw payload never reaches the browser unescaped while
// its escaped form does.
func TestDashboardEscapesAdversarialEntries(t *testing.T) {
	rec := NewRecorder(Options{})
	ctx := ContextWithRequestID(context.Background(), "xss-req")

	rec.RecordRequest(ctx, RequestEntry{
		Method: "GET",
		Path:   "/search?q=" + xssPayload,
		Status: 200,
		IP:     xssPayload, // header-derived (X-Forwarded-For) reaches IP unfiltered
	})
	rec.RecordQuery(ctx, QueryEntry{
		SQL:      "select * from t where name = '" + xssPayload + "'",
		Bindings: []any{xssPayload},
	})
	rec.RecordException(ctx, ExceptionEntry{
		Err:   errors.New(xssPayload),
		Class: xssPayload,
		Stack: "at " + xssPayload,
	})
	rec.RecordLog(ctx, LogEntry{
		Level:   "error",
		Message: xssPayload,
		Context: map[string]any{"evil": xssPayload},
	})
	rec.RecordMail(ctx, MailEntry{Subject: xssPayload, To: []string{xssPayload}})
	rec.RecordCache(ctx, CacheEntry{Operation: "get", Key: xssPayload, Value: xssPayload})

	body := renderAll(t, rec)

	if strings.Contains(body, xssPayload) {
		t.Fatal("adversarial <script> payload rendered unescaped: stored XSS")
	}
	if strings.Contains(body, "<script>alert") {
		t.Fatal("an unescaped <script> tag reached the rendered output")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("expected the payload to appear HTML-escaped in the output")
	}
}

// TestDashboardEscapesHeaderDerivedValues proves that user-controlled request
// headers (X-Request-ID, X-Forwarded-For) which flow through Middleware into a
// stored Request entry are escaped when the dashboard renders them.
func TestDashboardEscapesHeaderDerivedValues(t *testing.T) {
	rec := NewRecorder(Options{})
	srv := rec.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("X-Forwarded-For", xssPayload)
	srv.ServeHTTP(httptest.NewRecorder(), req)

	body := renderAll(t, rec)
	if strings.Contains(body, "<script>alert") {
		t.Fatal("header-derived value rendered unescaped: stored XSS via X-Forwarded-For")
	}
}

// --- auth gate -----------------------------------------------------------

// TestHandlerIsUnauthenticatedByDefault documents that Handler does not gate
// itself: mounted bare, it serves sensitive recorded data to anyone. This is
// the posture that makes RequireBasicAuth necessary in front of it.
func TestHandlerIsUnauthenticatedByDefault(t *testing.T) {
	rec := NewRecorder(Options{})
	rec.RecordQuery(context.Background(), QueryEntry{SQL: "select secret from vault"})

	h := http.StripPrefix("/telescope", rec.Handler(HandlerOptions{}))
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/telescope/query", nil))

	if rw.Code != http.StatusOK {
		t.Fatalf("bare handler status = %d, want 200 (auth-agnostic)", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "select secret from vault") {
		t.Fatal("expected the bare handler to leak recorded SQL")
	}
}

func basicAuthHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// TestRequireBasicAuthGate verifies the documented prod-safe wiring: no
// credentials and wrong credentials are rejected with 401 + WWW-Authenticate,
// correct credentials pass through.
func TestRequireBasicAuthGate(t *testing.T) {
	rec := NewRecorder(Options{})
	rec.RecordQuery(context.Background(), QueryEntry{SQL: "select secret from vault"})

	dash := http.StripPrefix("/telescope", rec.Handler(HandlerOptions{}))
	guarded := RequireBasicAuth("ops", "s3cret", dash)

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no credentials", "", http.StatusUnauthorized},
		{"wrong password", basicAuthHeader("ops", "nope"), http.StatusUnauthorized},
		{"wrong user", basicAuthHeader("root", "s3cret"), http.StatusUnauthorized},
		{"correct", basicAuthHeader("ops", "s3cret"), http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/telescope/query", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rw := httptest.NewRecorder()
			guarded.ServeHTTP(rw, req)
			if rw.Code != c.want {
				t.Fatalf("status = %d, want %d", rw.Code, c.want)
			}
			if c.want == http.StatusUnauthorized {
				if rw.Header().Get("WWW-Authenticate") == "" {
					t.Error("401 must carry WWW-Authenticate")
				}
				if strings.Contains(rw.Body.String(), "select secret from vault") {
					t.Fatal("unauthenticated response leaked recorded SQL")
				}
			}
		})
	}
}

func TestRequireBasicAuthPanicsOnEmptyCredentials(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when both username and password are empty")
		}
	}()
	RequireBasicAuth("", "", http.NotFoundHandler())
}

// --- bounded memory ------------------------------------------------------

// TestRingBufferBoundedFarBeyondCapacity pushes an order of magnitude more
// entries than the capacity and asserts retention is capped exactly at the ring
// size, with no over- or under-retention.
func TestRingBufferBoundedFarBeyondCapacity(t *testing.T) {
	const cap = 64
	rec := NewRecorder(Options{Capacity: cap})
	for i := 0; i < cap*50; i++ {
		rec.RecordLog(context.Background(), LogEntry{Message: fmt.Sprintf("m%d", i)})
	}
	if got := len(rec.Entries()); got != cap {
		t.Fatalf("retained = %d, want exactly %d", got, cap)
	}
	// Newest must be the last one written, oldest evicted.
	if got := rec.Entries()[0].Payload["message"]; got != fmt.Sprintf("m%d", cap*50-1) {
		t.Fatalf("newest message = %v, want m%d", got, cap*50-1)
	}
}

// TestNPlusMapBoundedWithoutRequestTermination is the map-leak guard. It records
// queries under a huge number of distinct, never-terminated request ids (no
// matching RecordRequest), which previously grew rec.nplus without bound. The
// accumulator must stay capped at maxNPlusRequests.
func TestNPlusMapBoundedWithoutRequestTermination(t *testing.T) {
	rec := NewRecorder(Options{Capacity: 16})
	for i := 0; i < maxNPlusRequests*3; i++ {
		ctx := ContextWithRequestID(context.Background(), fmt.Sprintf("never-ends-%d", i))
		rec.RecordQuery(ctx, QueryEntry{SQL: "select * from t where id = ?"})
	}

	rec.mu.Lock()
	gotMap := len(rec.nplus)
	gotOrder := len(rec.nplusOrder)
	rec.mu.Unlock()

	if gotMap > maxNPlusRequests {
		t.Fatalf("nplus map grew to %d, exceeds bound %d", gotMap, maxNPlusRequests)
	}
	if gotMap != gotOrder {
		t.Fatalf("nplus map (%d) and order list (%d) out of sync", gotMap, gotOrder)
	}
}

// TestNPlusMapDrainedByRequestTermination confirms the original N+1-map cleanup
// still holds: each completed request drops its accumulator.
func TestNPlusMapDrainedByRequestTermination(t *testing.T) {
	rec := NewRecorder(Options{})
	for i := 0; i < 2000; i++ {
		ctx := ContextWithRequestID(context.Background(), fmt.Sprintf("req-%d", i))
		rec.RecordQuery(ctx, QueryEntry{SQL: "select * from t where id = ?"})
		rec.RecordRequest(ctx, RequestEntry{Method: "GET", Path: "/", Status: 200})
	}
	rec.mu.Lock()
	gotMap := len(rec.nplus)
	gotOrder := len(rec.nplusOrder)
	rec.mu.Unlock()
	if gotMap != 0 || gotOrder != 0 {
		t.Fatalf("after terminating every request: nplus=%d order=%d, want 0/0", gotMap, gotOrder)
	}
}

// TestResetClearsNPlusOrder makes sure Reset wipes the eviction-order list too,
// so it cannot retain dangling ids after a clear.
func TestResetClearsNPlusOrder(t *testing.T) {
	rec := NewRecorder(Options{})
	for i := 0; i < 10; i++ {
		ctx := ContextWithRequestID(context.Background(), fmt.Sprintf("r%d", i))
		rec.RecordQuery(ctx, QueryEntry{SQL: "select 1"})
	}
	rec.Reset()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.nplus) != 0 || len(rec.nplusOrder) != 0 {
		t.Fatalf("Reset left nplus=%d order=%d, want 0/0", len(rec.nplus), len(rec.nplusOrder))
	}
}

// --- concurrency ---------------------------------------------------------

// TestConcurrentRecorderHammered races every mutating and reading path
// simultaneously. Run under -race it must report no data race and leave the
// internal maps consistent.
func TestConcurrentRecorderHammered(t *testing.T) {
	rec := NewRecorder(Options{Capacity: 128})
	const workers = 24
	const iters = 200

	var wg sync.WaitGroup
	start := make(chan struct{})

	spawn := func(fn func(g, i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iters; i++ {
				fn(0, i)
			}
		}()
	}

	for g := 0; g < workers; g++ {
		g := g
		spawn(func(_, i int) {
			ctx := ContextWithRequestID(context.Background(), fmt.Sprintf("g%d-%d", g, i%7))
			rec.RecordQuery(ctx, QueryEntry{SQL: "select * from t where id = ?"})
		})
		spawn(func(_, i int) {
			ctx := ContextWithRequestID(context.Background(), fmt.Sprintf("g%d-%d", g, i%7))
			rec.RecordRequest(ctx, RequestEntry{Method: "GET", Path: "/", Status: 200})
		})
		spawn(func(_, _ int) { _ = rec.Entries() })
		spawn(func(_, _ int) { _ = rec.Filter(TypeQuery, 10) })
		if g%6 == 0 {
			spawn(func(_, _ int) { rec.Reset() })
		}
	}

	close(start)
	wg.Wait()

	// Maps must remain consistent after the storm.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.nplus) != len(rec.nplusOrder) {
		t.Fatalf("nplus map (%d) / order (%d) desynced under concurrency", len(rec.nplus), len(rec.nplusOrder))
	}
	if len(rec.nplus) > maxNPlusRequests {
		t.Fatalf("nplus grew past bound under concurrency: %d", len(rec.nplus))
	}
}
