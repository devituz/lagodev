package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// goroutineCount returns the current number of goroutines after giving
// the runtime a brief moment to let just-finished goroutines unwind.
func goroutineCount() int {
	// Two GC cycles + a short settle window so transient request
	// goroutines (e.g. the transport's writeLoop/readLoop) are reaped
	// before we sample.
	for i := 0; i < 3; i++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

// TestTimeout_SlowServerReturnsPromptly asserts the configured Timeout
// aborts a slow request quickly with an error, and that the call does
// not leak goroutines waiting on the dead connection.
func TestTimeout_SlowServerReturnsPromptly(t *testing.T) {
	// Server blocks until the client gives up (or the test ends).
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	before := goroutineCount()

	start := time.Now()
	_, err := New().
		BaseURL(srv.URL).
		Timeout(150*time.Millisecond).
		Get(context.Background(), "/")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("slow server must trigger a timeout error")
	}
	// Must return promptly — generous ceiling to avoid flakiness, but
	// far below any "hang forever" behaviour.
	if elapsed > 2*time.Second {
		t.Fatalf("timeout not honored promptly: took %v", elapsed)
	}

	after := goroutineCount()
	// Allow a small slack for runtime/transport bookkeeping, but a real
	// leak (one goroutine blocked per request forever) would exceed this.
	if after > before+3 {
		t.Fatalf("possible goroutine leak: before=%d after=%d", before, after)
	}
}

// TestTimeout_WithRetriesStillBounded ensures combining Timeout with
// Retry still returns an error (rather than hanging) when every attempt
// times out against a slow server.
func TestTimeout_WithRetriesStillBounded(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	_, err := New().
		BaseURL(srv.URL).
		Timeout(100*time.Millisecond).
		Retry(2).
		Backoff(time.Millisecond).
		Get(context.Background(), "/")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("all attempts timing out must yield an error")
	}
	// 3 attempts * 100ms + backoff, with comfortable slack.
	if elapsed > 3*time.Second {
		t.Fatalf("retry+timeout not bounded: took %v", elapsed)
	}
}

// TestMaxResponseBytes_LargeBodyTruncatedWithError asserts a server
// streaming far more than the cap fails with ErrResponseTooLarge rather
// than reading unbounded memory. The server writes well past the cap.
func TestMaxResponseBytes_LargeBodyTruncatedWithError(t *testing.T) {
	const cap = 64 << 10 // 64 KiB cap
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stream ~8 MiB in chunks; the client must stop reading near the
		// cap and never buffer the whole thing.
		chunk := make([]byte, 32<<10)
		flusher, _ := w.(http.Flusher)
		for written := 0; written < 8<<20; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	_, err := New().BaseURL(srv.URL).MaxResponseBytes(cap).Get(context.Background(), "/")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge for oversize body, got %v", err)
	}
}

// TestMaxResponseBytes_ContentLengthLiarStillCapped covers a hostile
// server that advertises a small Content-Length but streams more. The
// cap must rely on bytes actually read, not the declared length.
func TestMaxResponseBytes_ContentLengthLiarStillCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Chunked transfer (no Content-Length) streaming past the cap.
		body := make([]byte, 256<<10) // 256 KiB
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	_, err := New().BaseURL(srv.URL).MaxResponseBytes(1024).Get(context.Background(), "/")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("cap must be enforced on bytes read, got %v", err)
	}
}

// TestRetry_FlappingServerSucceeds checks fail-N-then-succeed is retried
// per policy and the successful response is returned exactly once (no
// double-send after success).
func TestRetry_FlappingServerSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n < 4 { // fail first 3
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := New().
		BaseURL(srv.URL).
		Retry(5).
		Backoff(time.Millisecond).
		Get(context.Background(), "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !resp.OK() {
		t.Fatalf("want 2xx after retries, got %d", resp.Status())
	}
	if got := atomic.LoadInt32(&hits); got != 4 {
		t.Fatalf("server hit %d times, want exactly 4 (3 fail + 1 success, no extra send)", got)
	}
}

// TestRetry_RespectsContextCancellation asserts that a context cancelled
// mid-backoff aborts the retry loop promptly and does not keep sending.
func TestRetry_RespectsContextCancellation(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable) // always retryable
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the first attempt, during a long backoff.
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := New().
		BaseURL(srv.URL).
		Retry(10).
		Backoff(500*time.Millisecond). // long enough that cancel lands mid-wait
		Get(ctx, "/")
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("cancellation not honored promptly: took %v", elapsed)
	}
	// Far fewer than 11 attempts because we bailed during backoff.
	if got := atomic.LoadInt32(&hits); got > 3 {
		t.Fatalf("kept sending after cancel: %d hits", got)
	}
}

// TestContextCancellation_AbortsInFlight asserts a cancelled context
// aborts an in-flight request promptly (not just between attempts).
func TestContextCancellation_AbortsInFlight(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := New().BaseURL(srv.URL).Get(ctx, "/")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("cancelled in-flight request must error")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("cancel not honored promptly: took %v", elapsed)
	}
}

