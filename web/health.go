package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// HealthCheck is a probe function used by Ready(). It must return
// quickly — Kubernetes typically calls /readyz every 5–10 seconds.
//
//	app.Ready(
//	    web.HealthCheck{Name: "db", Probe: func(ctx context.Context) error {
//	        return conn.DB.PingContext(ctx)
//	    }},
//	)
type HealthCheck struct {
	Name  string
	Probe func(ctx context.Context) error
	// Timeout caps the probe; default 2 seconds.
	Timeout time.Duration
}

// Health registers a liveness endpoint at `/healthz` that always
// returns 200 once the app is up. Kubernetes uses this to decide
// whether to restart the pod.
func (a *App) Health() {
	a.Get("/healthz", func(c *Context) (any, error) {
		return map[string]string{"status": "ok"}, nil
	})
}

// Ready registers a readiness endpoint at `/readyz` that runs every
// supplied probe in parallel. If any probe returns an error the
// endpoint replies with 503 + the name of the first failing check;
// otherwise 200. Kubernetes uses this to decide whether to route
// traffic to the pod.
//
// `Ready` is additive — call it once, listing every probe the app
// needs.
func (a *App) Ready(checks ...HealthCheck) {
	a.Get("/readyz", func(c *Context) (any, error) {
		ctx := c.Ctx()
		results := make(map[string]string, len(checks))
		var mu sync.Mutex
		var wg sync.WaitGroup
		var firstFail string

		for _, ch := range checks {
			ch := ch
			if ch.Timeout == 0 {
				ch.Timeout = 2 * time.Second
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				pctx, cancel := context.WithTimeout(ctx, ch.Timeout)
				defer cancel()
				err := ch.Probe(pctx)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					results[ch.Name] = "fail: " + err.Error()
					if firstFail == "" {
						firstFail = ch.Name
					}
				} else {
					results[ch.Name] = "ok"
				}
			}()
		}
		wg.Wait()

		body := map[string]any{
			"status": "ready",
			"checks": results,
		}
		if firstFail != "" {
			body["status"] = "not_ready"
			body["first_fail"] = firstFail
			c.JSON(http.StatusServiceUnavailable, body)
			return nil, nil
		}
		return body, nil
	})
}

// Test drives a single request through the App's middleware chain
// and route table without binding a port. Returns the
// httptest.ResponseRecorder so tests can assert on Code, Body, and
// headers.
//
//	rec := app.Test(httptest.NewRequest("GET", "/users/42", nil))
//	if rec.Code != 200 { t.Fatal(...) }
//
// The app does NOT need to be Run() — Test materialises the route
// table on demand. Safe to call concurrently with other Test()
// invocations, but not with a live Run().
func (a *App) Test(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	for _, rt := range a.Routes() {
		method, path, h := rt.Method, rt.Path, rt.Handler
		mux.HandleFunc(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			ctx := newContext(w, r, a.conn)
			// respond() already runs as the innermost chain layer (see
			// web/router.go withRespond); the handler writes the response.
			_, _ = h(ctx)
		})
	}
	mux.ServeHTTP(rec, req)
	return rec
}
