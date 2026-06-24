package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

// Redirect sends an HTTP redirect response (`Location` header + the
// given status code). Use 301/302/307/308 as appropriate. Marks the
// response body as written so respond() does not append a JSON tail.
func (c *Context) Redirect(code int, url string) {
	if c.bodyWritten {
		return
	}
	if code < 300 || code > 399 {
		code = http.StatusFound
	}
	http.Redirect(c.Writer, c.Request, url, code)
	c.statusWritten = true
	c.bodyWritten = true
}

// File serves a file from disk with `Content-Disposition: inline`. The
// browser shows the file (PDF preview, image render, …) instead of
// downloading it.
//
// Path is passed verbatim to http.ServeFile — apply filepath.Clean and
// your own sandboxing if the path is derived from user input.
func (c *Context) File(path string) {
	if c.bodyWritten {
		return
	}
	c.Writer.Header().Set("Content-Disposition",
		fmt.Sprintf(`inline; filename=%q`, filepath.Base(path)))
	c.bodyWritten = true
	c.statusWritten = true
	http.ServeFile(c.Writer, c.Request, path)
}

// Attachment serves a file with `Content-Disposition: attachment` so
// the browser triggers a download. `filename` is the suggested filename
// shown to the user.
func (c *Context) Attachment(path, filename string) {
	if c.bodyWritten {
		return
	}
	if filename == "" {
		filename = filepath.Base(path)
	}
	c.Writer.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename=%q`, filename))
	c.bodyWritten = true
	c.statusWritten = true
	http.ServeFile(c.Writer, c.Request, path)
}

// Stream sends Content-Type and copies from r into the response body,
// flushing periodically if the underlying ResponseWriter is a
// http.Flusher. Use for streaming uploads (CSV exports, large JSON
// arrays produced incrementally, …) without buffering everything in
// memory.
//
// Stream sets the response status to `code` and writes the headers
// before the first byte of the body. Returns the number of bytes
// copied and any read/write error.
func (c *Context) Stream(code int, contentType string, r io.Reader) (int64, error) {
	if c.bodyWritten {
		return 0, errors.New("web: response already written")
	}
	if contentType != "" {
		c.Writer.Header().Set("Content-Type", contentType)
	}
	c.writeHeader(code)
	c.bodyWritten = true
	n, err := io.Copy(c.Writer, r)
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

// SSE writes a single Server-Sent Events frame to the client. The
// first call to SSE sets `Content-Type: text/event-stream` and the
// other SSE-specific headers; subsequent calls just append more
// events. The connection stays open until the handler returns or the
// client disconnects.
//
// Usage:
//
//	for evt := range source {
//	    if err := c.SSE("message", evt.JSON()); err != nil { return nil, err }
//	    select {
//	    case <-c.Ctx().Done(): return nil, nil
//	    default:
//	    }
//	}
func (c *Context) SSE(event, data string) error {
	if !c.statusWritten {
		h := c.Writer.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no") // nginx
		c.writeHeader(http.StatusOK)
		c.bodyWritten = true
	}
	// Each SSE frame ends with a blank line. Multi-line data values are
	// split into multiple "data:" lines per the spec.
	var b strings.Builder
	if event != "" {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteByte('\n')
	}
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	if _, err := io.WriteString(c.Writer, b.String()); err != nil {
		return err
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