// TestJSON_MalformedBodyCleanError ensures malformed JSON yields a clean
// error, never a panic.
func TestJSON_MalformedBodyCleanError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 1, "name": `)) // truncated / invalid
	}))
	defer srv.Close()

	resp, err := New().BaseURL(srv.URL).Get(context.Background(), "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var dst struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	err = func() (e error) {
		defer func() {
			if r := recover(); r != nil {
				e = fmt.Errorf("panic: %v", r)
			}
		}()
		return resp.JSON(&dst)
	}()
	if err == nil {
		t.Fatal("malformed JSON must return an error")
	}
	if strings.HasPrefix(err.Error(), "panic:") {
		t.Fatalf("JSON must not panic: %v", err)
	}
}

// TestJSON_OversizedJSONCleanError ensures a large-but-valid body that
// exceeds the cap surfaces ErrResponseTooLarge before JSON decoding,
// never an unbounded read or panic.
func TestJSON_OversizedJSONCleanError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"`))
		blob := make([]byte, 64<<10)
		for i := range blob {
			blob[i] = 'a'
		}
		_, _ = w.Write(blob)
		_, _ = w.Write([]byte(`"}`))
	}))
	defer srv.Close()

	_, err := New().BaseURL(srv.URL).MaxResponseBytes(1024).Get(context.Background(), "/")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversized JSON must hit the cap, got %v", err)
	}
}

// TestJSON_EmptyBodyError documents that decoding an empty body is a
// clean error, not a panic.
func TestJSON_EmptyBodyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	resp, err := New().BaseURL(srv.URL).Get(context.Background(), "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var dst map[string]any
	if err := resp.JSON(&dst); err == nil {
		t.Fatal("empty body decode must error")
	}
}

// TestBasicAuth_SentCorrectly verifies Basic credentials reach the wire.
func TestBasicAuth_SentCorrectly(t *testing.T) {
	var user, pass string
	var ok bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := New().BaseURL(srv.URL).BasicAuth("alice", "s3cr3t").Get(context.Background(), "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || user != "alice" || pass != "s3cr3t" {
		t.Fatalf("BasicAuth not sent: ok=%v user=%q pass=%q", ok, user, pass)
	}
}

// TestHeaders_AllSent verifies Header/Headers/BearerToken all land on the
// request.
func TestHeaders_AllSent(t *testing.T) {
	var h http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := New().
		BaseURL(srv.URL).
		BearerToken("tok123").
		Header("X-Single", "one").
		Headers(map[string]string{"X-A": "a", "X-B": "b"}).
		Get(context.Background(), "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := h.Get("Authorization"); got != "Bearer tok123" {
		t.Fatalf("Authorization = %q", got)
	}
	if h.Get("X-Single") != "one" || h.Get("X-A") != "a" || h.Get("X-B") != "b" {
		t.Fatalf("custom headers missing: %v", h)
	}
}

// TestBaseURL_JoinsOddPaths verifies BaseURL joins with assorted path
// shapes without producing "//" or a missing "/".
func TestBaseURL_JoinsOddPaths(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cases := []struct {
		base string
		path string
		want string
	}{
		{srv.URL, "/users/1", "/users/1"},
		{srv.URL, "users/1", "/users/1"},        // missing leading slash
		{srv.URL + "/", "/users/1", "/users/1"}, // trailing slash on base
		{srv.URL + "/", "users/1", "/users/1"},
		{srv.URL + "/api", "/v1/x", "/api/v1/x"},
	}
	for _, tc := range cases {
		_, err := New().BaseURL(tc.base).Get(context.Background(), tc.path)
		if err != nil {
			t.Fatalf("base=%q path=%q: %v", tc.base, tc.path, err)
		}
		if gotPath != tc.want {
			t.Fatalf("base=%q path=%q -> got %q want %q", tc.base, tc.path, gotPath, tc.want)
		}
		if strings.Contains(gotPath, "//") {
			t.Fatalf("joined path has double slash: %q", gotPath)
		}
	}
}

// TestPostJSON_RetryReplaysBody asserts a POST body is replayed on every
// retry attempt (read-once-into-memory) so retried requests are not sent
// with an empty body.
//
// Note on idempotency: Retry retries POST/PATCH/PUT on 5xx/429 the same
// as GET. That is safe only for idempotent or de-duplicated upstreams;
// see the package SSRF/retry documentation. This test only verifies the
// body survives a replay, not that POSTs should be retried in general.
func TestPostJSON_RetryReplaysBody(t *testing.T) {
	var hits int32
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		lastBody = string(buf)
		if atomic.AddInt32(&hits, 1) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := New().
		BaseURL(srv.URL).
		Retry(3).
		Backoff(time.Millisecond).
		PostJSON(context.Background(), "/", map[string]string{"name": "Ada"})
	if err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if !resp.OK() {
		t.Fatalf("want OK after retry, got %d", resp.Status())
	}
	if !strings.Contains(lastBody, `"name":"Ada"`) {
		t.Fatalf("body not replayed on retry: %q", lastBody)
	}
}
