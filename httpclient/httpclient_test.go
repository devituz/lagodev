package httpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGet_JSONRoundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/users/1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "name": "Ada"})
	}))
	defer srv.Close()

	resp, err := New().BaseURL(srv.URL).Get(context.Background(), "/users/1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !resp.OK() {
		t.Fatalf("want 2xx, got %d", resp.Status())
	}
	var u struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := resp.JSON(&u); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if u.ID != 1 || u.Name != "Ada" {
		t.Fatalf("decoded = %+v", u)
	}
}

func TestPostJSON_SendsBodyAndContentType(t *testing.T) {
	var gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	resp, err := New().BaseURL(srv.URL).
		PostJSON(context.Background(), "/users", map[string]string{"name": "Ada"})
	if err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if resp.Status() != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.Status())
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type = %q", gotCT)
	}
	if !strings.Contains(gotBody, `"name":"Ada"`) {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestRetry_OnTransientFailure(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n < 3 {
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
		t.Fatalf("want OK after retries, got %d", resp.Status())
	}
	if hits != 3 {
		t.Fatalf("server hit %d times, expected 3", hits)
	}
}

func TestRetry_GivesUpAfterMaxAttempts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	resp, err := New().BaseURL(srv.URL).Retry(2).Backoff(time.Millisecond).Get(context.Background(), "/")
	// Final attempt returned a response (502), so we get a response,
	// not a transport error.
	if err != nil {
		t.Fatalf("expected response, got err %v", err)
	}
	if resp.Status() != http.StatusBadGateway {
		t.Fatalf("want final 502, got %d", resp.Status())
	}
}

func TestBearerToken_SentAsAuthorization(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer srv.Close()
	_, _ = New().BaseURL(srv.URL).BearerToken("abc").Get(context.Background(), "/")
	if got != "Bearer abc" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestQuery_AppendsToURL(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		w.WriteHeader(200)
	}))
	defer srv.Close()
	_, _ = New().BaseURL(srv.URL).Query("page", "2").Query("limit", "10").Get(context.Background(), "/users")
	if !strings.Contains(got, "page=2") || !strings.Contains(got, "limit=10") {
		t.Fatalf("query = %q", got)
	}
}

func TestClone_ImmutableConfig(t *testing.T) {
	base := New().Header("X-Base", "1")
	child := base.Header("X-Child", "2")
	if _, ok := base.headers["X-Child"]; ok {
		t.Fatal("base must not have X-Child after child clone")
	}
	if _, ok := child.headers["X-Base"]; !ok {
		t.Fatal("child must inherit X-Base")
	}
}

func TestNoBaseURL_WithAbsolutePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	resp, err := New().Get(context.Background(), srv.URL+"/x")
	if err != nil || !resp.OK() {
		t.Fatalf("absolute URL must work without BaseURL; err=%v status=%d", err, resp.Status())
	}
}

func TestNoBaseURL_RelativePathErrors(t *testing.T) {
	_, err := New().Get(context.Background(), "/x")
	if err == nil {
		t.Fatal("relative path without BaseURL must error")
	}
}
