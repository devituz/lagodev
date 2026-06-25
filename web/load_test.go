package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// loadController is a minimal ResourceController exercising the full
// (value, error) handler shape plus JSON bind+respond under load.
type loadController struct{}

func (loadController) Index(c *Context) (any, error) {
	return map[string]string{"resource": "items"}, nil
}
func (loadController) Show(c *Context) (any, error) {
	return map[string]string{"id": c.Param("id")}, nil
}
func (loadController) Store(c *Context) (any, error) {
	var p struct {
		Title string `json:"title"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, nil // Bind already wrote 400
	}
	return c.Created(map[string]string{"title": p.Title}), nil
}
func (loadController) Update(c *Context) (any, error) {
	return map[string]string{"id": c.Param("id")}, nil
}
func (loadController) Destroy(c *Context) (any, error) { return nil, nil }

// newLoadApp builds a realistic app: default middleware (Logger+Recovery),
// the full security stack, static + param + group routes, and a resource.
// The Logger writes to io.Discard so it doesn't dominate the benchmark.
func newLoadApp() *App {
	app := New(WithoutDefaultMiddleware())
	silent := log.New(io.Discard, "", 0)
	app.Use(
		Logger(silent),
		Recovery(silent),
		RequestID(),
		SecurityHeaders(),
		BodyLimit(1<<20),
	)

	// Use a concrete static path (not "/") — registering "/" on net/http's
	// ServeMux makes it a catch-all subtree, which would shadow the 404
	// shape this load mix exercises.
	app.Get("/ping", func(c *Context) (any, error) {
		return map[string]string{"ok": "1"}, nil
	})
	app.Get("/users/{id}", func(c *Context) (any, error) {
		return map[string]string{"user": c.Param("id")}, nil
	})
	app.Group("/api/v1", func(g *Router) {
		g.Resource("items", loadController{})
	})
	return app
}

// TestLoad_SustainedConcurrency drives a live httptest server with many
// concurrent goroutines mixing static routes, param routes, JSON
// bind+respond, and 404s. It asserts zero panics, no goroutine leak, and
// reports rough throughput. Run with -race to catch data races.
func TestLoad_SustainedConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in -short mode")
	}

	app := newLoadApp()

	mux := http.NewServeMux()
	for _, rt := range app.Routes() {
		method, path, h := rt.Method, rt.Path, rt.Handler
		mux.HandleFunc(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			ctx := newContext(w, r, app.conn)
			_, _ = h(ctx)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        512,
			MaxIdleConnsPerHost: 512,
			MaxConnsPerHost:     512,
		},
	}

	// Let server goroutines settle before measuring the baseline.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	const (
		workers       = 64
		reqPerWorker  = 1500
		totalRequests = workers * reqPerWorker
	)

	var (
		ok2xx  int64
		got404 int64
		errs   int64
	)

	// Each worker cycles through a mix of request shapes.
	shapes := []func(w int, i int) (*http.Request, int){
		// static route
		func(w, i int) (*http.Request, int) {
			r, _ := http.NewRequest(http.MethodGet, srv.URL+"/ping", nil)
			return r, http.StatusOK
		},
		// param route
		func(w, i int) (*http.Request, int) {
			r, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/users/%d", srv.URL, w*1000+i), nil)
			return r, http.StatusOK
		},
		// grouped resource index
		func(w, i int) (*http.Request, int) {
			r, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/items", nil)
			return r, http.StatusOK
		},
		// JSON bind + 201
		func(w, i int) (*http.Request, int) {
			body, _ := json.Marshal(map[string]string{"title": fmt.Sprintf("t-%d-%d", w, i)})
			r, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/items", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			return r, http.StatusCreated
		},
		// unknown path → 404
		func(w, i int) (*http.Request, int) {
			r, _ := http.NewRequest(http.MethodGet, srv.URL+"/does/not/exist", nil)
			return r, http.StatusNotFound
		},
	}

	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < reqPerWorker; i++ {
				build := shapes[(w+i)%len(shapes)]
				req, want := build(w, i)
				resp, err := client.Do(req)
				if err != nil {
					atomic.AddInt64(&errs, 1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				switch {
				case resp.StatusCode == http.StatusNotFound:
					atomic.AddInt64(&got404, 1)
				case resp.StatusCode >= 200 && resp.StatusCode < 300:
					atomic.AddInt64(&ok2xx, 1)
				default:
					atomic.AddInt64(&errs, 1)
				}
				if want == http.StatusNotFound && resp.StatusCode != http.StatusNotFound {
					t.Errorf("expected 404 for unknown path, got %d", resp.StatusCode)
				}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)

	if errs > 0 {
		t.Fatalf("encountered %d transport/5xx errors under load", errs)
	}

	rps := float64(totalRequests) / elapsed.Seconds()
	t.Logf("load: %d requests in %s = %.0f req/s (2xx=%d 404=%d)",
		totalRequests, elapsed.Round(time.Millisecond), rps, ok2xx, got404)

	// Close idle keep-alive connections so their reader goroutines exit,
	// then allow the server to reap them before counting.
	client.CloseIdleConnections()
	srv.CloseClientConnections()
	if !waitGoroutines(before, 2*time.Second) {
		// Not fatal on its own (server bookkeeping goroutines can linger),
		// but a large persistent delta indicates a leak.
		after := stableGoroutines()
		if after > before+20 {
			t.Fatalf("goroutine leak: before=%d after=%d (delta=%d)", before, after, after-before)
		}
		t.Logf("goroutines: before=%d after=%d (within tolerance)", before, after)
	} else {
		t.Logf("goroutines: before=%d after<=before (no leak)", before)
	}
}

// waitGoroutines polls until NumGoroutine drops back to <= target or the
// deadline passes. Returns true if it reached the target.
func waitGoroutines(target int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= target {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// stableGoroutines returns a settled goroutine count after a short grace.
func stableGoroutines() int {
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	runtime.GC()
	return runtime.NumGoroutine()
}

// TestLoad_NoPanicOnHandlerPanic confirms a panicking handler under
// concurrency is recovered by Recovery() and never crashes the server.
func TestLoad_NoPanicOnHandlerPanic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in -short mode")
	}
	silent := log.New(io.Discard, "", 0)
	app := New(WithoutDefaultMiddleware())
	app.Use(Logger(silent), Recovery(silent))
	app.Get("/boom", func(c *Context) (any, error) {
		panic("kaboom")
	})

	mux := http.NewServeMux()
	for _, rt := range app.Routes() {
		method, path, h := rt.Method, rt.Path, rt.Handler
		mux.HandleFunc(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			ctx := newContext(w, r, app.conn)
			_, _ = h(ctx)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var wg sync.WaitGroup
	var non500 int64
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				resp, err := http.Get(srv.URL + "/boom")
				if err != nil {
					atomic.AddInt64(&non500, 1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusInternalServerError {
					atomic.AddInt64(&non500, 1)
				}
			}
		}()
	}
	wg.Wait()
	if non500 != 0 {
		t.Fatalf("%d panicking requests were not cleanly recovered to 500", non500)
	}
}
