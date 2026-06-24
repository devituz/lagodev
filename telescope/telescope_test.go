package telescope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecordAssignsIDAndTime(t *testing.T) {
	rec := NewRecorder(Options{})
	e := rec.Record(Entry{Type: TypeRequest})
	if e.ID == "" {
		t.Fatal("expected a generated id")
	}
	if e.Time.IsZero() {
		t.Fatal("expected a defaulted time")
	}
	if got := rec.Capacity(); got != DefaultCapacity {
		t.Fatalf("capacity = %d, want %d", got, DefaultCapacity)
	}
}

func TestRingBufferEvictsAtCapacity(t *testing.T) {
	rec := NewRecorder(Options{Capacity: 3})
	for i := 0; i < 10; i++ {
		rec.RecordLog(context.Background(), LogEntry{Level: "info", Message: fmt.Sprintf("m%d", i)})
	}
	got := rec.Entries()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (ring evicted)", len(got))
	}
	// Newest first: m9, m8, m7.
	wantTitles := []string{"info m9", "info m8", "info m7"}
	for i, w := range wantTitles {
		if got[i].Title() != w {
			t.Fatalf("entry[%d] title = %q, want %q", i, got[i].Title(), w)
		}
	}
}

func TestAllRecordTypes(t *testing.T) {
	rec := NewRecorder(Options{})
	ctx := context.Background()
	rec.RecordRequest(ctx, RequestEntry{Method: "GET", Path: "/x", Status: 200})
	rec.RecordQuery(ctx, QueryEntry{SQL: "select 1"})
	rec.RecordJob(ctx, JobEntry{Name: "Mailer", Status: "processed"})
	rec.RecordCache(ctx, CacheEntry{Operation: "get", Key: "k", Hit: true})
	rec.RecordMail(ctx, MailEntry{Subject: "Hi", To: []string{"a@b.c"}})
	rec.RecordException(ctx, ExceptionEntry{Err: errors.New("boom")})
	rec.RecordLog(ctx, LogEntry{Level: "warn", Message: "careful"})

	want := map[Type]int{
		TypeRequest: 1, TypeQuery: 1, TypeJob: 1, TypeCache: 1,
		TypeMail: 1, TypeException: 1, TypeLog: 1,
	}
	for typ, n := range want {
		if got := len(rec.Filter(typ, 0)); got != n {
			t.Errorf("Filter(%s) = %d, want %d", typ, got, n)
		}
	}
}

func TestRecordExceptionNilErrIgnored(t *testing.T) {
	rec := NewRecorder(Options{})
	e := rec.RecordException(context.Background(), ExceptionEntry{Err: nil})
	if e.ID != "" {
		t.Fatal("nil error should be ignored")
	}
	if len(rec.Entries()) != 0 {
		t.Fatal("nothing should be stored for a nil error")
	}
}

func TestCacheHitMissTags(t *testing.T) {
	rec := NewRecorder(Options{})
	ctx := context.Background()
	hit := rec.RecordCache(ctx, CacheEntry{Operation: "get", Key: "a", Hit: true})
	miss := rec.RecordCache(ctx, CacheEntry{Operation: "get", Key: "b", Hit: false})
	if !hit.hasTag("hit") {
		t.Error("expected hit tag")
	}
	if !miss.hasTag("miss") {
		t.Error("expected miss tag")
	}
}

func TestNPlusOneDetection(t *testing.T) {
	rec := NewRecorder(Options{})
	ctx := ContextWithRequestID(context.Background(), "req-1")

	// One parent query, then a burst of identical (literal-varying) child
	// queries that should trip the N+1 heuristic.
	rec.RecordQuery(ctx, QueryEntry{SQL: "select * from posts"})
	var flagged int
	for i := 0; i < 5; i++ {
		e := rec.RecordQuery(ctx, QueryEntry{
			SQL: fmt.Sprintf("select * from comments where post_id = %d", i),
		})
		if e.hasTag("n+1") {
			flagged++
		}
	}
	// First child sets the baseline; the next four are flagged repeats.
	if flagged != 4 {
		t.Fatalf("flagged = %d, want 4", flagged)
	}

	// The parent query and a different statement must not be flagged.
	for _, e := range rec.Filter(TypeQuery, 0) {
		sql, _ := e.Payload["sql"].(string)
		if strings.Contains(sql, "from posts") && e.hasTag("n+1") {
			t.Error("distinct query wrongly flagged as n+1")
		}
	}
}

func TestNPlusOneScopedPerRequest(t *testing.T) {
	rec := NewRecorder(Options{})
	sql := "select * from users where id = ?"
	// Same statement under two different request ids must not cross-flag.
	for _, id := range []string{"r1", "r2"} {
		ctx := ContextWithRequestID(context.Background(), id)
		e := rec.RecordQuery(ctx, QueryEntry{SQL: sql})
		if e.hasTag("n+1") {
			t.Fatalf("first query in %s wrongly flagged", id)
		}
	}
	// No request id at all: must never flag.
	for i := 0; i < 3; i++ {
		e := rec.RecordQuery(context.Background(), QueryEntry{SQL: sql})
		if e.hasTag("n+1") {
			t.Fatal("query without request id wrongly flagged")
		}
	}
}

