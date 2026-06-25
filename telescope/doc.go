// Package telescope provides a Laravel-Telescope-style in-process debug
// dashboard for the lagodev framework, built entirely on the Go standard
// library (net/http, html/template, sync, encoding/json, time).
//
// The package is intentionally self-contained and decoupled from the rest of
// the framework. It never reaches into observability, orm, cache or mail
// internals; instead the application *pushes* what happened through a small
// Record* API and telescope stores it in a bounded, concurrency-safe ring
// buffer. This keeps telescope testable in isolation and lets it sit behind
// any router or middleware stack.
//
// # Entries
//
// Everything telescope stores is an Entry: an immutable record with an ID, a
// Type (Request, Query, Job, Cache, Mail, Exception, Log), a timestamp, a set of
// Tags and a free-form payload map. Typed helpers build the well-known shapes:
//
//	rec := telescope.NewRecorder(telescope.Options{Capacity: 500})
//	rec.RecordQuery(ctx, telescope.QueryEntry{
//		SQL:      "select * from users where id = ?",
//		Bindings: []any{42},
//		Duration: 3 * time.Millisecond,
//	})
//
// # HTTP middleware
//
// Middleware times each request, assigns it a request id (propagated through
// the context so child entries can be correlated) and records a Request entry
// when the handler returns:
//
//	rec := telescope.NewRecorder(telescope.Options{})
//	srv := rec.Middleware()(mux)
//	http.ListenAndServe(":8080", srv)
//
// Inside a handler, the active request id is available so that queries, cache
// hits and exceptions recorded during the request are tagged with it:
//
//	id, _ := telescope.RequestIDFromContext(r.Context())
//
// # Dashboard
//
// Handler returns an http.Handler serving an HTML dashboard plus a small JSON
// API. Everything is rendered through html/template, so recorded payloads are
// auto-escaped (a stored-XSS defense) even when they contain untrusted data:
//
//	mux.Handle("/telescope/", http.StripPrefix("/telescope",
//		rec.Handler(telescope.HandlerOptions{})))
//
// The Handler is deliberately auth-agnostic: it exposes SQL, bindings, request
// IPs, log context and stack traces to anyone who can reach it. Never mount it
// publicly. Gate it with RequireBasicAuth, your own app middleware, or simply
// do not mount it in production. See docs/TELESCOPE.md for the prod-safe wiring.
//
// Routes (relative to the mount point):
//
//	GET  /            HTML overview: counts per type + recent entries
//	GET  /{type}      HTML list of entries of one type
//	GET  /entry/{id}  HTML detail view with a pretty-printed payload
//	GET  /api/entries JSON entry list (honours ?type= and ?limit=)
//	POST /clear       clears every stored entry, then redirects to /
//
// # N+1 heuristic
//
// RecordQuery normalises each statement (literals and bind placeholders are
// collapsed) and, when the same normalised statement is recorded more than
// once under the same request id, flags the duplicates with the "n+1" tag and
// a payload "n_plus_one" boolean. This surfaces the classic ORM lazy-loading
// problem without any ORM hook.
package telescope
