// Package httpclient is a fluent wrapper around net/http modelled on
// Laravel's Http facade.
//
// Compared to net/http directly, the wrapper handles JSON marshalling,
// query strings, headers, basic/bearer auth, timeouts, and retries
// with exponential backoff — common boilerplate that every service
// caller writes by hand.
//
// Usage:
//
//	resp, err := httpclient.New().
//	    Timeout(5 * time.Second).
//	    Retry(3).
//	    BaseURL("https://api.example.com").
//	    BearerToken(tok).
//	    Get(ctx, "/users/42")
//	if err != nil { return err }
//	var u User
//	if err := resp.JSON(&u); err != nil { return err }
//
// Errors from the underlying transport are wrapped; non-2xx responses
// are NOT errors (callers inspect resp.Status()).
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is an immutable HTTP client configuration. Each setter
// returns a clone so chains compose safely:
//
//	base := httpclient.New().BaseURL("https://api.x.com").BearerToken(t)
//	a := base.Header("X-Trace", "1") // doesn't mutate base
type Client struct {
	httpClient *http.Client
	baseURL    string
	headers    map[string]string
	query      url.Values
	timeout    time.Duration
	retries    int
	backoff    time.Duration
}

// New returns a Client with safe defaults (10s timeout, no retries,
// stdlib transport).
func New() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		headers:    map[string]string{},
		query:      url.Values{},
		timeout:    10 * time.Second,
		backoff:    100 * time.Millisecond,
	}
}

func (c *Client) clone() *Client {
	cp := *c
	cp.headers = make(map[string]string, len(c.headers))
	for k, v := range c.headers {
		cp.headers[k] = v
	}
	cp.query = url.Values{}
	for k, vs := range c.query {
		cp.query[k] = append([]string{}, vs...)
	}
	return &cp
}

// BaseURL sets the prefix for relative paths.
func (c *Client) BaseURL(u string) *Client {
	cp := c.clone()
	cp.baseURL = strings.TrimRight(u, "/")
	return cp
}

// Header adds (or overwrites) a header.
func (c *Client) Header(k, v string) *Client {
	cp := c.clone()
	cp.headers[k] = v
	return cp
}

// Headers adds multiple headers in one call.
func (c *Client) Headers(m map[string]string) *Client {
	cp := c.clone()
	for k, v := range m {
		cp.headers[k] = v
	}
	return cp
}

// BearerToken sets Authorization: Bearer <token>.
func (c *Client) BearerToken(t string) *Client {
	return c.Header("Authorization", "Bearer "+t)
}

// BasicAuth sets the Authorization header with HTTP Basic credentials.
func (c *Client) BasicAuth(user, password string) *Client {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.SetBasicAuth(user, password)
	return c.Header("Authorization", r.Header.Get("Authorization"))
}

// Query adds a URL query parameter (appends — call multiple times for
// repeated keys).
func (c *Client) Query(k, v string) *Client {
	cp := c.clone()
	cp.query.Add(k, v)
	return cp
}

// Timeout overrides the per-request timeout.
func (c *Client) Timeout(d time.Duration) *Client {
	cp := c.clone()
	cp.timeout = d
	cp.httpClient = &http.Client{Timeout: d, Transport: c.httpClient.Transport}
	return cp
}

// Retry sets how many times to retry on transport errors or 5xx /
// 429 responses. Default 0 (no retries). Backoff grows exponentially
// from the value passed to Backoff().
func (c *Client) Retry(n int) *Client {
	cp := c.clone()
	if n < 0 {
		n = 0
	}
	cp.retries = n
	return cp
}

// Backoff sets the initial retry delay; subsequent attempts double it.
func (c *Client) Backoff(d time.Duration) *Client {
	cp := c.clone()
	cp.backoff = d
	return cp
}

// Transport replaces the underlying http.RoundTripper (e.g. for mocks
// or to enforce TLS pinning).
func (c *Client) Transport(rt http.RoundTripper) *Client {
	cp := c.clone()
	cp.httpClient = &http.Client{Timeout: c.timeout, Transport: rt}
	return cp
}