func TestRecordRequestEvictsNPlusAccumulator(t *testing.T) {
	rec := NewRecorder(Options{})

	// Simulate many completed requests, each running a query (which seeds the
	// per-request N+1 accumulator) followed by the terminating Request entry
	// the middleware records. The accumulator must not leak one map per id.
	for i := 0; i < 1000; i++ {
		ctx := ContextWithRequestID(context.Background(), fmt.Sprintf("req-%d", i))
		rec.RecordQuery(ctx, QueryEntry{SQL: "select * from t where id = ?"})
		rec.RecordRequest(ctx, RequestEntry{Method: "GET", Path: "/", Status: 200})
	}

	rec.mu.Lock()
	leaked := len(rec.nplus)
	rec.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("nplus accumulator leaked %d request ids; want 0 after each request completed", leaked)
	}

	// N+1 detection must still work within a single in-flight request after the
	// eviction change.
	ctx := ContextWithRequestID(context.Background(), "live")
	rec.RecordQuery(ctx, QueryEntry{SQL: "select * from c where id = 1"})
	flagged := rec.RecordQuery(ctx, QueryEntry{SQL: "select * from c where id = 2"})
	if !flagged.hasTag("n+1") {
		t.Fatal("repeat query within a request should still be flagged n+1")
	}
}

func TestNormalizeSQL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT * FROM users WHERE id = 42", "select * from users where id = ?"},
		{"select  *  from\tusers", "select * from users"},
		{"WHERE name = 'bob'", "where name = ?"},
		{"WHERE id = $1", "where id = ?"},
	}
	for _, c := range cases {
		if got := normalizeSQL(c.in); got != c.want {
			t.Errorf("normalizeSQL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMiddlewareRecordsRequestAndCorrelatesQueries(t *testing.T) {
	rec := NewRecorder(Options{})

	mux := http.NewServeMux()
	mux.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
		// Child queries recorded inside the handler must inherit the
		// request id from the context Middleware injected.
		rec.RecordQuery(r.Context(), QueryEntry{SQL: "select * from users"})
		rec.RecordQuery(r.Context(), QueryEntry{SQL: "select * from roles"})
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	})

	srv := rec.Middleware()(mux)
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	rw := httptest.NewRecorder()
	srv.ServeHTTP(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rw.Code)
	}
	if rw.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID response header")
	}

	requests := rec.Filter(TypeRequest, 0)
	if len(requests) != 1 {
		t.Fatalf("request entries = %d, want 1", len(requests))
	}
	reqEntry := requests[0]
	if reqEntry.RequestID == "" {
		t.Fatal("request entry missing request id")
	}
	if got := reqEntry.Payload["status"]; got != 201 {
		t.Fatalf("recorded status = %v, want 201", got)
	}

	// Both queries must carry the same request id as the request entry.
	queries := rec.Filter(TypeQuery, 0)
	if len(queries) != 2 {
		t.Fatalf("query entries = %d, want 2", len(queries))
	}
	for _, q := range queries {
		if q.RequestID != reqEntry.RequestID {
			t.Fatalf("query req id = %q, want %q", q.RequestID, reqEntry.RequestID)
		}
	}
}

func TestMiddlewareHonoursInboundRequestID(t *testing.T) {
	rec := NewRecorder(Options{})
	srv := rec.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := RequestIDFromContext(r.Context())
		if !ok || id != "incoming-123" {
			t.Errorf("ctx request id = %q (ok=%v), want incoming-123", id, ok)
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "incoming-123")
	rw := httptest.NewRecorder()
	srv.ServeHTTP(rw, req)
	if got := rw.Header().Get("X-Request-ID"); got != "incoming-123" {
		t.Fatalf("response X-Request-ID = %q, want incoming-123", got)
	}
}

func newTestHandler(rec *Recorder) http.Handler {
	return http.StripPrefix("/telescope", rec.Handler(HandlerOptions{}))
}

func TestHandlerAPIFiltersByType(t *testing.T) {
	rec := NewRecorder(Options{})
	ctx := context.Background()
	rec.RecordQuery(ctx, QueryEntry{SQL: "select 1"})
	rec.RecordQuery(ctx, QueryEntry{SQL: "select 2"})
	rec.RecordMail(ctx, MailEntry{Subject: "Hi"})

	h := newTestHandler(rec)

	// Filtered to queries only.
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/telescope/api/entries?type=query", nil))
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d", rw.Code)
	}
	var resp struct {
		Type    Type    `json:"type"`
		Count   int     `json:"count"`
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 2 || len(resp.Entries) != 2 {
		t.Fatalf("count = %d, want 2", resp.Count)
	}
	for _, e := range resp.Entries {
		if e.Type != TypeQuery {
			t.Fatalf("got type %s in query filter", e.Type)
		}
	}

	// No filter returns everything.
	rw = httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/telescope/api/entries", nil))
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp.Count != 3 {
		t.Fatalf("unfiltered count = %d, want 3", resp.Count)
	}

	// Unknown type is a 400.
	rw = httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/telescope/api/entries?type=bogus", nil))
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("bogus type status = %d, want 400", rw.Code)
	}
}

func TestHandlerHTMLPagesRender(t *testing.T) {
	rec := NewRecorder(Options{})
	ctx := ContextWithRequestID(context.Background(), "corr-1")
	rec.RecordRequest(ctx, RequestEntry{Method: "GET", Path: "/home", Status: 200})
	q := rec.RecordQuery(ctx, QueryEntry{SQL: "select * from t where id = 1"})

	h := newTestHandler(rec)

	// Overview.
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/telescope/", nil))
	if rw.Code != http.StatusOK {
		t.Fatalf("overview status = %d", rw.Code)
	}
	if ct := rw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("overview content-type = %q", ct)
	}
	if !strings.Contains(rw.Body.String(), "GET /home") {
		t.Error("overview missing request title")
	}

	// Type list.
	rw = httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/telescope/query", nil))
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "select * from t") {
		t.Fatalf("list page wrong: code=%d", rw.Code)
	}

	// Detail (shows correlated entries from the same request).
	rw = httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/telescope/entry/"+q.ID, nil))
	if rw.Code != http.StatusOK {
		t.Fatalf("detail status = %d", rw.Code)
	}
	body := rw.Body.String()
	if !strings.Contains(body, q.ID) {
		t.Error("detail missing entry id")
	}
	if !strings.Contains(body, "Same request") {
		t.Error("detail should list correlated entries under the same request")
	}

	// Unknown type -> 404.
	rw = httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/telescope/nope", nil))
	if rw.Code != http.StatusNotFound {
		t.Fatalf("unknown type status = %d, want 404", rw.Code)
	}

	// Missing entry -> 404.
	rw = httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/telescope/entry/does-not-exist", nil))
	if rw.Code != http.StatusNotFound {
		t.Fatalf("missing entry status = %d, want 404", rw.Code)
	}
}

func TestHandlerHTMLEscapesPayload(t *testing.T) {
	rec := NewRecorder(Options{})
	rec.RecordException(context.Background(), ExceptionEntry{
		Err: errors.New("<script>alert(1)</script>"),
	})
	e := rec.Filter(TypeException, 0)[0]

	h := newTestHandler(rec)
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/telescope/entry/"+e.ID, nil))
	body := rw.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("payload was not HTML-escaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("expected escaped payload in output")
	}
}

func TestHandlerClearAll(t *testing.T) {
	rec := NewRecorder(Options{})
	rec.RecordLog(context.Background(), LogEntry{Message: "x"})
	if len(rec.Entries()) != 1 {
		t.Fatal("setup failed")
	}
	h := newTestHandler(rec)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodPost, "/telescope/clear", nil))
	if rw.Code != http.StatusSeeOther {
		t.Fatalf("clear status = %d, want 303", rw.Code)
	}
	if len(rec.Entries()) != 0 {
		t.Fatal("entries not cleared")
	}

	// GET on clear is rejected.
	rw = httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/telescope/clear", nil))
	if rw.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET clear status = %d, want 405", rw.Code)
	}
}

func TestConcurrentRecording(t *testing.T) {
	rec := NewRecorder(Options{Capacity: 256})
	const goroutines, perG = 16, 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			ctx := ContextWithRequestID(context.Background(), fmt.Sprintf("req-%d", g))
			for i := 0; i < perG; i++ {
				rec.RecordQuery(ctx, QueryEntry{SQL: "select * from t where id = 1"})
				rec.RecordLog(ctx, LogEntry{Level: "info", Message: "m"})
			}
		}(g)
	}
	// Concurrent readers exercising the snapshot/find paths under -race.
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = rec.Entries()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, _ = rec.Find("nope")
		}
	}()
	wg.Wait()

	// Ring must be full (capacity) and never over.
	if got := len(rec.Entries()); got != 256 {
		t.Fatalf("entries = %d, want 256 (ring cap)", got)
	}
}

func TestMiddlewareTimesRequest(t *testing.T) {
	// Drive a fake clock so the recorded duration is deterministic.
	var calls int
	base := time.Unix(0, 0)
	rec := NewRecorder(Options{Now: func() time.Time {
		calls++
		// First call = request start, second = end (+5ms).
		if calls == 1 {
			return base
		}
		return base.Add(5 * time.Millisecond)
	}})
	srv := rec.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	e := rec.Filter(TypeRequest, 0)[0]
	if got := e.Payload["duration_ms"]; got != 5.0 {
		t.Fatalf("duration_ms = %v, want 5", got)
	}
}