// --- request helpers ---------------------------------------------------

// Get issues a GET to path.
func (c *Client) Get(ctx context.Context, path string) (*Response, error) {
	return c.do(ctx, http.MethodGet, path, nil, nil)
}

// Delete issues a DELETE.
func (c *Client) Delete(ctx context.Context, path string) (*Response, error) {
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// PostJSON marshals body as JSON and POSTs it.
func (c *Client) PostJSON(ctx context.Context, path string, body any) (*Response, error) {
	return c.sendJSON(ctx, http.MethodPost, path, body)
}

// PutJSON marshals body as JSON and PUTs it.
func (c *Client) PutJSON(ctx context.Context, path string, body any) (*Response, error) {
	return c.sendJSON(ctx, http.MethodPut, path, body)
}

// PatchJSON marshals body as JSON and PATCHes it.
func (c *Client) PatchJSON(ctx context.Context, path string, body any) (*Response, error) {
	return c.sendJSON(ctx, http.MethodPatch, path, body)
}

func (c *Client) sendJSON(ctx context.Context, method, path string, body any) (*Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("httpclient: marshal body: %w", err)
	}
	return c.do(ctx, method, path, bytes.NewReader(buf), map[string]string{"Content-Type": "application/json"})
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, extra map[string]string) (*Response, error) {
	target, err := c.resolveURL(path)
	if err != nil {
		return nil, err
	}
	var data []byte
	if body != nil {
		// Read into memory once so we can replay on retry.
		data, err = io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("httpclient: read body: %w", err)
		}
	}

	var lastErr error
	attempts := c.retries + 1
	delay := c.backoff
	for i := 0; i < attempts; i++ {
		var rdr io.Reader
		if data != nil {
			rdr = bytes.NewReader(data)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, rdr)
		if err != nil {
			return nil, fmt.Errorf("httpclient: build request: %w", err)
		}
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
		} else if shouldRetry(resp.StatusCode) && i < attempts-1 {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("httpclient: retryable status %d", resp.StatusCode)
		} else {
			return readResponse(resp)
		}
		if i < attempts-1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
		}
	}
	if lastErr == nil {
		lastErr = errors.New("httpclient: no attempts made")
	}
	return nil, lastErr
}

func (c *Client) resolveURL(path string) (string, error) {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return appendQuery(path, c.query), nil
	}
	if c.baseURL == "" {
		return "", errors.New("httpclient: no BaseURL and path is not absolute")
	}
	full := c.baseURL + "/" + strings.TrimLeft(path, "/")
	return appendQuery(full, c.query), nil
}

func appendQuery(u string, q url.Values) string {
	if len(q) == 0 {
		return u
	}
	if strings.Contains(u, "?") {
		return u + "&" + q.Encode()
	}
	return u + "?" + q.Encode()
}

func shouldRetry(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// Response wraps an http.Response with helpers and an already-read body
// so callers don't need to remember to Close.
type Response struct {
	status  int
	headers http.Header
	body    []byte
}

func readResponse(resp *http.Response) (*Response, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("httpclient: read response: %w", err)
	}
	return &Response{status: resp.StatusCode, headers: resp.Header, body: b}, nil
}

// Status returns the HTTP status code.
func (r *Response) Status() int { return r.status }

// OK reports whether the status is 2xx.
func (r *Response) OK() bool { return r.status >= 200 && r.status < 300 }

// Header returns the response header value.
func (r *Response) Header(k string) string { return r.headers.Get(k) }

// Body returns the raw body bytes (already drained from the wire).
func (r *Response) Body() []byte { return r.body }

// String returns the body as a string.
func (r *Response) String() string { return string(r.body) }

// JSON decodes the body into dst.
func (r *Response) JSON(dst any) error {
	if len(r.body) == 0 {
		return errors.New("httpclient: empty body")
	}
	return json.Unmarshal(r.body, dst)
}
